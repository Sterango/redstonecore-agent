// Package moddata extracts human-facing information from a Minecraft server's
// installed mod JARs — language strings (labels, tooltips, GUI text) and, in a
// later phase, items and recipes — and serves it to the control panel.
//
// All caches are keyed by serverUUID (unlike maprender's process-global texture
// cache) because every instance has a different mod set. Caches are busted
// automatically when the mods directory changes, detected via a cheap signature
// of each jar's name/size/modtime (no jar is opened to compute it).
package moddata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// langCacheDir is the per-server directory holding extracted lang maps.
func langCacheDir(dataDir, serverUUID string) string {
	return filepath.Join(dataDir, "cache", "mod-lang", serverUUID)
}

// indexCacheDir is the per-server directory holding the item/recipe index.
func indexCacheDir(dataDir, serverUUID string) string {
	return filepath.Join(dataDir, "cache", "mod-index", serverUUID)
}

// modsSignature returns a cheap fingerprint of the mods directory based on each
// jar's name, size and modtime. It does NOT open the jars. Returns "" if the
// mods dir can't be read.
func modsSignature(serverDir string) string {
	modsDir := filepath.Join(serverDir, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return ""
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", e.Name(), info.Size(), info.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// safeKey makes a modId safe to use as a filename.
func safeKey(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" {
		s = "_all"
	}
	return s
}

func readSig(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "_sig"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeSig(dir, sig string) error {
	if err := os.MkdirAll(dir, 0775); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "_sig"), []byte(sig), 0644)
}

// ensureFresh busts the cache directory if the mods signature changed, and
// records the current signature. Returns the current signature ("" if unknown).
func ensureFresh(cacheDir, serverDir string) string {
	sig := modsSignature(serverDir)
	if sig == "" {
		return ""
	}
	if readSig(cacheDir) != sig {
		_ = os.RemoveAll(cacheDir)
		_ = writeSig(cacheDir, sig)
	}
	return sig
}

func cachedLang(dir, modID string) (map[string]string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, safeKey(modID)+".json"))
	if err != nil {
		return nil, false
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return m, true
}

func storeLang(dir, modID string, m map[string]string) error {
	if err := os.MkdirAll(dir, 0775); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, safeKey(modID)+".json"), data, 0644)
}
