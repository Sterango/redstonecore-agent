package minecraft

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Downloader struct {
	httpClient *http.Client
	cacheDir   string
}

func NewDownloader(cacheDir string) *Downloader {
	return &Downloader{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		cacheDir: cacheDir,
	}
}

// DownloadServer downloads the appropriate server JAR based on type and version
func (d *Downloader) DownloadServer(serverType ServerType, version string, destDir string) (string, error) {
	switch serverType {
	case TypePaper:
		return d.downloadPaper(version, destDir)
	case TypeVanilla:
		return d.downloadVanilla(version, destDir)
	case TypeFabric:
		return d.downloadFabric(version, "", destDir)
	case TypeForge:
		return d.downloadForge(version, "", destDir)
	case TypeNeoForge:
		return d.downloadNeoForge(version, "", destDir)
	case TypeVelocity:
		return d.downloadVelocity(destDir)
	case TypeBungeeCord:
		return d.downloadWaterfall(version, destDir)
	case TypeSpigot:
		return d.downloadSpigot(version, destDir)
	default:
		return "", fmt.Errorf("automatic download not supported for server type: %s", serverType)
	}
}

// DownloadServerWithLoader downloads a server with a specific loader version
func (d *Downloader) DownloadServerWithLoader(serverType ServerType, mcVersion, loaderVersion string, destDir string) (string, error) {
	switch serverType {
	case TypeFabric:
		return d.downloadFabric(mcVersion, loaderVersion, destDir)
	case TypeForge:
		return d.downloadForge(mcVersion, loaderVersion, destDir)
	case TypeNeoForge:
		return d.downloadNeoForge(mcVersion, loaderVersion, destDir)
	default:
		return d.DownloadServer(serverType, mcVersion, destDir)
	}
}

// downloadPaper downloads Paper server from PaperMC API
func (d *Downloader) downloadPaper(version string, destDir string) (string, error) {
	// Get the latest build for this version
	buildsURL := fmt.Sprintf("https://api.papermc.io/v2/projects/paper/versions/%s/builds", version)

	resp, err := d.httpClient.Get(buildsURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Paper builds: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Paper API returned status %d for version %s", resp.StatusCode, version)
	}

	var buildsResp struct {
		Builds []struct {
			Build     int    `json:"build"`
			Downloads struct {
				Application struct {
					Name string `json:"name"`
					SHA256 string `json:"sha256"`
				} `json:"application"`
			} `json:"downloads"`
		} `json:"builds"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&buildsResp); err != nil {
		return "", fmt.Errorf("failed to parse Paper builds response: %w", err)
	}

	if len(buildsResp.Builds) == 0 {
		return "", fmt.Errorf("no builds found for Paper %s", version)
	}

	// Get the latest build
	latestBuild := buildsResp.Builds[len(buildsResp.Builds)-1]
	jarName := latestBuild.Downloads.Application.Name

	downloadURL := fmt.Sprintf("https://api.papermc.io/v2/projects/paper/versions/%s/builds/%d/downloads/%s",
		version, latestBuild.Build, jarName)

	destPath := filepath.Join(destDir, "server.jar")

	if err := d.downloadFile(downloadURL, destPath); err != nil {
		return "", fmt.Errorf("failed to download Paper JAR: %w", err)
	}

	return destPath, nil
}

// downloadVanilla downloads Vanilla server from Mojang
func (d *Downloader) downloadVanilla(version string, destDir string) (string, error) {
	// Get version manifest
	manifestURL := "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

	resp, err := d.httpClient.Get(manifestURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch version manifest: %w", err)
	}
	defer resp.Body.Close()

	var manifest struct {
		Versions []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"versions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", fmt.Errorf("failed to parse version manifest: %w", err)
	}

	// Find the version URL
	var versionURL string
	for _, v := range manifest.Versions {
		if v.ID == version {
			versionURL = v.URL
			break
		}
	}

	if versionURL == "" {
		return "", fmt.Errorf("version %s not found in manifest", version)
	}

	// Get version details
	resp, err = d.httpClient.Get(versionURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch version details: %w", err)
	}
	defer resp.Body.Close()

	var versionDetails struct {
		Downloads struct {
			Server struct {
				URL    string `json:"url"`
				SHA1   string `json:"sha1"`
			} `json:"server"`
		} `json:"downloads"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&versionDetails); err != nil {
		return "", fmt.Errorf("failed to parse version details: %w", err)
	}

	if versionDetails.Downloads.Server.URL == "" {
		return "", fmt.Errorf("no server download URL for version %s", version)
	}

	destPath := filepath.Join(destDir, "server.jar")

	if err := d.downloadFile(versionDetails.Downloads.Server.URL, destPath); err != nil {
		return "", fmt.Errorf("failed to download Vanilla JAR: %w", err)
	}

	return destPath, nil
}

// downloadFile downloads a file from URL to destination path
func (d *Downloader) downloadFile(url, destPath string) error {
	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temporary file
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	// Download
	resp, err := d.httpClient.Get(url)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		os.Remove(tmpPath)
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Copy data
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Close and rename
	out.Close()
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// GetAvailablePaperVersions returns available Paper versions
func (d *Downloader) GetAvailablePaperVersions() ([]string, error) {
	resp, err := d.httpClient.Get("https://api.papermc.io/v2/projects/paper")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var project struct {
		Versions []string `json:"versions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, err
	}

	return project.Versions, nil
}

// downloadFabric downloads Fabric server launcher
func (d *Downloader) downloadFabric(mcVersion, loaderVersion string, destDir string) (string, error) {
	// If no loader version specified, get the latest stable
	if loaderVersion == "" {
		versions, err := d.getFabricLoaderVersions()
		if err != nil {
			return "", fmt.Errorf("failed to get Fabric loader versions: %w", err)
		}
		if len(versions) == 0 {
			return "", fmt.Errorf("no Fabric loader versions available")
		}
		loaderVersion = versions[0] // Latest stable
	}

	// Get the latest installer version
	installerVersion, err := d.getLatestFabricInstallerVersion()
	if err != nil {
		return "", fmt.Errorf("failed to get Fabric installer version: %w", err)
	}

	// Build download URL for server launcher JAR
	downloadURL := fmt.Sprintf(
		"https://meta.fabricmc.net/v2/versions/loader/%s/%s/%s/server/jar",
		mcVersion, loaderVersion, installerVersion,
	)

	destPath := filepath.Join(destDir, "server.jar")

	if err := d.downloadFile(downloadURL, destPath); err != nil {
		return "", fmt.Errorf("failed to download Fabric server: %w", err)
	}

	return destPath, nil
}

// getFabricLoaderVersions returns available Fabric loader versions
func (d *Downloader) getFabricLoaderVersions() ([]string, error) {
	resp, err := d.httpClient.Get("https://meta.fabricmc.net/v2/versions/loader")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var versions []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}

	// Return stable versions first
	var result []string
	for _, v := range versions {
		if v.Stable {
			result = append(result, v.Version)
		}
	}
	for _, v := range versions {
		if !v.Stable {
			result = append(result, v.Version)
		}
	}

	return result, nil
}

// getLatestFabricInstallerVersion returns the latest Fabric installer version
func (d *Downloader) getLatestFabricInstallerVersion() (string, error) {
	resp, err := d.httpClient.Get("https://meta.fabricmc.net/v2/versions/installer")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var versions []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", err
	}

	// Return first stable version
	for _, v := range versions {
		if v.Stable {
			return v.Version, nil
		}
	}

	if len(versions) > 0 {
		return versions[0].Version, nil
	}

	return "", fmt.Errorf("no installer versions available")
}

// downloadForge downloads Forge server installer and runs it
func (d *Downloader) downloadForge(mcVersion, forgeVersion string, destDir string) (string, error) {
	// If no forge version specified, get latest for this MC version
	if forgeVersion == "" {
		versions, err := d.getForgeVersions(mcVersion)
		if err != nil {
			return "", fmt.Errorf("failed to get Forge versions: %w", err)
		}
		if len(versions) == 0 {
			return "", fmt.Errorf("no Forge versions available for Minecraft %s", mcVersion)
		}
		forgeVersion = versions[0] // Latest
	}

	// Forge installer download URL
	// Format: https://maven.minecraftforge.net/net/minecraftforge/forge/{mcVersion}-{forgeVersion}/forge-{mcVersion}-{forgeVersion}-installer.jar
	installerURL := fmt.Sprintf(
		"https://maven.minecraftforge.net/net/minecraftforge/forge/%s-%s/forge-%s-%s-installer.jar",
		mcVersion, forgeVersion, mcVersion, forgeVersion,
	)

	installerPath := filepath.Join(destDir, "forge-installer.jar")

	if err := d.downloadFile(installerURL, installerPath); err != nil {
		return "", fmt.Errorf("failed to download Forge installer: %w", err)
	}

	// Run the installer in server mode
	if err := d.runForgeInstaller(installerPath, destDir); err != nil {
		return "", fmt.Errorf("failed to run Forge installer: %w", err)
	}

	// The installer creates run scripts - find the server JAR or scripts
	// Forge creates different files depending on version
	patterns := []string{
		"forge-*-universal.jar",
		"forge-*-server.jar",
		"minecraft_server.*.jar",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(destDir, pattern))
		if len(matches) > 0 {
			// Rename to server.jar for consistency
			serverJar := filepath.Join(destDir, "server.jar")
			os.Rename(matches[0], serverJar)
			return serverJar, nil
		}
	}

	// For newer Forge versions, we might need to use run.sh/run.bat
	// Check if there's a libraries folder (indicates installer worked)
	if _, err := os.Stat(filepath.Join(destDir, "libraries")); err == nil {
		// Installer worked, but we need to set up for server launch
		return d.setupForgeServer(mcVersion, forgeVersion, destDir)
	}

	return "", fmt.Errorf("Forge installation completed but server JAR not found")
}

// getForgeVersions returns available Forge versions for a Minecraft version
func (d *Downloader) getForgeVersions(mcVersion string) ([]string, error) {
	// Forge promotions/versions endpoint
	resp, err := d.httpClient.Get("https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var promos struct {
		Promos map[string]string `json:"promos"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&promos); err != nil {
		return nil, err
	}

	var versions []string

	// Look for recommended and latest for this MC version
	if v, ok := promos.Promos[mcVersion+"-recommended"]; ok {
		versions = append(versions, v)
	}
	if v, ok := promos.Promos[mcVersion+"-latest"]; ok {
		// Avoid duplicates
		if len(versions) == 0 || versions[0] != v {
			versions = append(versions, v)
		}
	}

	return versions, nil
}

// runForgeInstaller runs the Forge installer in server mode
func (d *Downloader) runForgeInstaller(installerPath, destDir string) error {
	// Run: java -jar forge-installer.jar --installServer
	cmd := exec.Command("java", "-jar", installerPath, "--installServer")
	cmd.Dir = destDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installer failed: %w\nOutput: %s", err, string(output))
	}

	// Clean up installer
	os.Remove(installerPath)

	return nil
}

// setupForgeServer creates run scripts for newer Forge versions
func (d *Downloader) setupForgeServer(mcVersion, forgeVersion, destDir string) (string, error) {
	// For Forge 1.17+, we need to use the @libraries/net/minecraftforge/... approach
	// or run.sh/run.bat that the installer creates

	// Check for run.sh
	runScript := filepath.Join(destDir, "run.sh")
	if _, err := os.Stat(runScript); err == nil {
		// Make executable
		os.Chmod(runScript, 0755)
		return runScript, nil
	}

	// For older versions, look for forge JAR
	forgeJarPattern := fmt.Sprintf("forge-%s-%s*.jar", mcVersion, forgeVersion)
	matches, _ := filepath.Glob(filepath.Join(destDir, forgeJarPattern))
	if len(matches) > 0 {
		// Skip installer jar
		for _, m := range matches {
			isInstaller, _ := filepath.Match("*installer*", filepath.Base(m))
			if !isInstaller {
				serverJar := filepath.Join(destDir, "server.jar")
				os.Rename(m, serverJar)
				return serverJar, nil
			}
		}
	}

	// Create a wrapper script that runs Forge properly
	wrapperScript := filepath.Join(destDir, "run.sh")
	script := fmt.Sprintf(`#!/bin/bash
cd "$(dirname "$0")"
java @user_jvm_args.txt @libraries/net/minecraftforge/forge/%s-%s/unix_args.txt "$@"
`, mcVersion, forgeVersion)

	if err := os.WriteFile(wrapperScript, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("failed to create run script: %w", err)
	}

	return wrapperScript, nil
}

// downloadNeoForge downloads NeoForge server installer and runs it
func (d *Downloader) downloadNeoForge(mcVersion, neoforgeVersion string, destDir string) (string, error) {
	// If no neoforge version specified, get latest for this MC version
	if neoforgeVersion == "" {
		versions, err := d.getNeoForgeVersions(mcVersion)
		if err != nil {
			return "", fmt.Errorf("failed to get NeoForge versions: %w", err)
		}
		if len(versions) == 0 {
			return "", fmt.Errorf("no NeoForge versions available for Minecraft %s", mcVersion)
		}
		neoforgeVersion = versions[0] // Latest
	}

	// NeoForge installer download URL
	// Format: https://maven.neoforged.net/releases/net/neoforged/neoforge/{version}/neoforge-{version}-installer.jar
	installerURL := fmt.Sprintf(
		"https://maven.neoforged.net/releases/net/neoforged/neoforge/%s/neoforge-%s-installer.jar",
		neoforgeVersion, neoforgeVersion,
	)

	installerPath := filepath.Join(destDir, "neoforge-installer.jar")

	if err := d.downloadFile(installerURL, installerPath); err != nil {
		return "", fmt.Errorf("failed to download NeoForge installer: %w", err)
	}

	// Run the installer in server mode
	if err := d.runNeoForgeInstaller(installerPath, destDir); err != nil {
		return "", fmt.Errorf("failed to run NeoForge installer: %w", err)
	}

	// NeoForge creates run.sh - find and set up the server
	return d.setupNeoForgeServer(neoforgeVersion, destDir)
}

// getNeoForgeVersions returns available NeoForge versions for a Minecraft version
func (d *Downloader) getNeoForgeVersions(mcVersion string) ([]string, error) {
	// NeoForge API endpoint to get versions
	// NeoForge versions are formatted as: mcVersion.x.x (e.g., 21.1.77 for MC 1.21.1)
	url := "https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge"

	resp, err := d.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var response struct {
		Versions []string `json:"versions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}

	// Filter versions for the given Minecraft version
	// NeoForge versions follow pattern: majorMC.loaderVersion (e.g., 21.1.77 for MC 1.21.1)
	// We need to extract MC major version from mcVersion (e.g., 1.21.1 -> 21.1)
	var mcMajor string
	parts := strings.Split(mcVersion, ".")
	if len(parts) >= 2 {
		// Remove the "1." prefix and use the rest
		if parts[0] == "1" && len(parts) >= 2 {
			mcMajor = parts[1]
			if len(parts) >= 3 {
				mcMajor += "." + parts[2]
			}
		}
	}

	var matchingVersions []string
	for i := len(response.Versions) - 1; i >= 0; i-- {
		v := response.Versions[i]
		// Check if version starts with the MC major version
		if strings.HasPrefix(v, mcMajor+".") {
			matchingVersions = append(matchingVersions, v)
		}
	}

	return matchingVersions, nil
}

// runNeoForgeInstaller runs the NeoForge installer in server mode
func (d *Downloader) runNeoForgeInstaller(installerPath, destDir string) error {
	// Run: java -jar neoforge-installer.jar --installServer
	cmd := exec.Command("java", "-jar", installerPath, "--installServer")
	cmd.Dir = destDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installer failed: %w\nOutput: %s", err, string(output))
	}

	// Clean up installer
	os.Remove(installerPath)
	// Also remove the installer log
	os.Remove(filepath.Join(destDir, "installer.log"))

	return nil
}

// setupNeoForgeServer sets up NeoForge server after installation
func (d *Downloader) setupNeoForgeServer(neoforgeVersion, destDir string) (string, error) {
	// NeoForge creates a run.sh file
	runScript := filepath.Join(destDir, "run.sh")
	if _, err := os.Stat(runScript); err == nil {
		// Make executable
		os.Chmod(runScript, 0755)
		return runScript, nil
	}

	// Check for neoforge JAR
	neoforgeJarPattern := fmt.Sprintf("neoforge-%s*.jar", neoforgeVersion)
	matches, _ := filepath.Glob(filepath.Join(destDir, neoforgeJarPattern))
	if len(matches) > 0 {
		for _, m := range matches {
			// Skip installer jar
			if strings.Contains(filepath.Base(m), "installer") {
				continue
			}
			serverJar := filepath.Join(destDir, "server.jar")
			os.Rename(m, serverJar)
			return serverJar, nil
		}
	}

	// Create a wrapper script for NeoForge
	wrapperScript := filepath.Join(destDir, "run.sh")
	script := fmt.Sprintf(`#!/bin/bash
cd "$(dirname "$0")"
java @user_jvm_args.txt @libraries/net/neoforged/neoforge/%s/unix_args.txt "$@"
`, neoforgeVersion)

	if err := os.WriteFile(wrapperScript, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("failed to create run script: %w", err)
	}

	// Create default user_jvm_args.txt if it doesn't exist
	jvmArgsPath := filepath.Join(destDir, "user_jvm_args.txt")
	if _, err := os.Stat(jvmArgsPath); os.IsNotExist(err) {
		defaultArgs := "# Xmx and Xms set the maximum and minimum RAM usage, respectively.\n# They can take any number, followed by an M or a G.\n# M means Megabyte, G means Gigabyte.\n# For example, to set the maximum to 3GB: -Xmx3G\n# To set the minimum to 2.5GB: -Xms2500M\n-Xmx4G\n"
		os.WriteFile(jvmArgsPath, []byte(defaultArgs), 0644)
	}

	return wrapperScript, nil
}

// downloadVelocity downloads Velocity proxy from PaperMC API
func (d *Downloader) downloadVelocity(destDir string) (string, error) {
	// Get available versions
	versionsURL := "https://api.papermc.io/v2/projects/velocity"

	resp, err := d.httpClient.Get(versionsURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Velocity versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Velocity API returned status %d", resp.StatusCode)
	}

	var projectResp struct {
		Versions []string `json:"versions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&projectResp); err != nil {
		return "", fmt.Errorf("failed to parse Velocity versions: %w", err)
	}

	if len(projectResp.Versions) == 0 {
		return "", fmt.Errorf("no Velocity versions available")
	}

	// Get the latest stable version (last in list, skip SNAPSHOTs)
	var version string
	for i := len(projectResp.Versions) - 1; i >= 0; i-- {
		v := projectResp.Versions[i]
		if !strings.Contains(v, "SNAPSHOT") {
			version = v
			break
		}
	}
	if version == "" {
		version = projectResp.Versions[len(projectResp.Versions)-1]
	}

	// Get the latest build for this version
	buildsURL := fmt.Sprintf("https://api.papermc.io/v2/projects/velocity/versions/%s/builds", version)

	resp, err = d.httpClient.Get(buildsURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Velocity builds: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Velocity API returned status %d for version %s", resp.StatusCode, version)
	}

	var buildsResp struct {
		Builds []struct {
			Build     int `json:"build"`
			Downloads struct {
				Application struct {
					Name   string `json:"name"`
					SHA256 string `json:"sha256"`
				} `json:"application"`
			} `json:"downloads"`
		} `json:"builds"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&buildsResp); err != nil {
		return "", fmt.Errorf("failed to parse Velocity builds: %w", err)
	}

	if len(buildsResp.Builds) == 0 {
		return "", fmt.Errorf("no builds found for Velocity %s", version)
	}

	// Get the latest build
	latestBuild := buildsResp.Builds[len(buildsResp.Builds)-1]
	jarName := latestBuild.Downloads.Application.Name

	downloadURL := fmt.Sprintf("https://api.papermc.io/v2/projects/velocity/versions/%s/builds/%d/downloads/%s",
		version, latestBuild.Build, jarName)

	destPath := filepath.Join(destDir, "server.jar")

	if err := d.downloadFile(downloadURL, destPath); err != nil {
		return "", fmt.Errorf("failed to download Velocity JAR: %w", err)
	}

	return destPath, nil
}

// downloadWaterfall downloads Waterfall (BungeeCord fork) from PaperMC API
func (d *Downloader) downloadWaterfall(mcVersion string, destDir string) (string, error) {
	// Get available versions
	versionsURL := "https://api.papermc.io/v2/projects/waterfall"

	resp, err := d.httpClient.Get(versionsURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Waterfall versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Waterfall API returned status %d", resp.StatusCode)
	}

	var projectResp struct {
		Versions []string `json:"versions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&projectResp); err != nil {
		return "", fmt.Errorf("failed to parse Waterfall versions: %w", err)
	}

	if len(projectResp.Versions) == 0 {
		return "", fmt.Errorf("no Waterfall versions available")
	}

	// Find matching version or use latest
	version := ""
	for _, v := range projectResp.Versions {
		if v == mcVersion {
			version = v
			break
		}
	}
	if version == "" {
		// Use latest version
		version = projectResp.Versions[len(projectResp.Versions)-1]
	}

	// Get the latest build for this version
	buildsURL := fmt.Sprintf("https://api.papermc.io/v2/projects/waterfall/versions/%s/builds", version)

	resp, err = d.httpClient.Get(buildsURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Waterfall builds: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Waterfall API returned status %d for version %s", resp.StatusCode, version)
	}

	var buildsResp struct {
		Builds []struct {
			Build     int `json:"build"`
			Downloads struct {
				Application struct {
					Name   string `json:"name"`
					SHA256 string `json:"sha256"`
				} `json:"application"`
			} `json:"downloads"`
		} `json:"builds"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&buildsResp); err != nil {
		return "", fmt.Errorf("failed to parse Waterfall builds: %w", err)
	}

	if len(buildsResp.Builds) == 0 {
		return "", fmt.Errorf("no builds found for Waterfall %s", version)
	}

	// Get the latest build
	latestBuild := buildsResp.Builds[len(buildsResp.Builds)-1]
	jarName := latestBuild.Downloads.Application.Name

	downloadURL := fmt.Sprintf("https://api.papermc.io/v2/projects/waterfall/versions/%s/builds/%d/downloads/%s",
		version, latestBuild.Build, jarName)

	destPath := filepath.Join(destDir, "server.jar")

	if err := d.downloadFile(downloadURL, destPath); err != nil {
		return "", fmt.Errorf("failed to download Waterfall JAR: %w", err)
	}

	return destPath, nil
}

// downloadSpigot builds Spigot using BuildTools
func (d *Downloader) downloadSpigot(version string, destDir string) (string, error) {
	// Create a build directory in cache
	buildDir := filepath.Join(d.cacheDir, "spigot-build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create build directory: %w", err)
	}

	// Check if we already have a cached build for this version
	cachedJar := filepath.Join(d.cacheDir, fmt.Sprintf("spigot-%s.jar", version))
	if _, err := os.Stat(cachedJar); err == nil {
		// Use cached build
		destPath := filepath.Join(destDir, "server.jar")
		if err := d.copyFile(cachedJar, destPath); err != nil {
			return "", fmt.Errorf("failed to copy cached Spigot JAR: %w", err)
		}
		return destPath, nil
	}

	// Download BuildTools.jar
	buildToolsURL := "https://hub.spigotmc.org/jenkins/job/BuildTools/lastSuccessfulBuild/artifact/target/BuildTools.jar"
	buildToolsPath := filepath.Join(buildDir, "BuildTools.jar")

	if err := d.downloadFile(buildToolsURL, buildToolsPath); err != nil {
		return "", fmt.Errorf("failed to download BuildTools: %w", err)
	}

	// Run BuildTools
	// java -jar BuildTools.jar --rev <version>
	cmd := exec.Command("java", "-jar", "BuildTools.jar", "--rev", version)
	cmd.Dir = buildDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("BuildTools failed: %w\nOutput: %s", err, string(output))
	}

	// Find the built spigot jar
	spigotJarPattern := fmt.Sprintf("spigot-%s*.jar", version)
	matches, _ := filepath.Glob(filepath.Join(buildDir, spigotJarPattern))
	if len(matches) == 0 {
		// Try alternate pattern
		matches, _ = filepath.Glob(filepath.Join(buildDir, "spigot-*.jar"))
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("BuildTools completed but spigot JAR not found")
	}

	builtJar := matches[0]

	// Cache the built jar for future use
	if err := d.copyFile(builtJar, cachedJar); err != nil {
		// Non-fatal, just log
		fmt.Printf("Warning: Failed to cache Spigot JAR: %v\n", err)
	}

	// Copy to destination
	destPath := filepath.Join(destDir, "server.jar")
	if err := d.copyFile(builtJar, destPath); err != nil {
		return "", fmt.Errorf("failed to copy Spigot JAR: %w", err)
	}

	// Clean up build directory (keep cache)
	os.RemoveAll(buildDir)

	return destPath, nil
}

// copyFile copies a file from src to dst
func (d *Downloader) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
