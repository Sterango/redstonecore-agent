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

// ModpackInstaller handles downloading and installing Modrinth modpacks
type ModpackInstaller struct {
	httpClient       *http.Client
	cacheDir         string
	progressCallback ProgressCallback
}

// ModrinthIndex represents the modrinth.index.json file format
type ModrinthIndex struct {
	FormatVersion int                    `json:"formatVersion"`
	Game          string                 `json:"game"`
	VersionID     string                 `json:"versionId"`
	Name          string                 `json:"name"`
	Files         []ModrinthFile         `json:"files"`
	Dependencies  map[string]string      `json:"dependencies"`
}

// ModrinthFile represents a file entry in the modpack index
type ModrinthFile struct {
	Path      string             `json:"path"`
	Hashes    map[string]string  `json:"hashes"`
	Env       *ModrinthEnv       `json:"env,omitempty"`
	Downloads []string           `json:"downloads"`
	FileSize  int64              `json:"fileSize"`
}

// ModrinthEnv specifies client/server requirements
type ModrinthEnv struct {
	Client string `json:"client"` // required, optional, unsupported
	Server string `json:"server"` // required, optional, unsupported
}

// ModrinthVersionResponse represents a version from Modrinth API
type ModrinthVersionResponse struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string `json:"loaders"`
	Files         []struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Primary  bool   `json:"primary"`
		Size     int64  `json:"size"`
	} `json:"files"`
}

// NewModpackInstaller creates a new modpack installer
func NewModpackInstaller(cacheDir string) *ModpackInstaller {
	return &ModpackInstaller{
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Modpacks can be large
		},
		cacheDir: cacheDir,
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

// InstallModpack downloads and installs a modpack from Modrinth
func (m *ModpackInstaller) InstallModpack(versionID string, destDir string) (*ModrinthIndex, error) {
	log.Printf("Installing modpack version: %s", versionID)

	m.reportProgress("preparing", "Fetching modpack information...", 0, 100, "")

	// Get version details from Modrinth API
	version, err := m.getModrinthVersion(versionID)
	if err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to get version details: %v", err), 0, 100, "")
		return nil, fmt.Errorf("failed to get version details: %w", err)
	}

	// Find the primary .mrpack file
	var mrpackURL string
	var mrpackFilename string
	for _, file := range version.Files {
		if strings.HasSuffix(file.Filename, ".mrpack") {
			mrpackURL = file.URL
			mrpackFilename = file.Filename
			break
		}
	}

	if mrpackURL == "" {
		m.reportProgress("error", "No .mrpack file found in modpack", 0, 100, "")
		return nil, fmt.Errorf("no .mrpack file found in version %s", versionID)
	}

	m.reportProgress("downloading_modpack", fmt.Sprintf("Downloading %s...", mrpackFilename), 5, 100, mrpackFilename)

	// Download the .mrpack file
	mrpackPath := filepath.Join(m.cacheDir, "modpacks", mrpackFilename)
	if err := m.downloadFile(mrpackURL, mrpackPath); err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to download modpack: %v", err), 0, 100, "")
		return nil, fmt.Errorf("failed to download modpack: %w", err)
	}

	log.Printf("Downloaded modpack to: %s", mrpackPath)
	m.reportProgress("extracting", "Extracting modpack files...", 15, 100, "")

	// Extract and install the modpack
	index, err := m.extractModpack(mrpackPath, destDir)
	if err != nil {
		m.reportProgress("error", fmt.Sprintf("Failed to extract modpack: %v", err), 0, 100, "")
		return nil, fmt.Errorf("failed to extract modpack: %w", err)
	}

	return index, nil
}

// getModrinthVersion fetches version details from Modrinth API
func (m *ModpackInstaller) getModrinthVersion(versionID string) (*ModrinthVersionResponse, error) {
	url := fmt.Sprintf("https://api.modrinth.com/v2/version/%s", versionID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0 (https://redstonecore.net)")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Modrinth API returned %d: %s", resp.StatusCode, string(body))
	}

	var version ModrinthVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return nil, fmt.Errorf("failed to parse version response: %w", err)
	}

	return &version, nil
}

// extractModpack extracts a .mrpack file and installs mods
func (m *ModpackInstaller) extractModpack(mrpackPath, destDir string) (*ModrinthIndex, error) {
	// Open the .mrpack file (it's a ZIP)
	reader, err := zip.OpenReader(mrpackPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open mrpack: %w", err)
	}
	defer reader.Close()

	// Find and parse modrinth.index.json
	var index *ModrinthIndex
	for _, file := range reader.File {
		if file.Name == "modrinth.index.json" {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open index: %w", err)
			}
			defer rc.Close()

			if err := json.NewDecoder(rc).Decode(&index); err != nil {
				return nil, fmt.Errorf("failed to parse index: %w", err)
			}
			break
		}
	}

	if index == nil {
		return nil, fmt.Errorf("modrinth.index.json not found in modpack")
	}

	log.Printf("Modpack: %s (version %s)", index.Name, index.VersionID)
	log.Printf("Dependencies: %v", index.Dependencies)

	// Extract overrides folder (contains configs, scripts, etc.)
	for _, file := range reader.File {
		// Handle both "overrides/" and "server-overrides/" prefixes
		var relPath string
		if strings.HasPrefix(file.Name, "overrides/") {
			relPath = strings.TrimPrefix(file.Name, "overrides/")
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

	// Download all mods from the index
	if err := m.downloadMods(index, destDir); err != nil {
		return nil, fmt.Errorf("failed to download mods: %w", err)
	}

	return index, nil
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

// downloadMods downloads all mods listed in the index
func (m *ModpackInstaller) downloadMods(index *ModrinthIndex, destDir string) error {
	// Filter to server-side mods only
	var serverMods []ModrinthFile
	for _, file := range index.Files {
		// Skip client-only mods
		if file.Env != nil && file.Env.Server == "unsupported" {
			log.Printf("Skipping client-only mod: %s", file.Path)
			continue
		}
		serverMods = append(serverMods, file)
	}

	totalMods := len(serverMods)
	log.Printf("Downloading %d mods...", totalMods)
	m.reportProgress("downloading_mods", fmt.Sprintf("Downloading %d mods...", totalMods), 0, totalMods, "")

	// Download each mod
	for i, mod := range serverMods {
		if len(mod.Downloads) == 0 {
			log.Printf("Warning: No download URL for %s", mod.Path)
			continue
		}

		modName := filepath.Base(mod.Path)
		destPath := filepath.Join(destDir, mod.Path)
		log.Printf("[%d/%d] Downloading: %s", i+1, totalMods, modName)
		m.reportProgress("downloading_mods", fmt.Sprintf("Downloading mod %d of %d", i+1, totalMods), i, totalMods, modName)

		// Try each download URL until one works
		var downloadErr error
		for _, url := range mod.Downloads {
			if err := m.downloadFile(url, destPath); err != nil {
				downloadErr = err
				continue
			}
			downloadErr = nil
			break
		}

		if downloadErr != nil {
			log.Printf("Warning: Failed to download %s: %v", mod.Path, downloadErr)
		}
	}

	m.reportProgress("downloading_mods", fmt.Sprintf("Downloaded %d mods", totalMods), totalMods, totalMods, "")
	return nil
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

// GetMinecraftVersion extracts the Minecraft version from dependencies
func (index *ModrinthIndex) GetMinecraftVersion() string {
	if v, ok := index.Dependencies["minecraft"]; ok {
		return v
	}
	return ""
}

// GetLoaderType returns the mod loader type (fabric, forge, neoforge, quilt)
func (index *ModrinthIndex) GetLoaderType() string {
	if _, ok := index.Dependencies["fabric-loader"]; ok {
		return "fabric"
	}
	if _, ok := index.Dependencies["forge"]; ok {
		return "forge"
	}
	if _, ok := index.Dependencies["neoforge"]; ok {
		return "neoforge"
	}
	if _, ok := index.Dependencies["quilt-loader"]; ok {
		return "quilt"
	}
	return ""
}

// GetLoaderVersion returns the loader version
func (index *ModrinthIndex) GetLoaderVersion() string {
	if v, ok := index.Dependencies["fabric-loader"]; ok {
		return v
	}
	if v, ok := index.Dependencies["forge"]; ok {
		return v
	}
	if v, ok := index.Dependencies["neoforge"]; ok {
		return v
	}
	if v, ok := index.Dependencies["quilt-loader"]; ok {
		return v
	}
	return ""
}
