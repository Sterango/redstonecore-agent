package moddata

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sterango/redstonecore-agent/internal/maprender"
)

// dropPrefixes are lang key categories that add noise to the config helper and
// glossary without helping a user understand a mod's UI.
var dropPrefixes = []string{
	"_",            // metadata, e.g. "_mekanism_force_utf8"
	"advancement",  // covers "advancement." and "advancements."
	"death.",
	"subtitles.",
	"commands.",
	"gamerule.",
	"selectWorld.",
	"options.",
	"narrator.",
	"chat.",
}

// loadLangForMod scans the server's jars for lang files. When modID is set it
// reads only assets/<modID>/lang/en_us.json (decompressing just that entry per
// jar); when modID is empty it merges every assets/*/lang/en_us.json (used by
// the Phase 2 index). Lang keys are namespaced, so merging never collides.
func loadLangForMod(serverDir, modID string) (map[string]string, error) {
	jars := maprender.FindJars(serverDir)
	result := make(map[string]string)

	var want string
	if modID != "" {
		want = fmt.Sprintf("assets/%s/lang/en_us.json", modID)
	}

	for _, jarPath := range jars {
		zr, err := zip.OpenReader(jarPath)
		if err != nil {
			continue
		}
		for _, f := range zr.File {
			if modID != "" {
				if f.Name != want {
					continue
				}
			} else if !strings.HasPrefix(f.Name, "assets/") || !strings.HasSuffix(f.Name, "/lang/en_us.json") {
				continue
			}
			mergeLangEntry(f, result)
		}
		zr.Close()
	}

	return result, nil
}

// mergeLangEntry decodes one en_us.json zip entry into result. Decoding is
// tolerant: non-string values (rare) are skipped rather than failing the file.
func mergeLangEntry(f *zip.File, result map[string]string) {
	rc, err := f.Open()
	if err != nil {
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
}

// filterLang strips noisy key categories (see dropPrefixes).
func filterLang(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		drop := false
		for _, p := range dropPrefixes {
			if strings.HasPrefix(k, p) {
				drop = true
				break
			}
		}
		if !drop {
			out[k] = v
		}
	}
	return out
}
