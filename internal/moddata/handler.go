package moddata

import (
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// HandleModLang returns a mod's filtered language map (labels, tooltips, GUI
// strings). params: {"modId": "<id>"} — when modId is empty/absent the merged
// map of all mods is returned. Results are cached per-server on disk and busted
// automatically when the mods directory changes.
//
// Signature matches the maprender.Handle* convention so the SFTP dispatcher can
// wrap it identically.
func HandleModLang(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	modID, _ := params["modId"].(string)

	cacheDir := langCacheDir(dataDir, serverUUID)
	ensureFresh(cacheDir, serverDir)

	if m, ok := cachedLang(cacheDir, modID); ok {
		return map[string]interface{}{"modId": modID, "lang": m, "cached": true}
	}

	m, err := loadLangForMod(serverDir, modID)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	m = filterLang(m)

	if err := storeLang(cacheDir, modID, m); err != nil {
		log.Printf("[ModData] Failed to cache lang for %q: %v", modID, err)
	}

	return map[string]interface{}{"modId": modID, "lang": m, "cached": false}
}

// HandleModInvalidate clears extracted mod-data caches for a server, forcing a
// rebuild on the next request. Exposed for an explicit "Rebuild" admin action.
func HandleModInvalidate(serverUUID, dataDir string) map[string]interface{} {
	indexMem.Delete(serverUUID)
	_ = os.RemoveAll(langCacheDir(dataDir, serverUUID))
	if err := os.RemoveAll(indexCacheDir(dataDir, serverUUID)); err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	return map[string]interface{}{"invalidated": true}
}

// HandleModIndexBuild builds (or returns the cached) item/recipe index.
// params: {"force": bool}. Returns counts + build timestamp.
func HandleModIndexBuild(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	force, _ := params["force"].(bool)

	var ci *cachedIndex
	var err error
	if force {
		ci, err = rebuild(serverDir, serverUUID, dataDir)
	} else {
		ci, err = getIndex(serverDir, serverUUID, dataDir)
	}
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	// Per-mod item counts, for the item grid's mod tabs (sorted most items first).
	counts := map[string]int{}
	for _, it := range ci.idx.Items {
		counts[it.Mod]++
	}
	type modCount struct {
		Mod   string `json:"mod"`
		Count int    `json:"count"`
	}
	mods := make([]modCount, 0, len(counts))
	for m, c := range counts {
		mods = append(mods, modCount{m, c})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Count > mods[j].Count })

	return map[string]interface{}{
		"itemCount":   len(ci.idx.Items),
		"recipeCount": len(ci.idx.Recipes),
		"builtAt":     ci.idx.BuiltAt,
		"mods":        mods,
	}
}

// HandleModItemsSearch returns a page of matching items.
// params: {"q","mod","limit","offset"}.
func HandleModItemsSearch(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	ci, err := getIndex(serverDir, serverUUID, dataDir)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	q, _ := params["q"].(string)
	mod, _ := params["mod"].(string)
	limit := intParam(params, "limit", 50)
	offset := intParam(params, "offset", 0)

	items, total := ci.search(q, mod, limit, offset)
	return map[string]interface{}{"items": items, "total": total}
}

// HandleModItemRecipes returns recipes that make/use an item, plus name+icon
// lookups for every referenced item id.
func HandleModItemRecipes(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	ci, err := getIndex(serverDir, serverUUID, dataDir)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	id, _ := params["id"].(string)
	if id == "" {
		return map[string]interface{}{"error": "id is required"}
	}
	makes, uses := ci.recipesFor(id)
	names, icons := ci.referencedMeta(makes, uses)
	tags := ci.recipeTags(makes, uses)
	return map[string]interface{}{
		"makes": makes,
		"uses":  uses,
		"names": names,
		"icons": icons,
		"tags":  tags,
	}
}

// HandleModIcon returns a single item's cached icon as base64 PNG. Relies on the
// index already being built; returns an error otherwise (caller serves a 404).
func HandleModIcon(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	id, _ := params["id"].(string)
	if id == "" {
		return map[string]interface{}{"error": "id is required"}
	}
	path := filepath.Join(iconsDir(indexCacheDir(dataDir, serverUUID)), iconFile(id))
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{"error": "icon not found"}
	}
	return map[string]interface{}{"content": base64.StdEncoding.EncodeToString(data)}
}

func intParam(params map[string]interface{}, key string, def int) int {
	if f, ok := params[key].(float64); ok {
		return int(f)
	}
	return def
}
