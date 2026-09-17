package minecraft

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree builds a fake server directory from a path -> contents map.
// A path ending in "/" creates a directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for path, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(path))

		if len(path) > 0 && path[len(path)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", path, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return root
}

const sampleProperties = `#Minecraft server properties
server-port=25577
max-players=42
level-name=BigWorld
motd=A Test Server
difficulty=hard
`

func TestDetectPaperServer(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":       sampleProperties,
		"paper-1.20.4-497.jar":    "",
		"plugins/EssentialsX.jar": "",
		"plugins/Vault.jar":       "",
		"BigWorld/level.dat":      "world data",
		"start.sh":                "#!/bin/sh\njava -Xmx4G -jar paper-1.20.4-497.jar nogui\n",
	})

	d := DetectServer(dir)

	if d.Type != string(TypePaper) {
		t.Errorf("Type = %q, want paper", d.Type)
	}
	if d.MinecraftVersion != "1.20.4" {
		t.Errorf("MinecraftVersion = %q, want 1.20.4", d.MinecraftVersion)
	}
	if d.Port != 25577 {
		t.Errorf("Port = %d, want 25577", d.Port)
	}
	if d.MaxPlayers != 42 {
		t.Errorf("MaxPlayers = %d, want 42", d.MaxPlayers)
	}
	if d.AllocatedRAMMB != 4096 {
		t.Errorf("AllocatedRAMMB = %d, want 4096 (from -Xmx4G)", d.AllocatedRAMMB)
	}
	if d.LevelName != "BigWorld" {
		t.Errorf("LevelName = %q, want BigWorld", d.LevelName)
	}
	if d.PluginCount != 2 {
		t.Errorf("PluginCount = %d, want 2", d.PluginCount)
	}
	// The whole server.properties must survive so the settings UI shows the
	// imported server's real config, not our defaults.
	if d.Properties["difficulty"] != "hard" {
		t.Errorf("Properties[difficulty] = %q, want hard", d.Properties["difficulty"])
	}
}

func TestDetectForgeServerFromJarName(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":       "server-port=25565\nmax-players=20\n",
		"forge-1.20.1-47.2.0.jar": "",
		"mods/jei.jar":            "",
		"mods/create.jar":         "",
		"world/level.dat":         "",
	})

	d := DetectServer(dir)

	if d.Type != string(TypeForge) {
		t.Errorf("Type = %q, want forge", d.Type)
	}
	if d.MinecraftVersion != "1.20.1" {
		t.Errorf("MinecraftVersion = %q, want 1.20.1", d.MinecraftVersion)
	}
	if d.LoaderVersion != "47.2.0" {
		t.Errorf("LoaderVersion = %q, want 47.2.0", d.LoaderVersion)
	}
	if d.ModCount != 2 {
		t.Errorf("ModCount = %d, want 2", d.ModCount)
	}
}

// Modern NeoForge has no runnable jar: versions live under libraries/.
func TestDetectNeoForgeFromLibraries(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":                         "server-port=25565\n",
		"libraries/net/neoforged/neoforge/21.1.77/": "",
		"run.sh":            "#!/bin/sh\njava @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.77/unix_args.txt\n",
		"user_jvm_args.txt": "-Xmx8G\n",
		"mods/someMod.jar":  "",
		"world/level.dat":   "",
	})

	d := DetectServer(dir)

	if d.Type != string(TypeNeoForge) {
		t.Errorf("Type = %q, want neoforge", d.Type)
	}
	if d.LoaderVersion != "21.1.77" {
		t.Errorf("LoaderVersion = %q, want 21.1.77", d.LoaderVersion)
	}
	// NeoForge 21.1.x targets Minecraft 1.21.1.
	if d.MinecraftVersion != "1.21.1" {
		t.Errorf("MinecraftVersion = %q, want 1.21.1", d.MinecraftVersion)
	}
	if d.AllocatedRAMMB != 8192 {
		t.Errorf("AllocatedRAMMB = %d, want 8192", d.AllocatedRAMMB)
	}
	if d.JarFile != "run.sh" {
		t.Errorf("JarFile = %q, want run.sh", d.JarFile)
	}
}

func TestDetectFabricServer(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":                 "server-port=25565\n",
		"fabric-server-launch.jar":          "",
		"fabric-server-launcher.properties": "serverJar=fabric-server-mc.1.21-loader.0.16.0-launcher.1.0.1.jar\n",
		"mods/fabric-api.jar":               "",
		"world/level.dat":                   "",
	})

	d := DetectServer(dir)

	if d.Type != string(TypeFabric) {
		t.Errorf("Type = %q, want fabric", d.Type)
	}
	if d.MinecraftVersion != "1.21" {
		t.Errorf("MinecraftVersion = %q, want 1.21", d.MinecraftVersion)
	}
	if d.LoaderVersion != "0.16.0" {
		t.Errorf("LoaderVersion = %q, want 0.16.0", d.LoaderVersion)
	}
}

// A zip that wraps everything in one folder must not import one level too deep.
func TestDetectResolvesWrappedArchiveRoot(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"MyCoolServer/server.properties":    sampleProperties,
		"MyCoolServer/paper-1.20.4-497.jar": "",
		"MyCoolServer/BigWorld/level.dat":   "",
	})

	d := DetectServer(dir)

	if d.RootPrefix != "MyCoolServer" {
		t.Errorf("RootPrefix = %q, want MyCoolServer", d.RootPrefix)
	}
	if d.Type != string(TypePaper) {
		t.Errorf("Type = %q, want paper", d.Type)
	}
	if d.Port != 25577 {
		t.Errorf("Port = %d, want 25577 (properties inside the wrapper)", d.Port)
	}
}

// macOS zips carry a __MACOSX sibling that must not defeat root resolution.
func TestDetectIgnoresArchiverJunk(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"__MACOSX/._server.properties": "",
		"Server/server.properties":     "server-port=25565\n",
		"Server/server.jar":            "",
		"Server/world/level.dat":       "",
	})

	d := DetectServer(dir)

	if d.RootPrefix != "Server" {
		t.Errorf("RootPrefix = %q, want Server", d.RootPrefix)
	}
}

func TestDetectCurseForgeModpack(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties": "server-port=25565\n",
		"manifest.json": `{
			"name": "All the Mods 10",
			"version": "1.2.3",
			"minecraft": {
				"version": "1.21.1",
				"modLoaders": [{"id": "neoforge-21.1.77", "primary": true}]
			}
		}`,
		"mods/a.jar":      "",
		"world/level.dat": "",
	})

	d := DetectServer(dir)

	if d.Modpack == nil {
		t.Fatal("Modpack = nil, want detected CurseForge pack")
	}
	if d.Modpack.Name != "All the Mods 10" {
		t.Errorf("Modpack.Name = %q", d.Modpack.Name)
	}
	if d.Modpack.Source != "curseforge" {
		t.Errorf("Modpack.Source = %q, want curseforge", d.Modpack.Source)
	}
	if d.Modpack.Loader != "neoforge" {
		t.Errorf("Modpack.Loader = %q, want neoforge", d.Modpack.Loader)
	}
}

func TestDetectModrinthModpack(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties": "server-port=25565\n",
		"modrinth.index.json": `{
			"name": "Cobblemon Pack",
			"versionId": "4.5.1",
			"dependencies": {"minecraft": "1.20.1", "fabric-loader": "0.15.11"}
		}`,
		"mods/a.jar": "",
	})

	d := DetectServer(dir)

	if d.Modpack == nil {
		t.Fatal("Modpack = nil, want detected Modrinth pack")
	}
	if d.Modpack.Source != "modrinth" {
		t.Errorf("Modpack.Source = %q, want modrinth", d.Modpack.Source)
	}
	if d.MinecraftVersion != "1.20.1" {
		t.Errorf("MinecraftVersion = %q, want 1.20.1", d.MinecraftVersion)
	}
}

// An unrecognisable directory must still produce usable defaults plus warnings,
// never a zero-value config that would create a broken server.
func TestDetectUnknownServerFallsBackSafely(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"eula.txt":    "eula=true\n",
		"notes.txt":   "hello",
		"world/a.dat": "",
	})

	d := DetectServer(dir)

	if d.Type != string(TypeVanilla) {
		t.Errorf("Type = %q, want vanilla fallback", d.Type)
	}
	if d.Port != 25565 {
		t.Errorf("Port = %d, want 25565 default", d.Port)
	}
	if d.MaxPlayers != 20 {
		t.Errorf("MaxPlayers = %d, want 20 default", d.MaxPlayers)
	}
	if d.AllocatedRAMMB == 0 {
		t.Error("AllocatedRAMMB = 0, want a non-zero default")
	}
	if len(d.Warnings) == 0 {
		t.Error("expected warnings for an unidentifiable server")
	}
}

// A modded server with no -Xmx anywhere should default high, not to 2GB.
func TestDetectModdedServerDefaultsToLargerHeap(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":       "server-port=25565\n",
		"forge-1.20.1-47.2.0.jar": "",
		"mods/a.jar":              "",
		"world/level.dat":         "",
	})

	d := DetectServer(dir)

	if d.AllocatedRAMMB != 6144 {
		t.Errorf("AllocatedRAMMB = %d, want 6144 for a modded server", d.AllocatedRAMMB)
	}
}

func TestNeoForgeToMinecraft(t *testing.T) {
	cases := map[string]string{
		"21.1.77":  "1.21.1",
		"21.0.167": "1.21",
		"20.4.190": "1.20.4",
		"":         "",
		"garbage":  "",
	}

	for in, want := range cases {
		if got := neoForgeToMinecraft(in); got != want {
			t.Errorf("neoForgeToMinecraft(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectVelocityProxy(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"velocity.toml":      "bind = \"0.0.0.0:25577\"\n",
		"velocity-3.3.0.jar": "",
	})

	d := DetectServer(dir)

	if d.Type != string(TypeVelocity) {
		t.Errorf("Type = %q, want velocity", d.Type)
	}
	if d.LoaderVersion != "3.3.0" {
		t.Errorf("LoaderVersion = %q, want 3.3.0", d.LoaderVersion)
	}
}

func TestDetectMeasuresWorldSize(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"server.properties":         sampleProperties,
		"paper-1.20.4-497.jar":      "0123456789",
		"BigWorld/region/r.0.0.mca": "this is some region data",
	})

	d := DetectServer(dir)

	if d.WorldSizeBytes == 0 {
		t.Error("WorldSizeBytes = 0, want the BigWorld directory to be measured")
	}
	if d.TotalSizeBytes <= d.WorldSizeBytes {
		t.Errorf("TotalSizeBytes (%d) should exceed WorldSizeBytes (%d)",
			d.TotalSizeBytes, d.WorldSizeBytes)
	}
}
