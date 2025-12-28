package minecraft

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProgressCallback is called to report installation progress
type ProgressCallback func(stage, message string, progress, total int, currentFile string)

// ModpackInstaller handles downloading and installing CurseForge modpacks
type ModpackInstaller struct {
	httpClient       *http.Client
	cacheDir         string
	progressCallback ProgressCallback
	curseforgeAPIKey string
}

// CurseForgeManifest represents the manifest.json file format from CurseForge
type CurseForgeManifest struct {
	Minecraft    CurseForgeMinecraft `json:"minecraft"`
	ManifestType string              `json:"manifestType"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Author       string              `json:"author"`
	Files        []CurseForgeFile    `json:"files"`
	Overrides    string              `json:"overrides"`
}

// CurseForgeMinecraft contains Minecraft version and loader info
type CurseForgeMinecraft struct {
	Version    string                  `json:"version"`
	ModLoaders []CurseForgeModLoader   `json:"modLoaders"`
}

// CurseForgeModLoader represents a mod loader entry
type CurseForgeModLoader struct {
	ID      string `json:"id"`      // e.g., "neoforge-21.1.215", "forge-47.2.0", "fabric-0.15.0"
	Primary bool   `json:"primary"`
}

// CurseForgeFile represents a mod file entry in the manifest
type CurseForgeFile struct {
	ProjectID int  `json:"projectID"`
	FileID    int  `json:"fileID"`
	Required  bool `json:"required"`
}

// CurseForgeFileResponse represents the API response for a file
type CurseForgeFileResponse struct {
	Data struct {
		ID          int    `json:"id"`
		DisplayName string `json:"displayName"`
		FileName    string `json:"fileName"`
		DownloadURL string `json:"downloadUrl"`
		ServerPack  *struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"serverPack"`
		GameID int `json:"gameId"`
	} `json:"data"`
}

// CurseForgeFilesResponse represents bulk file lookup response
type CurseForgeFilesResponse struct {
	Data []struct {
		ID          int    `json:"id"`
		ModID       int    `json:"modId"`
		DisplayName string `json:"displayName"`
		FileName    string `json:"fileName"`
		DownloadURL string `json:"downloadUrl"`
	} `json:"data"`
}

// ModpackDownloadInfo contains direct download information
type ModpackDownloadInfo struct {
	URL      string
	Filename string
}

// ModpackInfo contains extracted modpack information (normalized from CurseForge format)
type ModpackInfo struct {
	Name           string
	Version        string
	MinecraftVer   string
	LoaderType     string
	LoaderVersion  string
}

// NewModpackInstaller creates a new modpack installer
func NewModpackInstaller(cacheDir string, curseforgeAPIKey string) *ModpackInstaller {
	return &ModpackInstaller{
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Modpacks can be large
		},
		cacheDir:         cacheDir,
		curseforgeAPIKey: curseforgeAPIKey,
	}
}

// SetProgressCallback sets the callback for progress updates
func (m *ModpackInstaller) SetProgressCallback(callback ProgressCallback) {
	m.progressCallback = callback
}

// reportProgress reports progress if callback is set
func (m *ModpackInstaller) reportProgress(stage, message string, progress, total int, currentFile string) {
	if m.progressCallback != nil {
		m.progressCallback(stage, message, progress, total, currentFile)
	}
}

// InstallModpack downloads and installs a CurseForge modpack
func (m *ModpackInstaller) InstallModpack(versionID string, destDir string, downloadInfo *ModpackDownloadInfo) (*ModpackInfo, error) {
	log.Printf("Installing modpack version: %s", versionID)

	m.reportProgress("preparing", "Fetching modpack information...", 0, 100, "")

	if downloadInfo == nil || downloadInfo.URL == "" {
		return nil, fmt.Errorf("download URL is required for CurseForge modpacks")
	}

	log.Printf("Using direct download URL: %s", downloadInfo.URL)
	modpackFilename := downloadInfo.Filename
	if modpackFilename == "" {
		modpackFilename = "modpack.zip"
	}

	m.reportProgress("downloading_modpack", fmt.Sprintf("Downloading %s...", modpackFilename), 5, 100, modpackFilename)

	// Download the modpack zip file
	modpackPath := filepath.Join(m.cacheDir, "modpacks", modpackFilename)
	if err := m.downloadFile(downloadInfo.URL, modpackPath); err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to download modpack: %v", err), 0, 100, "")
		return nil, fmt.Errorf("failed to download modpack: %w", err)
	}

	log.Printf("Downloaded modpack to: %s", modpackPath)
	m.reportProgress("extracting", "Extracting modpack files...", 15, 100, "")

	// Extract and install the modpack
	info, err := m.extractModpack(modpackPath, destDir)
	if err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to extract modpack: %v", err), 0, 100, "")
		return nil, fmt.Errorf("failed to extract modpack: %w", err)
	}

	return info, nil
}

// extractModpack extracts a CurseForge modpack zip and installs mods
func (m *ModpackInstaller) extractModpack(zipPath, destDir string) (*ModpackInfo, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open modpack zip: %w", err)
	}
	defer reader.Close()

	// Find and parse manifest.json
	var manifest *CurseForgeManifest
	for _, file := range reader.File {
		if file.Name == "manifest.json" {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open manifest: %w", err)
			}
			defer rc.Close()

			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				return nil, fmt.Errorf("failed to parse manifest: %w", err)
			}
			break
		}
	}

	if manifest == nil {
		return nil, fmt.Errorf("manifest.json not found in modpack")
	}

	log.Printf("Modpack: %s (version %s)", manifest.Name, manifest.Version)
	log.Printf("Minecraft version: %s", manifest.Minecraft.Version)

	// Parse loader info
	loaderType, loaderVersion := parseLoaderInfo(manifest)
	log.Printf("Loader: %s %s", loaderType, loaderVersion)

	// Determine overrides folder name
	overridesFolder := manifest.Overrides
	if overridesFolder == "" {
		overridesFolder = "overrides"
	}

	// Extract overrides folder (contains configs, scripts, etc.)
	for _, file := range reader.File {
		var relPath string
		if strings.HasPrefix(file.Name, overridesFolder+"/") {
			relPath = strings.TrimPrefix(file.Name, overridesFolder+"/")
		} else if strings.HasPrefix(file.Name, "server-overrides/") {
			relPath = strings.TrimPrefix(file.Name, "server-overrides/")
		} else {
			continue
		}

		if relPath == "" {
			continue
		}

		destPath := filepath.Join(destDir, relPath)

		if file.FileInfo().IsDir() {
			os.MkdirAll(destPath, 0755)
			continue
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Extract file
		if err := m.extractFile(file, destPath); err != nil {
			log.Printf("Warning: Failed to extract %s: %v", file.Name, err)
		}
	}

	// Download all mods from the manifest
	if err := m.downloadCurseForgeMods(manifest, destDir); err != nil {
		return nil, fmt.Errorf("failed to download mods: %w", err)
	}

	return &ModpackInfo{
		Name:          manifest.Name,
		Version:       manifest.Version,
		MinecraftVer:  manifest.Minecraft.Version,
		LoaderType:    loaderType,
		LoaderVersion: loaderVersion,
	}, nil
}

// parseLoaderInfo extracts loader type and version from manifest
func parseLoaderInfo(manifest *CurseForgeManifest) (loaderType, loaderVersion string) {
	for _, loader := range manifest.Minecraft.ModLoaders {
		if !loader.Primary {
			continue
		}

		// Parse loader ID like "neoforge-21.1.215" or "forge-47.2.0" or "fabric-0.15.0"
		parts := strings.SplitN(loader.ID, "-", 2)
		if len(parts) == 2 {
			loaderType = parts[0]
			loaderVersion = parts[1]
			return
		}
	}

	// Fallback to first loader if no primary
	if len(manifest.Minecraft.ModLoaders) > 0 {
		parts := strings.SplitN(manifest.Minecraft.ModLoaders[0].ID, "-", 2)
		if len(parts) == 2 {
			loaderType = parts[0]
			loaderVersion = parts[1]
		}
	}

	return
}

// extractFile extracts a single file from the ZIP
func (m *ModpackInstaller) extractFile(file *zip.File, destPath string) error {
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// downloadCurseForgeMods downloads all mods from the CurseForge manifest
func (m *ModpackInstaller) downloadCurseForgeMods(manifest *CurseForgeManifest, destDir string) error {
	totalMods := len(manifest.Files)
	log.Printf("Downloading %d mods...", totalMods)
	m.reportProgress("downloading_mods", fmt.Sprintf("Downloading %d mods...", totalMods), 0, totalMods, "")

	// Create mods directory
	modsDir := filepath.Join(destDir, "mods")
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		return fmt.Errorf("failed to create mods directory: %w", err)
	}

	// Download each mod
	for i, mod := range manifest.Files {
		log.Printf("[%d/%d] Fetching mod info: projectID=%d, fileID=%d", i+1, totalMods, mod.ProjectID, mod.FileID)
		m.reportProgress("downloading_mods", fmt.Sprintf("Downloading mod %d of %d", i+1, totalMods), i, totalMods, "")

		// Get file info from CurseForge API
		fileInfo, err := m.getCurseForgeFileInfo(mod.ProjectID, mod.FileID)
		if err != nil {
			log.Printf("Warning: Failed to get file info for project %d file %d: %v", mod.ProjectID, mod.FileID, err)
			continue
		}

		if fileInfo.DownloadURL == "" {
			log.Printf("Warning: No download URL for %s (project %d file %d) - mod may restrict third-party downloads", fileInfo.FileName, mod.ProjectID, mod.FileID)
			continue
		}

		destPath := filepath.Join(modsDir, fileInfo.FileName)
		log.Printf("[%d/%d] Downloading: %s", i+1, totalMods, fileInfo.FileName)
		m.reportProgress("downloading_mods", fmt.Sprintf("Downloading mod %d of %d", i+1, totalMods), i, totalMods, fileInfo.FileName)

		if err := m.downloadFile(fileInfo.DownloadURL, destPath); err != nil {
			log.Printf("Warning: Failed to download %s: %v", fileInfo.FileName, err)
		}
	}

	m.reportProgress("downloading_mods", fmt.Sprintf("Downloaded %d mods", totalMods), totalMods, totalMods, "")
	return nil
}

// CurseForgeFileInfo contains file download information
type CurseForgeFileInfo struct {
	FileName    string
	DownloadURL string
}

// getCurseForgeFileInfo fetches file information from CurseForge API
func (m *ModpackInstaller) getCurseForgeFileInfo(projectID, fileID int) (*CurseForgeFileInfo, error) {
	url := fmt.Sprintf("https://api.curseforge.com/v1/mods/%d/files/%d", projectID, fileID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", m.curseforgeAPIKey)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CurseForge API returned %d: %s", resp.StatusCode, string(body))
	}

	var fileResp CurseForgeFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return nil, fmt.Errorf("failed to parse file response: %w", err)
	}

	return &CurseForgeFileInfo{
		FileName:    fileResp.Data.FileName,
		DownloadURL: fileResp.Data.DownloadURL,
	}, nil
}

// downloadFile downloads a file from URL to destination
func (m *ModpackInstaller) downloadFile(url, destPath string) error {
	// Create destination directory
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0 (https://redstonecore.net)")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Create temp file
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, resp.Body)
	out.Close()

	if err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Rename to final path
	return os.Rename(tmpPath, destPath)
}

// GetMinecraftVersion returns the Minecraft version
func (info *ModpackInfo) GetMinecraftVersion() string {
	return info.MinecraftVer
}

// GetLoaderType returns the mod loader type (fabric, forge, neoforge)
func (info *ModpackInfo) GetLoaderType() string {
	return info.LoaderType
}

// GetLoaderVersion returns the loader version
func (info *ModpackInfo) GetLoaderVersion() string {
	return info.LoaderVersion
}

// InstallServerPack downloads and extracts a CurseForge server pack
// Server packs include everything needed to run the server (loader + mods + configs)
func (m *ModpackInstaller) InstallServerPack(url, filename, destDir string) error {
	log.Printf("Installing server pack from: %s", url)

	if filename == "" {
		filename = "serverpack.zip"
	}

	m.reportProgress("downloading_server_pack", "Downloading server pack...", 5, 100, filename)

	// Download the server pack
	packPath := filepath.Join(m.cacheDir, "serverpacks", filename)
	if err := m.downloadFile(url, packPath); err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to download server pack: %v", err), 0, 100, "")
		return fmt.Errorf("failed to download server pack: %w", err)
	}

	log.Printf("Downloaded server pack to: %s", packPath)
	m.reportProgress("extracting", "Extracting server pack...", 30, 100, "")

	// Extract the server pack
	reader, err := zip.OpenReader(packPath)
	if err != nil {
		return fmt.Errorf("failed to open server pack zip: %w", err)
	}
	defer reader.Close()

	totalFiles := len(reader.File)
	for i, file := range reader.File {
		// Skip directories
		if file.FileInfo().IsDir() {
			destPath := filepath.Join(destDir, file.Name)
			os.MkdirAll(destPath, 0755)
			continue
		}

		destPath := filepath.Join(destDir, file.Name)

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Printf("Warning: Failed to create directory for %s: %v", file.Name, err)
			continue
		}

		// Extract file
		if err := m.extractFile(file, destPath); err != nil {
			log.Printf("Warning: Failed to extract %s: %v", file.Name, err)
			continue
		}

		// Report progress every 10 files
		if i%10 == 0 {
			progress := 30 + int(float64(i)/float64(totalFiles)*60)
			m.reportProgress("extracting", fmt.Sprintf("Extracting files (%d/%d)", i+1, totalFiles), progress, 100, file.Name)
		}
	}

	// Make run scripts executable
	for _, script := range []string{"run.sh", "run.bat", "start.sh", "start.bat"} {
		scriptPath := filepath.Join(destDir, script)
		if _, err := os.Stat(scriptPath); err == nil {
			os.Chmod(scriptPath, 0755)
			log.Printf("Made %s executable", script)
		}
	}

	m.reportProgress("complete", "Server pack installed successfully!", 100, 100, "")
	log.Printf("Server pack extracted to: %s", destDir)

	return nil
}
