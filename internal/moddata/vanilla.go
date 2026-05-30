package moddata

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// The official Mojang version manifest. The client jar it points to contains
// both vanilla item textures (assets/minecraft/...) and vanilla recipes/tags
// (data/minecraft/...), so scanning it gives us all vanilla content at once.
const versionManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

var mcVersionDirRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// detectMCVersion reads the Minecraft version from the server's libraries layout
// (libraries/net/minecraft/server/<version>/).
func detectMCVersion(serverDir string) string {
	base := filepath.Join(serverDir, "libraries", "net", "minecraft", "server")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && mcVersionDirRe.MatchString(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// ensureVanillaJar returns a cached vanilla client jar for the server's MC
// version, downloading it from Mojang on first use. Returns "" (and the index
// simply omits vanilla content) if the version is unknown or the download fails.
func ensureVanillaJar(serverDir, dataDir string) string {
	if os.Getenv("RSC_SKIP_VANILLA") == "1" {
		return ""
	}
	version := detectMCVersion(serverDir)
	if version == "" {
		return ""
	}
	jarPath := filepath.Join(dataDir, "cache", "vanilla", version, "client.jar")
	if fi, err := os.Stat(jarPath); err == nil && fi.Size() > 1_000_000 {
		return jarPath
	}
	if err := downloadVanillaClient(version, jarPath); err != nil {
		log.Printf("[ModData] vanilla client %s unavailable: %v", version, err)
		return ""
	}
	log.Printf("[ModData] downloaded vanilla client %s", version)
	return jarPath
}

func httpGetBytes(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func downloadVanillaClient(version, dest string) error {
	manifest, err := httpGetBytes(versionManifestURL, 30*time.Second)
	if err != nil {
		return err
	}
	var m struct {
		Versions []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return err
	}
	var pkgURL string
	for _, v := range m.Versions {
		if v.ID == version {
			pkgURL = v.URL
			break
		}
	}
	if pkgURL == "" {
		return fmt.Errorf("version %s not in manifest", version)
	}

	pkg, err := httpGetBytes(pkgURL, 30*time.Second)
	if err != nil {
		return err
	}
	var p struct {
		Downloads struct {
			Client struct {
				URL  string `json:"url"`
				SHA1 string `json:"sha1"`
			} `json:"client"`
		} `json:"downloads"`
	}
	if err := json.Unmarshal(pkg, &p); err != nil {
		return err
	}
	if p.Downloads.Client.URL == "" {
		return fmt.Errorf("no client download for %s", version)
	}

	data, err := httpGetBytes(p.Downloads.Client.URL, 180*time.Second)
	if err != nil {
		return err
	}
	if p.Downloads.Client.SHA1 != "" {
		sum := sha1.Sum(data)
		if hex.EncodeToString(sum[:]) != p.Downloads.Client.SHA1 {
			return fmt.Errorf("client jar sha1 mismatch")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0775); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0644)
}
