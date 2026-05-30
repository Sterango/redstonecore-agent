package moddata

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Matches printf-style placeholders (%s, %d, %1$s, %2$d) and Minecraft § codes,
// which appear in names that the game fills in at runtime (e.g. tier prefixes).
var formatPlaceholderRe = regexp.MustCompile(`%(\d+\$)?[sd]|§.`)

func cleanName(s string) string {
	s = formatPlaceholderRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// Item is a single registry item with its display name and whether an icon was
// resolved at build time.
type Item struct {
	ID      string `json:"id"`      // e.g. "mekanism:enriched_iron"
	Name    string `json:"name"`    // display name from lang, or prettified id
	Mod     string `json:"mod"`     // namespace
	HasIcon bool   `json:"hasIcon"`
}

func splitID(id string) (ns, path string) {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "minecraft", id
}

func prettify(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "/", " ")
	parts := strings.Fields(s)
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// displayName resolves an item's name from the merged lang map, trying item.*
// then block.*, falling back to a prettified id.
func displayName(id string, lang map[string]string) string {
	ns, path := splitID(id)
	if v, ok := lang["item."+ns+"."+path]; ok {
		return cleanName(v)
	}
	if v, ok := lang["block."+ns+"."+path]; ok {
		return cleanName(v)
	}
	return prettify(path)
}

// iconFile is the on-disk filename for an item's cached icon.
func iconFile(id string) string {
	s := strings.ReplaceAll(id, ":", "__")
	s = strings.ReplaceAll(s, "/", "_")
	return s + ".png"
}

type modelJSON struct {
	Parent   string            `json:"parent"`
	Textures map[string]string `json:"textures"`
}

// resolveIcon returns the PNG bytes for an item, trying its model's texture
// references first, then a texture conventionally named after the item id.
func resolveIcon(model []byte, blockModels, textures map[string][]byte, ns, path string) []byte {
	if texID := resolveItemTextureID(model, blockModels, 2); texID != "" {
		if png, ok := textures[texID]; ok {
			return png
		}
	}
	for _, cand := range []string{ns + ":item/" + path, ns + ":block/" + path} {
		if png, ok := textures[cand]; ok {
			return png
		}
	}
	// Custom-loader models (e.g. Sophisticated Storage barrels) nest textures
	// under non-standard keys like model_parts.*.textures.*. Deep-scan the model
	// (and its block-model parents) for any string that's a real texture.
	if png := deepScanTexture(model, blockModels, textures, 3); png != nil {
		return png
	}
	return nil
}

func deepScanTexture(model []byte, blockModels, textures map[string][]byte, depth int) []byte {
	var m map[string]interface{}
	if json.Unmarshal(model, &m) != nil {
		return nil
	}
	if png := scanForTexture(m, textures); png != nil {
		return png
	}
	if depth > 0 {
		if parent, _ := m["parent"].(string); parent != "" {
			if bm, ok := blockModelFor(parent, blockModels); ok {
				return deepScanTexture(bm, blockModels, textures, depth-1)
			}
		}
	}
	return nil
}

// scanForTexture walks arbitrary JSON returning the bytes of the first string
// value that's a real texture key, preferring common face keys.
func scanForTexture(v interface{}, textures map[string][]byte) []byte {
	switch t := v.(type) {
	case string:
		if png, ok := textures[t]; ok {
			return png
		}
	case map[string]interface{}:
		for _, k := range []string{"layer0", "texture", "side", "front", "top", "0", "all", "particle"} {
			if s, ok := t[k].(string); ok {
				if png, ok2 := textures[s]; ok2 {
					return png
				}
			}
		}
		for _, vv := range t {
			if png := scanForTexture(vv, textures); png != nil {
				return png
			}
		}
	case []interface{}:
		for _, vv := range t {
			if png := scanForTexture(vv, textures); png != nil {
				return png
			}
		}
	}
	return nil
}

// resolveItemTextureID returns the texture id (e.g. "mekanism:item/enriched_iron")
// for an item model, following an item→block model parent up to `depth` levels.
func resolveItemTextureID(modelBytes []byte, blockModels map[string][]byte, depth int) string {
	var m modelJSON
	if err := json.Unmarshal(modelBytes, &m); err != nil {
		return ""
	}
	if t := m.Textures["layer0"]; t != "" && !strings.HasPrefix(t, "#") {
		return t
	}
	if t := anyTexture(m.Textures); t != "" {
		return t
	}
	if depth > 0 {
		if bm, ok := blockModelFor(m.Parent, blockModels); ok {
			return resolveBlockTextureID(bm, blockModels, depth-1)
		}
	}
	return ""
}

// resolveBlockTextureID picks a representative face texture from a block model.
func resolveBlockTextureID(modelBytes []byte, blockModels map[string][]byte, depth int) string {
	var m modelJSON
	if err := json.Unmarshal(modelBytes, &m); err != nil {
		return ""
	}
	for _, k := range []string{"particle", "all", "side", "top", "north", "texture", "0"} {
		if t := m.Textures[k]; t != "" && !strings.HasPrefix(t, "#") {
			return t
		}
	}
	if t := anyTexture(m.Textures); t != "" {
		return t
	}
	if depth > 0 {
		if bm, ok := blockModelFor(m.Parent, blockModels); ok {
			return resolveBlockTextureID(bm, blockModels, depth-1)
		}
	}
	return ""
}

func anyTexture(textures map[string]string) string {
	for _, t := range textures {
		if t != "" && !strings.HasPrefix(t, "#") {
			return t
		}
	}
	return ""
}

// blockModelFor resolves a "ns:block/<path>" parent to its stored block model.
func blockModelFor(parent string, blockModels map[string][]byte) ([]byte, bool) {
	if !strings.Contains(parent, "block/") {
		return nil, false
	}
	ns, path := splitID(parent)
	key := ns + ":" + strings.TrimPrefix(path, "block/")
	bm, ok := blockModels[key]
	return bm, ok
}
