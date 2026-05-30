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
// indexSchema is bumped whenever the cached index format changes, so stale
// on-disk caches from an older agent are ignored (the mods signature alone
// doesn't change when only the agent code changes).
const indexSchema = 5

type Index struct {
	Schema    int                 `json:"schema"`
	Items     []Item              `json:"items"`
	Recipes   []Recipe            `json:"recipes"`
	ByOutput  map[string][]int    `json:"byOutput"` // item id -> recipe indices producing it
	ByInput   map[string][]int    `json:"byInput"`  // item id -> recipe indices consuming it
	Tags      map[string][]string `json:"tags"`     // tag id -> representative member item ids (recipe-referenced only)
	Signature string              `json:"signature"`
	BuiltAt   int64               `json:"builtAt"`
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
			if json.Unmarshal(data, &idx) == nil && idx.Schema == indexSchema {
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

	// Include the vanilla client jar (cached/downloaded) so vanilla items,
	// icons, recipes and tags are indexed alongside the mods.
	var extraJars []string
	if vj := ensureVanillaJar(serverDir, dataDir); vj != "" {
		extraJars = append(extraJars, vj)
	}

	idx := buildIndex(serverDir, iconsDir(dir), extraJars)
	idx.Schema = indexSchema
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
func buildIndex(serverDir, icons string, extraJars []string) *Index {
	jars := append(maprender.FindJars(serverDir), extraJars...)

	itemModels := map[string][]byte{}
	blockModels := map[string][]byte{}
	textures := map[string][]byte{}
	lang := map[string]string{}
	presentMods := map[string]bool{}
	rawTags := map[string][]string{}
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
			case strings.HasPrefix(name, "assets/") && strings.Contains(name, "/textures/") && strings.HasSuffix(name, ".png"):
				// Collect textures from ALL subfolders (item/, block/, part/,
				// particle/, …), keyed by full texture id, so any model texture
				// reference resolves. Skip large non-icon dirs (gui, font, …).
				if ns, rest := assetParts(name, "/textures/"); ns != "" && !skipTextureDir(rest) {
					if b := readEntry(f); b != nil {
						textures[ns+":"+strings.TrimSuffix(rest, ".png")] = b
					}
				}
			case strings.HasPrefix(name, "data/") && strings.Contains(name, "/recipe") && strings.HasSuffix(name, ".json"):
				if ns := dataNS(name); ns != "" {
					if b := readEntry(f); b != nil {
						recipeRaws = append(recipeRaws, recipeRaw{ns, b})
					}
				}
			case strings.HasPrefix(name, "data/") && strings.Contains(name, "/tags/item") && strings.HasSuffix(name, ".json"):
				if ns, path := tagParts(name); ns != "" {
					if b := readEntry(f); b != nil {
						mergeTag(rawTags, ns+":"+path, b)
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
		ns, path := splitID(id)
		it := Item{ID: id, Name: displayName(id, lang), Mod: ns}
		if png := resolveIcon(model, blockModels, textures, ns, path); png != nil {
			if os.WriteFile(filepath.Join(icons, iconFile(id)), png, 0644) == nil {
				it.HasIcon = true
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

	// Resolve only the tags actually referenced by recipes, to representative
	// member item ids (so the UI can show/cycle icons instead of raw tag names).
	tags := map[string][]string{}
	addTag := func(tag string) {
		if tag == "" || tags[tag] != nil {
			return
		}
		var items []string
		resolveTag(rawTags, tag, 24, map[string]bool{}, &items)
		if len(items) > 0 {
			tags[tag] = items
		}
	}
	for _, r := range recipes {
		for _, in := range r.Inputs {
			addTag(in.Tag)
		}
		for _, k := range r.Key {
			addTag(k.Tag)
		}
	}

	return &Index{Items: items, Recipes: recipes, ByOutput: byOutput, ByInput: byInput, Tags: tags}
}

// tagParts splits "data/<ns>/tags/item(s)/<path>.json" into ns and tag path.
func tagParts(name string) (ns, path string) {
	parts := strings.SplitN(name, "/", 5)
	if len(parts) < 5 || parts[0] != "data" || parts[2] != "tags" {
		return "", ""
	}
	if parts[3] != "item" && parts[3] != "items" {
		return "", ""
	}
	return parts[1], strings.TrimSuffix(parts[4], ".json")
}

// mergeTag appends a tag file's values (item ids, {id} objects, or #tag refs).
func mergeTag(tags map[string][]string, id string, data []byte) {
	var t struct {
		Values []interface{} `json:"values"`
	}
	if json.Unmarshal(data, &t) != nil {
		return
	}
	for _, v := range t.Values {
		switch x := v.(type) {
		case string:
			tags[id] = append(tags[id], x)
		case map[string]interface{}:
			if s, ok := x["id"].(string); ok {
				tags[id] = append(tags[id], s)
			}
		}
	}
}

// resolveTag flattens a tag to member item ids, expanding nested #tag refs.
func resolveTag(rawTags map[string][]string, tag string, limit int, seen map[string]bool, out *[]string) {
	if seen[tag] || len(*out) >= limit {
		return
	}
	seen[tag] = true
	for _, v := range rawTags[tag] {
		if len(*out) >= limit {
			return
		}
		if strings.HasPrefix(v, "#") {
			resolveTag(rawTags, strings.TrimPrefix(v, "#"), limit, seen, out)
		} else {
			*out = append(*out, v)
		}
	}
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

// Large, non-item texture folders we don't need for icons.
var nonIconTextureDirs = []string{
	"gui/", "guis/", "font/", "patchouli/", "effect/", "environment/",
	"painting/", "colormap/", "map/", "mob_effect/", "misc/",
}

func skipTextureDir(rest string) bool {
	for _, d := range nonIconTextureDirs {
		if strings.HasPrefix(rest, d) {
			return true
		}
	}
	return false
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
	addTagMembers := func(tag string) {
		for _, id := range ci.idx.Tags[tag] {
			add(id)
		}
	}
	for _, set := range sets {
		for _, r := range set {
			for _, o := range r.Outputs {
				add(o.Item)
			}
			for _, in := range r.Inputs {
				add(in.Item)
				addTagMembers(in.Tag)
			}
			for _, k := range r.Key {
				add(k.Item)
				addTagMembers(k.Tag)
			}
		}
	}
	return names, icons
}

// recipeTags returns the resolved tag->items map limited to tags referenced by
// the given recipe sets.
func (ci *cachedIndex) recipeTags(sets ...[]Recipe) map[string][]string {
	out := map[string][]string{}
	take := func(tag string) {
		if tag == "" || out[tag] != nil {
			return
		}
		if members := ci.idx.Tags[tag]; len(members) > 0 {
			out[tag] = members
		}
	}
	for _, set := range sets {
		for _, r := range set {
			for _, in := range r.Inputs {
				take(in.Tag)
			}
			for _, k := range r.Key {
				take(k.Tag)
			}
		}
	}
	return out
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
