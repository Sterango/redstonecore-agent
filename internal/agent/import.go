package agent

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/minecraft"
)

// Importing an existing server happens in two commands, because the user gets
// to review what we found before anything becomes a real server:
//
//	import_inspect  — pull the staged zip, extract it, detect the config, report
//	import_finalize — the user confirmed; move the files into place and register
//
// The archive can be 10GB, so everything here streams: the download goes
// straight to disk with Range-resume, and extraction copies entry by entry.

const (
	// importDownloadChunkTimeout bounds a single HTTP attempt. A stalled pull is
	// retried from where it stopped rather than restarted.
	importDownloadTimeout = 6 * time.Hour

	// importDownloadAttempts is how many times a broken transfer resumes before
	// the import is failed.
	importDownloadAttempts = 5

	// importProgressInterval throttles progress posts to the panel.
	importProgressInterval = 2 * time.Second
)

// importWorkDir is where an in-flight import is staged on the agent host.
func (a *Agent) importWorkDir(importID string) string {
	return filepath.Join(a.config.DataDir, "imports", importID)
}

// handleImportInspect downloads a staged archive, extracts it and reports what
// it contains. No server is created at this stage.
func (a *Agent) handleImportInspect(cmd api.Command) error {
	if cmd.Payload == nil {
		return fmt.Errorf("import_inspect requires payload")
	}

	importID, _ := cmd.Payload["import_id"].(string)
	downloadURL, _ := cmd.Payload["download_url"].(string)
	token, _ := cmd.Payload["token"].(string)
	expectedSHA, _ := cmd.Payload["sha256"].(string)
	totalSize, _ := cmd.Payload["total_size"].(float64)

	if importID == "" || downloadURL == "" || token == "" {
		return fmt.Errorf("import_inspect requires import_id, download_url and token")
	}

	log.Printf("[Import %s] starting inspection (%.2f GB)", importID, totalSize/(1<<30))

	workDir := a.importWorkDir(importID)
	if err := os.MkdirAll(workDir, 0o775); err != nil {
		a.failImport(importID, fmt.Sprintf("Could not create import workspace: %v", err))
		return err
	}

	archivePath := filepath.Join(workDir, "archive.zip")
	extractDir := filepath.Join(workDir, "extracted")

	// --- 1. Pull the archive down from the relay ---
	if err := a.downloadImport(importID, downloadURL, token, archivePath, int64(totalSize)); err != nil {
		a.failImport(importID, fmt.Sprintf("Download failed: %v", err))
		os.RemoveAll(workDir)
		return err
	}

	// --- 2. Verify it is byte-for-byte what was uploaded ---
	if expectedSHA != "" {
		a.reportImport(importID, "transferring", "Verifying archive", 100)

		actual, err := hashFileSHA256(archivePath)
		if err != nil {
			a.failImport(importID, fmt.Sprintf("Could not verify archive: %v", err))
			os.RemoveAll(workDir)
			return err
		}
		if !strings.EqualFold(actual, expectedSHA) {
			a.failImport(importID, "Archive failed integrity check — the upload was corrupted in transit.")
			os.RemoveAll(workDir)
			return fmt.Errorf("sha256 mismatch: got %s want %s", actual, expectedSHA)
		}
	}

	// --- 3. Extract ---
	if err := a.extractImport(importID, archivePath, extractDir); err != nil {
		a.failImport(importID, fmt.Sprintf("Extraction failed: %v", err))
		os.RemoveAll(workDir)
		return err
	}

	// The zip is no longer needed once extracted, and it is the single largest
	// thing on disk — free it before the user sits on the review screen.
	os.Remove(archivePath)

	// --- 4. Detect ---
	a.reportImport(importID, "detecting", "Inspecting server files", 0)

	detection := minecraft.DetectServer(extractDir)

	log.Printf("[Import %s] detected type=%s mc=%s loader=%s mods=%d plugins=%d",
		importID, detection.Type, detection.MinecraftVersion,
		detection.LoaderVersion, detection.ModCount, detection.PluginCount)

	if err := a.client.ReportImportDetected(&api.ImportDetectedRequest{
		ImportID: importID,
		Detected: detection,
	}); err != nil {
		log.Printf("[Import %s] failed to report detection: %v", importID, err)
		return err
	}

	return nil
}

// downloadImport streams the staged archive to disk, resuming with Range if the
// connection drops partway through.
func (a *Agent) downloadImport(importID, url, token, destPath string, totalSize int64) error {
	var lastErr error

	for attempt := 1; attempt <= importDownloadAttempts; attempt++ {
		// Resume from whatever is already on disk.
		var offset int64
		if info, err := os.Stat(destPath); err == nil {
			offset = info.Size()
		}

		if totalSize > 0 && offset >= totalSize {
			return nil // already complete
		}

		if attempt > 1 {
			log.Printf("[Import %s] resuming download at %d bytes (attempt %d): %v",
				importID, offset, attempt, lastErr)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		err := a.downloadImportOnce(importID, url, token, destPath, offset, totalSize)
		if err == nil {
			return nil
		}

		lastErr = err
	}

	return fmt.Errorf("download did not complete after %d attempts: %w", importDownloadAttempts, lastErr)
}

func (a *Agent) downloadImportOnce(importID, url, token, destPath string, offset, totalSize int64) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	client := &http.Client{Timeout: importDownloadTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored our Range (or we asked for the whole file): start over.
		offset = 0
	case http.StatusPartialContent:
		// Resuming as requested.
	case http.StatusRequestedRangeNotSatisfiable:
		// We already have everything.
		return nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("relay returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(destPath, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	written := offset
	lastReport := time.Now()
	buf := make([]byte, 1<<20) // 1MB

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)

			if time.Since(lastReport) >= importProgressInterval {
				percent := 0
				if totalSize > 0 {
					percent = int(written * 100 / totalSize)
				}
				a.reportImport(importID, "transferring",
					fmt.Sprintf("Transferring archive to your server (%s of %s)",
						humanBytes(written), humanBytes(totalSize)),
					percent)
				lastReport = time.Now()
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	if totalSize > 0 && written < totalSize {
		return fmt.Errorf("transfer ended early at %d of %d bytes", written, totalSize)
	}

	return nil
}

// extractImport unpacks the archive, guarding against path traversal and
// reporting progress as it goes.
func (a *Agent) extractImport(importID, archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("could not open archive: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(destDir, 0o775); err != nil {
		return err
	}

	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	total := len(zr.File)
	lastReport := time.Now()

	for i, f := range zr.File {
		// Reject entries that would escape the destination ("zip slip").
		target := filepath.Join(absDest, f.Name) // #nosec G305 - validated below
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) && target != absDest {
			log.Printf("[Import %s] skipping unsafe archive entry %q", importID, f.Name)
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o775); err != nil {
				return err
			}
			continue
		}

		// Skip symlinks rather than recreating them; an imported symlink could
		// point anywhere on the host.
		if f.Mode()&os.ModeSymlink != 0 {
			log.Printf("[Import %s] skipping symlink entry %q", importID, f.Name)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
			return err
		}

		if err := extractZipEntry(f, target); err != nil {
			return fmt.Errorf("failed to extract %s: %w", f.Name, err)
		}

		if time.Since(lastReport) >= importProgressInterval {
			a.reportImport(importID, "extracting",
				fmt.Sprintf("Extracting files (%d of %d)", i+1, total),
				int((i+1)*100/total))
			lastReport = time.Now()
		}
	}

	a.reportImport(importID, "extracting", "Extraction complete", 100)

	return nil
}

// extractZipEntry streams one entry to disk, preserving its mode so start
// scripts stay executable.
func extractZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}

	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)

	return err
}

// handleImportFinalize moves an inspected import into place as a real server,
// applying the config the user confirmed on the review screen.
func (a *Agent) handleImportFinalize(cmd api.Command) error {
	if cmd.Payload == nil {
		return fmt.Errorf("import_finalize requires payload")
	}

	importID, _ := cmd.Payload["import_id"].(string)
	serverUUID, _ := cmd.Payload["server_uuid"].(string)
	name, _ := cmd.Payload["name"].(string)
	serverType, _ := cmd.Payload["type"].(string)
	version, _ := cmd.Payload["minecraft_version"].(string)
	port, _ := cmd.Payload["port"].(float64)
	maxPlayers, _ := cmd.Payload["max_players"].(float64)
	ram, _ := cmd.Payload["allocated_ram_mb"].(float64)

	if importID == "" || serverUUID == "" || name == "" {
		return fmt.Errorf("import_finalize requires import_id, server_uuid and name")
	}

	log.Printf("[Import %s] finalizing as %q (%s %s)", importID, name, serverType, version)

	a.reportImport(importID, "importing", "Installing server files", 10)

	workDir := a.importWorkDir(importID)
	extractDir := filepath.Join(workDir, "extracted")

	if _, err := os.Stat(extractDir); err != nil {
		msg := "The extracted archive is no longer available on the agent — please re-upload."
		a.completeImport(importID, false, msg)
		return fmt.Errorf("%s: %w", msg, err)
	}

	// The detection step already worked out whether the archive wrapped the
	// server in a folder; re-resolve it so we move the right directory.
	sourceDir, _ := minecraft.ResolveServerRoot(extractDir)

	serverDir := filepath.Join(a.config.DataDir, "servers", sanitizeServerDirName(name))
	if _, err := os.Stat(serverDir); err == nil {
		msg := fmt.Sprintf("A server directory named %q already exists on this host.", filepath.Base(serverDir))
		a.completeImport(importID, false, msg)
		return fmt.Errorf("%s", msg)
	}

	if err := os.MkdirAll(filepath.Dir(serverDir), 0o775); err != nil {
		a.completeImport(importID, false, fmt.Sprintf("Could not create servers directory: %v", err))
		return err
	}

	a.reportImport(importID, "importing", "Moving files into place", 40)

	// Rename when both paths share a filesystem (the common case, since both
	// live under DataDir); fall back to a copy across devices.
	if err := os.Rename(sourceDir, serverDir); err != nil {
		log.Printf("[Import %s] rename failed (%v), falling back to copy", importID, err)
		if copyErr := copyTree(sourceDir, serverDir); copyErr != nil {
			a.completeImport(importID, false, fmt.Sprintf("Could not install files: %v", copyErr))
			return copyErr
		}
	}

	// Anything left in the workspace is scaffolding from the archive.
	os.RemoveAll(workDir)

	a.reportImport(importID, "importing", "Applying configuration", 70)

	// Mark ownership so the server survives an agent restart.
	if err := os.WriteFile(filepath.Join(serverDir, ".uuid"), []byte(serverUUID), 0o644); err != nil {
		log.Printf("[Import %s] warning: could not write .uuid: %v", importID, err)
	}

	consoleBuf := a.createConsoleBuffer(serverUUID)
	server := minecraft.NewServer(minecraft.ServerConfig{
		UUID:             serverUUID,
		Name:             name,
		Type:             minecraft.ServerType(serverType),
		MinecraftVersion: version,
		Port:             int(port),
		MaxPlayers:       int(maxPlayers),
		AllocatedRAM:     int(ram),
		DataDir:          serverDir,
		OnConsoleLine: func(line string) {
			consoleBuf.AddLine(line)
		},
		OnPlayerEvent: a.createPlayerEventCallback(serverUUID),
	})

	a.serversMu.Lock()
	a.servers[serverUUID] = server
	a.serversMu.Unlock()

	// Reconcile server.properties with the port/slots the user confirmed, while
	// leaving the rest of the imported file untouched.
	if err := server.EnsureServerProperties(); err != nil {
		log.Printf("[Import %s] warning: could not update server.properties: %v", importID, err)
	}

	a.reportImport(importID, "importing", "Finishing up", 90)

	log.Printf("[Import %s] server %q imported to %s", importID, name, serverDir)

	a.completeImport(importID, true, "Import complete")

	// Push the new server into the cloud's inventory immediately rather than
	// waiting for the next periodic sync.
	a.syncServers()

	return nil
}

// reportImport posts progress, logging rather than failing when the panel is
// briefly unreachable — progress is advisory, the import continues regardless.
func (a *Agent) reportImport(importID, status, message string, progress int) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}

	if err := a.client.ReportImportProgress(&api.ImportProgressRequest{
		ImportID: importID,
		Status:   status,
		Message:  message,
		Progress: progress,
	}); err != nil {
		log.Printf("[Import %s] progress report failed: %v", importID, err)
	}
}

func (a *Agent) failImport(importID, message string) {
	log.Printf("[Import %s] FAILED: %s", importID, message)

	if err := a.client.ReportImportProgress(&api.ImportProgressRequest{
		ImportID: importID,
		Status:   "failed",
		Message:  message,
		Progress: 0,
	}); err != nil {
		log.Printf("[Import %s] failure report failed: %v", importID, err)
	}
}

func (a *Agent) completeImport(importID string, success bool, message string) {
	if err := a.client.ReportImportComplete(&api.ImportCompleteRequest{
		ImportID: importID,
		Success:  success,
		Message:  message,
	}); err != nil {
		log.Printf("[Import %s] completion report failed: %v", importID, err)
	}
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyTree duplicates a directory tree, used when the import workspace and the
// servers directory turn out to be on different filesystems.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}

		// Skip anything that isn't a regular file (sockets, devices, symlinks).
		if !info.Mode().IsRegular() {
			return nil
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()

		_, err = io.Copy(out, in)

		return err
	})
}

// humanBytes formats a byte count for progress messages shown to the user.
func humanBytes(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
