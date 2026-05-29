package moddata

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sterango/redstonecore-agent/internal/maprender"
)

// Index is the per-server item + recipe catalog, persisted as index.json.
type Index struct {
	Items     []Item           `json:"items"`
	Recipes   []Recipe         `json:"recipes"`
	ByOutput  map[string][]int `json:"byOutput"` // item id -> recipe indices producing it
	ByInput   map[string][]int `json:"byInput"`  // item id -> recipe indices consuming it
	Signature string           `json:"signature"`
	BuiltAt   int64            `json:"builtAt"`
}

type recipeRaw struct {
	ns   string
	data []byte
}

// cachedIndex is the in-memory form, with name/icon lookups precomputed.
type cachedIndex struct {
	idx  *Index
	byID map[string]Item
}

var indexMem sync.Map // serverUUID -> *cachedIndex

// indexLimit bounds concurrent builds (jar scanning is memory-heavy).
var indexBuildSem = make(chan struct{}, 2)

func indexJSONPath(dir string) string { return filepath.Join(dir, "index.json") }
func iconsDir(dir string) string      { return filepath.Join(dir, "icons") }

// getIndex returns the in-memory index for a server, loading from disk or
// building if stale/missing.
func getIndex(serverDir, serverUUID, dataDir string) (*cachedIndex, error) {
	sig := modsSignature(serverDir)
	dir := indexCacheDir(dataDir, serverUUID)

	if v, ok := indexMem.Load(serverUUID); ok {
		ci := v.(*cachedIndex)
		if ci.idx.Signature == sig {
			return ci, nil
		}
	}

	// Try disk if the signature matches.
	if sig != "" && readSig(dir) == sig {
		if data, err := os.ReadFile(indexJSONPath(dir)); err == nil {
			var idx Index
			if json.Unmarshal(data, &idx) == nil {
				return storeMem(serverUUID, &idx), nil
			}
		}
	}

	return rebuild(serverDir, serverUUID, dataDir)
}

// rebuild scans the jars, writes the cache, and returns the in-memory index.
func rebuild(serverDir, serverUUID, dataDir string) (*cachedIndex, error) {
	indexBuildSem <- struct{}{}
	defer func() { <-indexBuildSem }()

	dir := indexCacheDir(dataDir, serverUUID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(iconsDir(dir), 0775); err != nil {
		return nil, err
	}

	idx := buildIndex(serverDir, iconsDir(dir))
	idx.Signature = modsSignature(serverDir)
	idx.BuiltAt = time.Now().Unix()

	if data, err := json.Marshal(idx); err == nil {
		_ = os.WriteFile(indexJSONPath(dir), data, 0644)
	}
	_ = writeSig(dir, idx.Signature)

	return storeMem(serverUUID, idx), nil
}

func storeMem(serverUUID string, idx *Index) *cachedIndex {
	byID := make(map[string]Item, len(idx.Items))
	for _, it := range idx.Items {
		byID[it.ID] = it
	}
	ci := &cachedIndex{idx: idx, byID: byID}
	indexMem.Store(serverUUID, ci)
	return ci
}

// buildIndex does a single pass over all jars, collecting models, textures,
// lang and recipe JSON, then resolves items (+icons) and parses recipes.
func buildIndex(serverDir, icons string) *Index {
	jars := maprender.FindJars(serverDir)

	itemModels := map[string][]byte{}
	blockModels := map[string][]byte{}
	textures := map[string][]byte{}
	lang := map[string]string{}
	presentMods := map[string]bool{}
	var recipeRaws []recipeRaw

	for _, jarPath := range jars {
		zr, err := zip.OpenReader(jarPath)
		if err != nil {
			continue
		}
		for _, f := range zr.File {
			name := f.Name
			switch {
			case strings.HasPrefix(name, "assets/") && strings.HasSuffix(name, "/lang/en_us.json"):
				mergeLangEntry(f, lang)
			case strings.Contains(name, "/models/item/") && strings.HasSuffix(name, ".json"):
				ns, rest := assetParts(name, "/models/item/")
				if ns != "" {
					if b := readEntry(f); b != nil {
						itemModels[ns+":"+strings.TrimSuffix(rest, ".json")] = b
						presentMods[ns] = true
					}
				}
			case strings.Contains(name, "/models/block/") && strings.HasSuffix(name, ".json"):
				ns, rest := assetParts(name, "/models/block/")
				if ns != "" {
					if b := readEntry(f); b != nil {
						blockModels[ns+":"+strings.TrimSuffix(rest, ".json")] = b
					}
				}
			case strings.Contains(name, "/textures/item/") && strings.HasSuffix(name, ".png"):
				ns, rest := assetParts(name, "/textures/item/")
				if ns != "" {
					if b := readEntry(f); b != nil {
						textures[ns+":item/"+strings.TrimSuffix(rest, ".png")] = b
					}
				}
			case strings.Contains(name, "/textures/block/") && strings.HasSuffix(name, ".png"):
				ns, rest := assetParts(name, "/textures/block/")
				if ns != "" {
					if b := readEntry(f); b != nil {
						textures[ns+":block/"+strings.TrimSuffix(rest, ".png")] = b
					}
				}
			case strings.HasPrefix(name, "data/") && strings.Contains(name, "/recipe") && strings.HasSuffix(name, ".json"):
				if ns := dataNS(name); ns != "" {
					if b := readEntry(f); b != nil {
						recipeRaws = append(recipeRaws, recipeRaw{ns, b})
					}
				}
			}
		}
		zr.Close()
	}

	// Datapack-style recipe overrides shipped with the instance (KubeJS data/).
	recipeRaws = append(recipeRaws, scanKubeJSRecipes(serverDir)...)

	// Resolve items + write icons.
	items := make([]Item, 0, len(itemModels))
	for id, model := range itemModels {
		ns, _ := splitID(id)
		it := Item{ID: id, Name: displayName(id, lang), Mod: ns}
		if texID := resolveItemTextureID(model, blockModels, 2); texID != "" {
			if png, ok := textures[texID]; ok {
				if os.WriteFile(filepath.Join(icons, iconFile(id)), png, 0644) == nil {
					it.HasIcon = true
				}
			}
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	// Parse recipes + build inverted indices.
	recipes := make([]Recipe, 0, len(recipeRaws))
	byOutput := map[string][]int{}
	byInput := map[string][]int{}
	for _, rr := range recipeRaws {
		rec, ok := parseRecipe(rr.data, presentMods)
		if !ok {
			continue
		}
		i := len(recipes)
		recipes = append(recipes, *rec)

		seen := map[string]bool{}
		for _, o := range rec.Outputs {
			if o.Item != "" && !seen["o:"+o.Item] {
				byOutput[o.Item] = append(byOutput[o.Item], i)
				seen["o:"+o.Item] = true
			}
		}
		ins := append([]Ingredient{}, rec.Inputs...)
		for _, k := range rec.Key {
			ins = append(ins, k)
		}
		for _, in := range ins {
			if in.Item != "" && !seen["i:"+in.Item] {
				byInput[in.Item] = append(byInput[in.Item], i)
				seen["i:"+in.Item] = true
			}
		}
	}

	return &Index{Items: items, Recipes: recipes, ByOutput: byOutput, ByInput: byInput}
}

// scanKubeJSRecipes reads instance-local datapack recipe JSON under kubejs/data.
func scanKubeJSRecipes(serverDir string) []recipeRaw {
	var out []recipeRaw
	base := filepath.Join(serverDir, "kubejs", "data")
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, e := filepath.Rel(base, path)
		if e != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 3 || (parts[1] != "recipe" && parts[1] != "recipes") {
			return nil
		}
		if b, e := os.ReadFile(path); e == nil {
			out = append(out, recipeRaw{parts[0], b})
		}
		return nil
	})
	return out
}

// assetParts splits "assets/<ns><marker><rest>" into ns and rest. ns must be a
// single path segment.
func assetParts(name, marker string) (ns, rest string) {
	if !strings.HasPrefix(name, "assets/") {
		return "", ""
	}
	i := strings.Index(name, marker)
	if i < 0 {
		return "", ""
	}
	ns = name[len("assets/"):i]
	if ns == "" || strings.Contains(ns, "/") {
		return "", ""
	}
	return ns, name[i+len(marker):]
}

// dataNS returns the namespace segment of a "data/<ns>/..." path.
func dataNS(name string) string {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// search filters the item catalog by query (name or id substring) and optional
// mod, returning a page plus the total match count.
func (ci *cachedIndex) search(q, mod string, limit, offset int) ([]Item, int) {
	q = strings.ToLower(strings.TrimSpace(q))
	matched := make([]Item, 0, 64)
	for _, it := range ci.idx.Items {
		if mod != "" && it.Mod != mod {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Name), q) && !strings.Contains(strings.ToLower(it.ID), q) {
			continue
		}
		matched = append(matched, it)
	}
	total := len(matched)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return matched[offset:end], total
}

// recipesFor returns recipes that produce (makes) and consume (uses) an item.
func (ci *cachedIndex) recipesFor(id string) (makes, uses []Recipe) {
	for _, i := range ci.idx.ByOutput[id] {
		makes = append(makes, ci.idx.Recipes[i])
	}
	for _, i := range ci.idx.ByInput[id] {
		uses = append(uses, ci.idx.Recipes[i])
	}
	return makes, uses
}

// referencedMeta returns name + hasIcon lookups for every item id referenced by
// the given recipe sets, so the frontend can render ingredients without a
// separate round-trip per id.
func (ci *cachedIndex) referencedMeta(sets ...[]Recipe) (map[string]string, map[string]bool) {
	names := map[string]string{}
	icons := map[string]bool{}
	add := func(id string) {
		if id == "" {
			return
		}
		if _, done := names[id]; done {
			return
		}
		if it, ok := ci.byID[id]; ok {
			names[id] = it.Name
			icons[id] = it.HasIcon
		} else {
			_, path := splitID(id)
			names[id] = prettify(path)
		}
	}
	for _, set := range sets {
		for _, r := range set {
			for _, o := range r.Outputs {
				add(o.Item)
			}
			for _, in := range r.Inputs {
				add(in.Item)
			}
			for _, k := range r.Key {
				add(k.Item)
			}
		}
	}
	return names, icons
}

func readEntry(f *zip.File) []byte {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil
	}
	return b
}
