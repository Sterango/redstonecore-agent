package minecraft

import (
	"archive/zip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Detection describes an existing server found on disk, as recovered from an
// imported archive. Every field is best-effort: the panel shows this to the user
// for confirmation rather than acting on it blindly, and Warnings carries
// anything that deserves a second look.
type Detection struct {
	Type             string            `json:"type"`
	MinecraftVersion string            `json:"minecraft_version"`
	LoaderVersion    string            `json:"loader_version,omitempty"`
	Port             int               `json:"port,omitempty"`
	MaxPlayers       int               `json:"max_players,omitempty"`
	AllocatedRAMMB   int               `json:"allocated_ram_mb,omitempty"`
	JarFile          string            `json:"jar_file,omitempty"`
	StartupCommand   string            `json:"startup_command,omitempty"`
	LevelName        string            `json:"level_name,omitempty"`
	MOTD             string            `json:"motd,omitempty"`
	WorldSizeBytes   int64             `json:"world_size_bytes,omitempty"`
	TotalSizeBytes   int64             `json:"total_size_bytes,omitempty"`
	ModCount         int               `json:"mod_count"`
	PluginCount      int               `json:"plugin_count"`
	Properties       map[string]string `json:"properties,omitempty"`
	Modpack          *DetectedModpack  `json:"modpack,omitempty"`
	Warnings         []string          `json:"warnings,omitempty"`

	// RootPrefix is the subdirectory inside the extraction that actually holds
	// the server, when the archive wraps everything in a folder.
	RootPrefix string `json:"root_prefix,omitempty"`
}

// DetectedModpack carries modpack identity when the archive came from a
// CurseForge or Modrinth pack.
type DetectedModpack struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Loader  string `json:"loader,omitempty"`
	Source  string `json:"source,omitempty"` // curseforge | modrinth
}

var (
	// forge-1.20.1-47.2.0.jar, neoforge-21.1.77.jar, fabric-server-mc.1.21-loader.0.16.0-launcher.1.0.1.jar
	reForgeJar    = regexp.MustCompile(`(?i)^forge-(\d+\.\d+(?:\.\d+)?)-(\d+(?:\.\d+)*)`)
	reNeoForgeJar = regexp.MustCompile(`(?i)^neoforge-(\d+(?:\.\d+)*)`)
	reFabricJar   = regexp.MustCompile(`(?i)fabric-server-mc\.(\d+(?:\.\d+)*)-loader\.(\d+(?:\.\d+)*)`)
	rePaperJar    = regexp.MustCompile(`(?i)^(?:paper|purpur|folia|pufferfish)(?:clip)?-(\d+\.\d+(?:\.\d+)?)`)
	reSpigotJar   = regexp.MustCompile(`(?i)^(?:spigot|craftbukkit)-(\d+\.\d+(?:\.\d+)?)`)
	reVanillaJar  = regexp.MustCompile(`(?i)^minecraft_server\.(\d+\.\d+(?:\.\d+)?)`)
	reVelocityJar = regexp.MustCompile(`(?i)^velocity-(\d+(?:\.\d+)*)`)
	reWaterfall   = regexp.MustCompile(`(?i)^(?:waterfall|bungeecord)`)

	// Modern Forge/NeoForge keep the real versions in libraries paths and in
	// the generated @argfile, e.g. libraries/net/neoforged/neoforge/21.1.77/...
	reNeoLibrary   = regexp.MustCompile(`libraries[/\\]net[/\\]neoforged[/\\]neoforge[/\\](\d+(?:\.\d+)*)`)
	reForgeLibrary = regexp.MustCompile(`libraries[/\\]net[/\\]minecraftforge[/\\]forge[/\\](\d+\.\d+(?:\.\d+)?)-(\d+(?:\.\d+)*)`)

	// -Xmx8G / -Xmx8192M in a start script or user_jvm_args.txt
	reXmx = regexp.MustCompile(`(?i)-Xmx(\d+)\s*([gGmM])`)
)

// DetectServer inspects an extracted server directory and reports what it is.
// It never fails outright — an unrecognisable directory comes back as a
// best-guess vanilla with warnings attached, so the user can correct it.
func DetectServer(dir string) *Detection {
	d := &Detection{
		Warnings:   []string{},
		Properties: map[string]string{},
	}

	// The archive may wrap the server in one top-level folder ("MyServer/").
	root, prefix := ResolveServerRoot(dir)
	d.RootPrefix = prefix

	d.readProperties(root)
	d.detectTypeAndVersion(root)
	d.detectMemoryAndStartup(root)
	d.detectModpack(root)
	d.countContent(root)
	d.measure(root)
	d.sanityCheck(root)

	return d
}

// ResolveServerRoot finds the directory that actually contains the server.
// Zips exported from other panels commonly nest everything one level deep, and
// importing that literally would produce a server whose files are all one
// directory too far down. Returns the resolved directory and the prefix that was
// stripped ("" when the archive was already rooted at the server).
func ResolveServerRoot(dir string) (string, string) {
	if looksLikeServerDir(dir) {
		return dir, ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return dir, ""
	}

	// Ignore junk macOS/Windows archivers add alongside the real folder.
	candidates := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if name == "__MACOSX" || name == ".DS_Store" || strings.HasPrefix(name, "._") {
			continue
		}
		candidates = append(candidates, e)
	}

	if len(candidates) == 1 && candidates[0].IsDir() {
		nested := filepath.Join(dir, candidates[0].Name())
		if looksLikeServerDir(nested) {
			return nested, candidates[0].Name()
		}
		// Allow one more level; some exports are "backup/world-name/".
		if inner, innerPrefix := ResolveServerRoot(nested); innerPrefix != "" {
			return inner, filepath.Join(candidates[0].Name(), innerPrefix)
		}
	}

	return dir, ""
}

// looksLikeServerDir is the marker test for "a Minecraft server lives here".
func looksLikeServerDir(dir string) bool {
	markers := []string{
		"server.properties", "eula.txt", "ops.json", "whitelist.json",
		"server.jar", "run.sh", "start.sh", "startserver.sh", "bungeecord.yml",
		"velocity.toml", "libraries", "mods", "plugins", "world",
	}

	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}

	// A bare "*.jar" plus a world folder is enough to count.
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.jar")); len(matches) > 0 {
		return true
	}

	return false
}

func (d *Detection) readProperties(root string) {
	props, err := LoadServerProperties(root)
	if err != nil || len(props) == 0 {
		d.Warnings = append(d.Warnings, "No server.properties found — port, slots and world name had to be guessed.")
		return
	}

	d.Properties = props

	if v, ok := props["server-port"]; ok {
		if port, err := strconv.Atoi(v); err == nil && port > 0 && port < 65536 {
			d.Port = port
		}
	}
	if v, ok := props["max-players"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			d.MaxPlayers = n
		}
	}

	d.LevelName = props["level-name"]
	d.MOTD = props["motd"]
}

// detectTypeAndVersion identifies the server flavour and its versions. Order
// matters: proxies and modern loaders are checked before the generic jar scan,
// because a Forge install also ships a plain-looking server jar.
func (d *Detection) detectTypeAndVersion(root string) {
	// --- Proxies, which have no world and their own config files ---
	if fileExists(root, "velocity.toml") {
		d.Type = string(TypeVelocity)
		d.scanJarsForVersion(root)
		return
	}
	// BungeeCord/Waterfall: a proxy config plus a matching jar. Checked together
	// so a Bukkit server that also has a config.yml isn't misread as a proxy.
	if fileExists(root, "bungeecord.yml") || (fileExists(root, "config.yml") && dirExists(root, "modules")) {
		if d.jarMatching(root, reWaterfall) != "" {
			d.Type = string(TypeBungeeCord)
			d.scanJarsForVersion(root)
			return
		}
	}

	// --- Modern NeoForge / Forge: the jar is gone, the versions live in
	// libraries/ and the launch args file. ---
	if v, mc, loader := detectFromArgsFiles(root); v != "" {
		d.Type = v
		d.MinecraftVersion = mc
		d.LoaderVersion = loader
		d.JarFile = pickLaunchScript(root)
		return
	}

	// --- Fabric ---
	if fileExists(root, "fabric-server-launch.jar") || fileExists(root, "fabric-server-launcher.properties") {
		d.Type = string(TypeFabric)
		d.JarFile = "fabric-server-launch.jar"

		// fabric-server-launcher.properties points at the real launcher jar,
		// whose name carries both versions.
		if data, err := os.ReadFile(filepath.Join(root, "fabric-server-launcher.properties")); err == nil {
			if m := reFabricJar.FindStringSubmatch(string(data)); len(m) == 3 {
				d.MinecraftVersion, d.LoaderVersion = m[1], m[2]
			}
		}
		if d.MinecraftVersion == "" {
			if jar := d.jarMatching(root, reFabricJar); jar != "" {
				if m := reFabricJar.FindStringSubmatch(jar); len(m) == 3 {
					d.MinecraftVersion, d.LoaderVersion = m[1], m[2]
					d.JarFile = jar
				}
			}
		}
		if d.MinecraftVersion == "" {
			d.MinecraftVersion = versionFromServerJar(root)
		}
		return
	}

	// --- Jar-name based detection for everything else ---
	d.scanJarsForVersion(root)

	if d.Type == "" {
		// Fall back on directory shape: plugins/ means a Bukkit derivative,
		// mods/ means a mod loader.
		switch {
		case dirExists(root, "plugins"):
			d.Type = string(TypePaper)
			d.Warnings = append(d.Warnings, "Server type was inferred from a plugins/ folder; confirm Paper vs Spigot.")
		case dirExists(root, "mods"):
			d.Type = string(TypeForge)
			d.Warnings = append(d.Warnings, "Server type was inferred from a mods/ folder; confirm the mod loader.")
		default:
			d.Type = string(TypeVanilla)
			d.Warnings = append(d.Warnings, "Could not identify the server type; defaulted to vanilla.")
		}
	}

	if d.MinecraftVersion == "" {
		d.MinecraftVersion = versionFromServerJar(root)
	}
	if d.MinecraftVersion == "" {
		d.Warnings = append(d.Warnings, "Could not determine the Minecraft version — set it before starting the server.")
	}
}

// scanJarsForVersion walks the top-level jars and matches them against the known
// distribution naming schemes.
func (d *Detection) scanJarsForVersion(root string) {
	matches, _ := filepath.Glob(filepath.Join(root, "*.jar"))

	for _, path := range matches {
		name := filepath.Base(path)
		if strings.Contains(strings.ToLower(name), "installer") {
			continue
		}

		switch {
		case reNeoForgeJar.MatchString(name):
			m := reNeoForgeJar.FindStringSubmatch(name)
			d.Type, d.LoaderVersion, d.JarFile = string(TypeNeoForge), m[1], name
			// NeoForge versions encode MC as 21.1.x -> 1.21.1
			d.MinecraftVersion = neoForgeToMinecraft(m[1])
			return

		case reForgeJar.MatchString(name):
			m := reForgeJar.FindStringSubmatch(name)
			d.Type, d.MinecraftVersion, d.LoaderVersion, d.JarFile = string(TypeForge), m[1], m[2], name
			return

		case reFabricJar.MatchString(name):
			m := reFabricJar.FindStringSubmatch(name)
			d.Type, d.MinecraftVersion, d.LoaderVersion, d.JarFile = string(TypeFabric), m[1], m[2], name
			return

		case rePaperJar.MatchString(name):
			m := rePaperJar.FindStringSubmatch(name)
			d.Type, d.MinecraftVersion, d.JarFile = string(TypePaper), m[1], name
			return

		case reSpigotJar.MatchString(name):
			m := reSpigotJar.FindStringSubmatch(name)
			d.Type, d.MinecraftVersion, d.JarFile = string(TypeSpigot), m[1], name
			return

		case reVelocityJar.MatchString(name):
			m := reVelocityJar.FindStringSubmatch(name)
			d.Type, d.LoaderVersion, d.JarFile = string(TypeVelocity), m[1], name
			return

		case reWaterfall.MatchString(name):
			d.Type, d.JarFile = string(TypeBungeeCord), name
			return

		case reVanillaJar.MatchString(name):
			m := reVanillaJar.FindStringSubmatch(name)
			d.Type, d.MinecraftVersion, d.JarFile = string(TypeVanilla), m[1], name
			return
		}
	}

	// A generic server.jar tells us nothing by name, so read its manifest.
	if fileExists(root, "server.jar") {
		d.JarFile = "server.jar"
		if v := versionFromServerJar(root); v != "" {
			d.MinecraftVersion = v
			if d.Type == "" {
				d.Type = string(TypeVanilla)
			}
		}
	}
}

// detectFromArgsFiles handles modern Forge/NeoForge, which launch from
// libraries/ via an @argfile rather than a runnable jar.
func detectFromArgsFiles(root string) (serverType, mcVersion, loaderVersion string) {
	candidates := []string{
		filepath.Join(root, "libraries", "net", "neoforged", "neoforge"),
		filepath.Join(root, "libraries", "net", "minecraftforge", "forge"),
	}

	// The versioned directory name under libraries/ is authoritative.
	if entries, err := os.ReadDir(candidates[0]); err == nil && len(entries) > 0 {
		for _, e := range entries {
			if e.IsDir() {
				return string(TypeNeoForge), neoForgeToMinecraft(e.Name()), e.Name()
			}
		}
	}
	if entries, err := os.ReadDir(candidates[1]); err == nil && len(entries) > 0 {
		for _, e := range entries {
			if e.IsDir() {
				// Directory name is "1.20.1-47.2.0".
				parts := strings.SplitN(e.Name(), "-", 2)
				if len(parts) == 2 {
					return string(TypeForge), parts[0], parts[1]
				}
			}
		}
	}

	// Otherwise fall back to scanning the generated arg files for library paths.
	for _, rel := range []string{
		filepath.Join("libraries", "net", "neoforged", "neoforge"),
		"user_jvm_args.txt", "run.sh", "run.bat", "startserver.sh",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		text := string(data)

		if m := reNeoLibrary.FindStringSubmatch(text); len(m) == 2 {
			return string(TypeNeoForge), neoForgeToMinecraft(m[1]), m[1]
		}
		if m := reForgeLibrary.FindStringSubmatch(text); len(m) == 3 {
			return string(TypeForge), m[1], m[2]
		}
	}

	return "", "", ""
}

// neoForgeToMinecraft maps a NeoForge version to its Minecraft version:
// NeoForge 21.1.77 targets Minecraft 1.21.1, 20.4.190 targets 1.20.4.
func neoForgeToMinecraft(neoVersion string) string {
	parts := strings.Split(neoVersion, ".")
	if len(parts) < 2 {
		return ""
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return ""
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return ""
	}

	// A trailing ".0" minor means the base release, e.g. 21.0.x -> 1.21.
	if minor == 0 {
		return "1." + strconv.Itoa(major)
	}

	return "1." + strconv.Itoa(major) + "." + strconv.Itoa(minor)
}

// versionFromServerJar reads version.json from inside the server jar, which is
// the most reliable source when filenames are unhelpful.
func versionFromServerJar(root string) string {
	candidates, _ := filepath.Glob(filepath.Join(root, "*.jar"))

	for _, path := range candidates {
		if strings.Contains(strings.ToLower(filepath.Base(path)), "installer") {
			continue
		}

		zr, err := zip.OpenReader(path)
		if err != nil {
			continue
		}

		for _, f := range zr.File {
			if f.Name != "version.json" {
				continue
			}

			rc, err := f.Open()
			if err != nil {
				break
			}

			// version.json is tiny; cap the read so a hostile jar can't blow up memory.
			data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				break
			}

			var meta struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &meta) == nil {
				version := meta.ID
				if version == "" {
					version = meta.Name
				}
				if version != "" {
					zr.Close()
					return version
				}
			}
			break
		}
		zr.Close()
	}

	return ""
}

// detectMemoryAndStartup recovers the heap size and launch command the server
// was previously running with, so an import keeps its tuning.
func (d *Detection) detectMemoryAndStartup(root string) {
	scripts := []string{
		"user_jvm_args.txt", "startserver.sh", "start.sh", "run.sh",
		"ServerStart.sh", "launch.sh", "start.bat", "run.bat",
	}

	for _, name := range scripts {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		text := string(data)

		if d.AllocatedRAMMB == 0 {
			if m := reXmx.FindStringSubmatch(text); len(m) == 3 {
				if n, err := strconv.Atoi(m[1]); err == nil {
					if strings.EqualFold(m[2], "g") {
						d.AllocatedRAMMB = n * 1024
					} else {
						d.AllocatedRAMMB = n
					}
				}
			}
		}

		// Keep the first real launch line as a starting point for the user.
		if d.StartupCommand == "" && strings.HasSuffix(name, ".sh") {
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "java ") || strings.Contains(line, "-jar ") {
					if len(line) > 4000 {
						line = line[:4000]
					}
					d.StartupCommand = line
					break
				}
			}
		}
	}
}

// detectModpack recognises CurseForge and Modrinth pack metadata so the imported
// server keeps its modpack identity in the panel.
func (d *Detection) detectModpack(root string) {
	// CurseForge: manifest.json
	if data, err := os.ReadFile(filepath.Join(root, "manifest.json")); err == nil {
		var manifest struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Minecraft struct {
				Version    string `json:"version"`
				ModLoaders []struct {
					ID string `json:"id"`
				} `json:"modLoaders"`
			} `json:"minecraft"`
		}

		if json.Unmarshal(data, &manifest) == nil && manifest.Name != "" {
			pack := &DetectedModpack{
				Name:    manifest.Name,
				Version: manifest.Version,
				Source:  "curseforge",
			}
			if len(manifest.Minecraft.ModLoaders) > 0 {
				// IDs look like "neoforge-21.1.77" or "forge-47.2.0".
				parts := strings.SplitN(manifest.Minecraft.ModLoaders[0].ID, "-", 2)
				pack.Loader = parts[0]
				if d.LoaderVersion == "" && len(parts) == 2 {
					d.LoaderVersion = parts[1]
				}
			}
			if d.MinecraftVersion == "" {
				d.MinecraftVersion = manifest.Minecraft.Version
			}
			d.Modpack = pack
			return
		}
	}

	// Modrinth: modrinth.index.json
	if data, err := os.ReadFile(filepath.Join(root, "modrinth.index.json")); err == nil {
		var index struct {
			Name         string            `json:"name"`
			VersionID    string            `json:"versionId"`
			Dependencies map[string]string `json:"dependencies"`
		}

		if json.Unmarshal(data, &index) == nil && index.Name != "" {
			pack := &DetectedModpack{
				Name:    index.Name,
				Version: index.VersionID,
				Source:  "modrinth",
			}
			if mc, ok := index.Dependencies["minecraft"]; ok && d.MinecraftVersion == "" {
				d.MinecraftVersion = mc
			}
			for _, loader := range []string{"neoforge", "forge", "fabric-loader", "quilt-loader"} {
				if v, ok := index.Dependencies[loader]; ok {
					pack.Loader = strings.TrimSuffix(loader, "-loader")
					if d.LoaderVersion == "" {
						d.LoaderVersion = v
					}
					break
				}
			}
			d.Modpack = pack
		}
	}
}

func (d *Detection) countContent(root string) {
	d.ModCount = countJars(filepath.Join(root, "mods"))
	d.PluginCount = countJars(filepath.Join(root, "plugins"))
}

func countJars(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			n++
		}
	}

	return n
}

// measure sizes the import so the review screen can show what is being brought
// in, and so an oversized world is visible before the server is created.
func (d *Detection) measure(root string) {
	d.TotalSizeBytes = dirSize(root)

	level := d.LevelName
	if level == "" {
		level = "world"
	}
	d.WorldSizeBytes = dirSize(filepath.Join(root, level))
}

func dirSize(dir string) int64 {
	var total int64

	filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries shouldn't abort the measurement
		}
		if entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})

	return total
}

// sanityCheck raises the things a user most needs to know before importing.
func (d *Detection) sanityCheck(root string) {
	if !dirExists(root, "world") && d.LevelName != "" && !dirExists(root, d.LevelName) {
		d.Warnings = append(d.Warnings,
			"No world folder was found in the archive — the server will generate a fresh world on first start.")
	}

	if d.Type == string(TypeForge) || d.Type == string(TypeNeoForge) || d.Type == string(TypeFabric) {
		if d.ModCount == 0 {
			d.Warnings = append(d.Warnings,
				"A mod loader was detected but the mods/ folder is empty.")
		}
	}

	if d.Port == 0 {
		d.Port = 25565
	}
	if d.MaxPlayers == 0 {
		d.MaxPlayers = 20
	}
	if d.AllocatedRAMMB == 0 {
		// Modded servers need materially more headroom than vanilla.
		if d.ModCount > 0 {
			d.AllocatedRAMMB = 6144
			d.Warnings = append(d.Warnings,
				"No heap size was found in the start scripts; defaulted to 6GB for a modded server.")
		} else {
			d.AllocatedRAMMB = 2048
		}
	}

	// Anything still running against a Java version we can't infer is fine, but
	// an unresolved loader version for Forge/NeoForge will break the launcher.
	if (d.Type == string(TypeForge) || d.Type == string(TypeNeoForge)) && d.LoaderVersion == "" {
		d.Warnings = append(d.Warnings,
			"The mod loader version could not be determined — set it before starting the server.")
	}

	if fileExists(root, "eula.txt") {
		if data, err := os.ReadFile(filepath.Join(root, "eula.txt")); err == nil {
			if !strings.Contains(string(data), "eula=true") {
				d.Warnings = append(d.Warnings, "The EULA was not accepted in this archive; it will be accepted on import.")
			}
		}
	}
}

// jarMatching returns the first top-level jar matching re, or "".
func (d *Detection) jarMatching(root string, re *regexp.Regexp) string {
	matches, _ := filepath.Glob(filepath.Join(root, "*.jar"))

	for _, path := range matches {
		name := filepath.Base(path)
		if re.MatchString(name) {
			return name
		}
	}

	return ""
}

// pickLaunchScript returns the script a modern Forge/NeoForge install starts from.
func pickLaunchScript(root string) string {
	for _, name := range []string{"startserver.sh", "run.sh", "start.sh"} {
		if fileExists(root, name) {
			return name
		}
	}

	return ""
}

func fileExists(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

func dirExists(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.IsDir()
}
