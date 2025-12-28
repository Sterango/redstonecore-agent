package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config holds backup configuration
type Config struct {
	ServerUUID    string
	ServerDataDir string
	BackupDir     string
	Type          string // "full" or "world"
	BackupUUID    string // UUID of the backup record in Laravel
	OnProgress    func(stage string, message string, progress, total int)
}

// Result holds the result of a backup operation
type Result struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
}

// Info represents backup metadata
type Info struct {
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	ServerUUID  string    `json:"server_uuid"`
}

// excludedDirs are directories to exclude from full backups
var excludedDirs = map[string]bool{
	"logs":          true,
	"crash-reports": true,
}

// excludedFiles are files to exclude from all backups
var excludedFiles = map[string]bool{
	".uuid": true,
}

// worldDirs are directories to include in world-only backups
var worldDirs = []string{
	"world",
	"world_nether",
	"world_the_end",
	"dimensions",
}

// Create creates a new backup
func Create(cfg Config) (*Result, error) {
	// Ensure backup directory exists
	if err := os.MkdirAll(cfg.BackupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate filename
	timestamp := time.Now().Format("2006-01-02_150405")
	filename := fmt.Sprintf("backup_%s_%s.tar.gz", timestamp, cfg.Type)
	backupPath := filepath.Join(cfg.BackupDir, filename)

	// Report progress - preparing
	if cfg.OnProgress != nil {
		cfg.OnProgress("preparing", "Scanning files to backup...", 0, 0)
	}

	// Collect files to backup
	var files []string

	if cfg.Type == "world" {
		files, _ = collectWorldFiles(cfg.ServerDataDir)
	} else {
		files, _ = collectFullFiles(cfg.ServerDataDir)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no files found to backup")
	}

	// Report progress - archiving
	if cfg.OnProgress != nil {
		cfg.OnProgress("archiving", fmt.Sprintf("Creating backup of %d files...", len(files)), 0, len(files))
	}

	// Create the backup file
	file, err := os.Create(backupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup file: %w", err)
	}
	defer file.Close()

	// Create gzip writer
	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Write files to archive
	for i, filePath := range files {
		relPath, _ := filepath.Rel(cfg.ServerDataDir, filePath)

		if cfg.OnProgress != nil && i%10 == 0 {
			cfg.OnProgress("archiving", fmt.Sprintf("Archiving: %s", relPath), i, len(files))
		}

		if err := addFileToTar(tarWriter, filePath, relPath); err != nil {
			// Log but continue - some files might be locked
			continue
		}
	}

	// Close writers to flush data
	tarWriter.Close()
	gzWriter.Close()
	file.Close()

	// Get final file size
	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat backup file: %w", err)
	}

	// Report progress - complete
	if cfg.OnProgress != nil {
		cfg.OnProgress("complete", fmt.Sprintf("Backup complete: %s (%.2f MB)", filename, float64(info.Size())/(1024*1024)), len(files), len(files))
	}

	return &Result{
		Filename: filename,
		Size:     info.Size(),
		Path:     backupPath,
	}, nil
}

// collectFullFiles collects all files for a full backup
func collectFullFiles(serverDir string) ([]string, int64) {
	var files []string
	var totalSize int64

	filepath.Walk(serverDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		// Skip root directory itself
		if path == serverDir {
			return nil
		}

		relPath, _ := filepath.Rel(serverDir, path)
		baseName := filepath.Base(path)

		// Check if directory should be excluded
		if info.IsDir() {
			parts := strings.Split(relPath, string(filepath.Separator))
			if excludedDirs[parts[0]] {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if file should be excluded
		if excludedFiles[baseName] {
			return nil
		}

		files = append(files, path)
		totalSize += info.Size()
		return nil
	})

	return files, totalSize
}

// collectWorldFiles collects world files for a world-only backup
func collectWorldFiles(serverDir string) ([]string, int64) {
	var files []string
	var totalSize int64

	for _, worldDir := range worldDirs {
		worldPath := filepath.Join(serverDir, worldDir)
		if _, err := os.Stat(worldPath); os.IsNotExist(err) {
			continue
		}

		filepath.Walk(worldPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			if !info.IsDir() {
				files = append(files, path)
				totalSize += info.Size()
			}
			return nil
		})
	}

	return files, totalSize
}

// addFileToTar adds a file to the tar archive
func addFileToTar(tw *tar.Writer, filePath, relPath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	header.Name = relPath

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

// List returns all backups for a server
func List(backupDir string) ([]Info, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Info{}, nil
		}
		return nil, err
	}

	var backups []Info
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Parse type from filename: backup_2025-12-17_143052_full.tar.gz
		backupType := "unknown"
		if strings.Contains(name, "_full.tar.gz") {
			backupType = "full"
		} else if strings.Contains(name, "_world.tar.gz") {
			backupType = "world"
		}

		backups = append(backups, Info{
			Filename:  name,
			Size:      info.Size(),
			Type:      backupType,
			CreatedAt: info.ModTime(),
		})
	}

	// Sort by creation time, newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// Delete removes a backup file
func Delete(backupDir, filename string) error {
	// Sanitize filename to prevent directory traversal
	filename = filepath.Base(filename)
	if !strings.HasSuffix(filename, ".tar.gz") {
		return fmt.Errorf("invalid backup filename")
	}

	backupPath := filepath.Join(backupDir, filename)
	return os.Remove(backupPath)
}

// GetReader returns a reader for streaming a backup file
func GetReader(backupDir, filename string) (io.ReadCloser, int64, error) {
	// Sanitize filename
	filename = filepath.Base(filename)
	if !strings.HasSuffix(filename, ".tar.gz") {
		return nil, 0, fmt.Errorf("invalid backup filename")
	}

	backupPath := filepath.Join(backupDir, filename)

	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, 0, err
	}

	file, err := os.Open(backupPath)
	if err != nil {
		return nil, 0, err
	}

	return file, info.Size(), nil
}

// GetPath returns the full path to a backup file
func GetPath(backupDir, filename string) (string, error) {
	// Sanitize filename
	filename = filepath.Base(filename)
	if !strings.HasSuffix(filename, ".tar.gz") {
		return "", fmt.Errorf("invalid backup filename")
	}

	backupPath := filepath.Join(backupDir, filename)

	if _, err := os.Stat(backupPath); err != nil {
		return "", err
	}

	return backupPath, nil
}
