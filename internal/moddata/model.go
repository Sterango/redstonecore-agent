package moddata

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HandleModModel returns a render-ready bundle for a 3D item (JSON elements or
// OBJ): the geometry plus the base64 PNGs of every texture it uses, so the
// browser can render it like the in-game inventory. Flat sprite items return an
// error (the frontend falls back to the 2D icon). Bundles are cached on disk.
func HandleModModel(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	id, _ := params["id"].(string)
	if id == "" {
		return map[string]interface{}{"error": "id is required"}
	}

	cachePath := filepath.Join(indexCacheDir(dataDir, serverUUID), "models3d", safeKey(id)+".json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var bundle map[string]interface{}
		// Honor the bundle format version so stale caches rebuild after a change.
		if json.Unmarshal(data, &bundle) == nil {
			if v, _ := bundle["v"].(float64); int(v) == bundleSchema {
				return bundle
			}
		}
	}

	ci, err := getIndex(serverDir, serverUUID, dataDir)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	it, ok := ci.byID[id]
	if !ok || it.Render == "" {
		return map[string]interface{}{"error": "not a model"}
	}

	js := newJarSet(ci.idx.NsToJars)
	defer js.close()

	ns, path := splitID(id)
	fm := &flatModel{Textures: map[string]string{}}
	flatten(js, "assets/"+ns+"/models/item/"+path+".json", 16, fm)

	bundle := buildBundle(js, it.Render, fm)
	if bundle == nil {
		return map[string]interface{}{"error": "not a model"}
	}
	bundle["id"] = id

	if b, e := json.Marshal(bundle); e == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0775)
		_ = os.WriteFile(cachePath, b, 0644)
	}
	return bundle
}

// ── Flattening ──────────────────────────────────────────────────────────

type rawModel struct {
	Parent   string                     `json:"parent"`
	Loader   string                     `json:"loader"`
	FlipV    bool                       `json:"flip_v"`
	Model    string                     `json:"model"`
	Textures map[string]string          `json:"textures"`
	Elements []json.RawMessage          `json:"elements"`
	Display  map[string]json.RawMessage `json:"display"`
}

type flatModel struct {
	Loader   string
	FlipV    bool
	ModelRef string
	Textures map[string]string
	Elements []json.RawMessage
	Display  map[string]json.RawMessage
}

// flatten merges a model with its parent chain (child wins for textures; the
// most-derived elements/loader/display are kept).
func flatten(js *jarSet, modelPath string, depth int, out *flatModel) {
	if depth <= 0 || modelPath == "" {
		return
	}
	data := js.read(modelPath)
	if data == nil {
		return
	}
	var rm rawModel
	if json.Unmarshal(data, &rm) != nil {
		return
	}
	for k, v := range rm.Textures {
		if _, set := out.Textures[k]; !set {
			out.Textures[k] = v
		}
	}
	if out.Loader == "" && rm.Loader != "" {
		out.Loader, out.FlipV, out.ModelRef = rm.Loader, rm.FlipV, rm.Model
	}
	if out.Elements == nil && len(rm.Elements) > 0 {
		out.Elements = rm.Elements
	}
	if out.Display == nil && rm.Display != nil {
		out.Display = rm.Display
	}
	if rm.Parent != "" && !strings.Contains(rm.Parent, "builtin/") {
		flatten(js, modelRefToPath(rm.Parent), depth-1, out)
	}
}

func buildBundle(js *jarSet, kind string, fm *flatModel) map[string]interface{} {
	resolved := map[string]string{}
	textureData := map[string]string{}
	for k, v := range fm.Textures {
		tid := resolveVar(fm.Textures, v, 6)
		if tid == "" || strings.HasPrefix(tid, "#") {
			continue
		}
		resolved[k] = tid
		if _, have := textureData[tid]; !have {
			if png := js.read(texIDToPath(tid)); png != nil {
				textureData[tid] = base64.StdEncoding.EncodeToString(png)
			}
		}
	}

	b := map[string]interface{}{"v": bundleSchema, "textures": resolved, "textureData": textureData}
	if fm.Display != nil {
		if g, ok := fm.Display["gui"]; ok {
			b["display"] = g
		}
	}

	switch {
	case kind == "elements" && len(fm.Elements) > 0:
		b["kind"] = "elements"
		b["elements"] = fm.Elements
		return b
	case kind == "obj" && strings.Contains(fm.Loader, "obj") && fm.ModelRef != "":
		obj := js.read(objRefToPath(fm.ModelRef))
		if obj == nil {
			return nil
		}
		b["kind"] = "obj"
		b["obj"] = string(obj)
		b["flipV"] = fm.FlipV
		// OBJ materials map (usemtl name -> texId) parsed from the .mtl, since
		// NeoForge maps each material's map_Kd to a #texture variable.
		b["materials"] = parseMtl(js, fm.ModelRef, string(obj), resolved)
		return b
	}
	return nil
}

// parseMtl reads the .mtl referenced by an obj and maps each material name to a
// resolved texId (map_Kd "#var" -> resolved[var]).
func parseMtl(js *jarSet, objRef, objText string, resolved map[string]string) map[string]string {
	mats := map[string]string{}
	var mtlName string
	for _, line := range strings.Split(objText, "\n") {
		if strings.HasPrefix(line, "mtllib ") {
			mtlName = strings.TrimSpace(line[len("mtllib "):])
			break
		}
	}
	if mtlName == "" {
		return mats
	}
	objPath := objRefToPath(objRef)
	dir := objPath[:strings.LastIndex(objPath, "/")+1]
	data := js.read(dir + mtlName)
	if data == nil {
		return mats
	}
	var cur string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "newmtl "):
			cur = strings.TrimSpace(line[len("newmtl "):])
		case strings.HasPrefix(line, "map_Kd ") && cur != "":
			ref := strings.TrimPrefix(strings.TrimSpace(line[len("map_Kd "):]), "#")
			if texId, ok := resolved[ref]; ok {
				mats[cur] = texId
			}
		}
	}
	return mats
}

const bundleSchema = 2

// resolveVar follows "#var" texture references through the textures map.
func resolveVar(tex map[string]string, v string, depth int) string {
	for depth > 0 && strings.HasPrefix(v, "#") {
		nv, ok := tex[strings.TrimPrefix(v, "#")]
		if !ok {
			return v
		}
		v, depth = nv, depth-1
	}
	return v
}

// modelRefToPath: "minecraft:block/cube_all" -> "assets/minecraft/models/block/cube_all.json"
func modelRefToPath(ref string) string {
	ns, rest := splitID(ref)
	return "assets/" + ns + "/models/" + rest + ".json"
}

// texIDToPath: "minecraft:block/spruce_log" -> "assets/minecraft/textures/block/spruce_log.png"
func texIDToPath(texID string) string {
	ns, rest := splitID(texID)
	return "assets/" + ns + "/textures/" + rest + ".png"
}

// objRefToPath: "create:models/block/x.obj" -> "assets/create/models/block/x.obj"
func objRefToPath(ref string) string {
	ns, rest := splitID(ref)
	return "assets/" + ns + "/" + rest
}

// ── On-demand jar reading ───────────────────────────────────────────────

type jarSet struct {
	nsToJars map[string][]string
	entries  map[string]map[string]*zip.File // jarPath -> name -> file
	readers  map[string]*zip.ReadCloser
}

func newJarSet(nsToJars map[string][]string) *jarSet {
	return &jarSet{nsToJars: nsToJars, entries: map[string]map[string]*zip.File{}, readers: map[string]*zip.ReadCloser{}}
}

func (j *jarSet) close() {
	for _, r := range j.readers {
		r.Close()
	}
}

func (j *jarSet) index(jarPath string) map[string]*zip.File {
	if m, ok := j.entries[jarPath]; ok {
		return m
	}
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		j.entries[jarPath] = nil
		return nil
	}
	j.readers[jarPath] = r
	m := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		m[f.Name] = f
	}
	j.entries[jarPath] = m
	return m
}

// read returns the bytes of an asset, searching jars that provide its namespace.
func (j *jarSet) read(assetPath string) []byte {
	ns := assetNamespace(assetPath)
	if ns == "" {
		return nil
	}
	for _, jp := range j.nsToJars[ns] {
		if m := j.index(jp); m != nil {
			if f, ok := m[assetPath]; ok {
				return readEntry(f)
			}
		}
	}
	return nil
}

func assetNamespace(assetPath string) string {
	if !strings.HasPrefix(assetPath, "assets/") {
		return ""
	}
	rest := assetPath[len("assets/"):]
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}
