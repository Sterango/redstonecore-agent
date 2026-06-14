package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/gameserver"
)

// ===========================================================================
// Config-driven game panels (web side: config/game_panels.php).
//
// These handle the generic update_game_config / game_action / install_game_mod /
// remove_game_mod commands for LinuxGSM games and report {game}_* status props
// back via SyncProperties (the same channel Satisfactory uses for sat_*).
//
// FRAMEWORK SLICE: settings that the LinuxGSM <name>.cfg already understands
// (server name, max players) flow into the config + a recreate. The full settings
// set is also persisted to rsc-game-settings.json so the deep per-game writer can
// translate it into each game's real config file later (server.cfg /
// PalWorldSettings.ini / serverconfig.xml / …) — that translation is the TODO.
// Mod install/remove maintain a manifest and push {game}_mods so the panel
// reflects them; the actual per-source download/extract is also a TODO.
// ===========================================================================

const (
	gameSettingsFile = "rsc-game-settings.json"
	gameModsFile     = "rsc-mods.json"
)

type gameModEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
}

type gameConfigFile struct {
	Label string `json:"label"`
	Path  string `json:"path"` // relative to the server's /data root (for servers.files.read)
}

// configGlobs lists best-effort candidate config-file patterns per game, relative
// to the LinuxGSM /data root. Discovery scans the real filesystem, so wrong guesses
// simply yield nothing. The agent-managed lgsm/config-lgsm cfg is intentionally
// excluded (it's overwritten on recreate).
var configGlobs = map[string][]string{
	"rust": {
		"serverfiles/server/*/cfg/server.cfg",
		"serverfiles/oxide/config/*.json",
		"serverfiles/carbon/configs/*.json",
	},
	"valheim":            {"serverfiles/BepInEx/config/*.cfg"},
	"palworld":           {"serverfiles/Pal/Saved/Config/LinuxServer/*.ini"},
	"7daystodie":         {"serverfiles/serverconfig.xml"},
	"projectzomboid":     {"serverfiles/Zomboid/Server/*.ini", "Zomboid/Server/*.ini"},
	"cs2":                {"serverfiles/game/csgo/cfg/server.cfg", "serverfiles/game/cs2/cfg/server.cfg"},
	"corekeeper":         {"serverfiles/*.ini"},
	"dontstarvetogether": {"serverfiles/*/cluster.ini", "serverfiles/*/*/server.ini"},
	"terraria":           {"serverfiles/serverconfig.txt"},
}

// updateGameConfig applies schema-driven settings. name/max-players pin into the
// LinuxGSM config (via recreate when required); the full set is persisted for the
// deep per-game config writer.
func (a *Agent) updateGameConfig(cmd api.Command, gs *gameserver.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("update_game_config requires payload")
	}
	settings, _ := cmd.Payload["settings"].(map[string]interface{})
	restart, _ := cmd.Payload["restart_required"].(bool)

	// Fields the LinuxGSM <name>.cfg already understands.
	if v, ok := settings["server_name"].(string); ok && v != "" {
		gs.Name = v
	}
	if v, ok := settings["max_players"].(float64); ok && int(v) > 0 {
		gs.MaxPlayers = int(v)
	}

	// Persist everything for the per-game config writer (TODO: translate these into
	// each game's real config file).
	a.writeGameJSON(gs.DataDir, gameSettingsFile, settings)
	log.Printf("[GamePanel] %s: stored %d settings (deep config-file write is TODO)", gs.Game, len(settings))

	a.writeGameMeta(filepath.Dir(gs.DataDir), gameMeta{
		Name: gs.Name, Game: gs.Game, BasePort: gs.BasePort,
		MaxPlayers: gs.MaxPlayers, AllocatedRAM: gs.AllocatedRAM,
	})

	if restart {
		if err := gs.Recreate(); err != nil {
			return err
		}
	}
	a.pushGameState(cmd.ServerUUID, gs)
	return nil
}

// gameAction runs a game-specific action. force_update is generic (LinuxGSM
// update); save / wipe_map / change_map are per-game and stubbed for now.
func (a *Agent) gameAction(cmd api.Command, gs *gameserver.Server) error {
	action, _ := cmd.Payload["action"].(string)
	switch action {
	case "force_update":
		out, err := gs.RunLGSM("update")
		if err != nil {
			return fmt.Errorf("update: %v: %s", err, out)
		}
		return nil
	default:
		log.Printf("[GamePanel] %s: action %q not yet implemented (TODO)", gs.Game, action)
		return nil
	}
}

// installGameMod records a mod in the per-server manifest and reflects it in the
// panel. TODO: actually download/extract per source (Thunderstore / uMod / …).
func (a *Agent) installGameMod(cmd api.Command, gs *gameserver.Server) error {
	id, _ := cmd.Payload["mod_id"].(string)
	if id == "" {
		return fmt.Errorf("mod_id is required")
	}
	name, _ := cmd.Payload["mod_name"].(string)
	if name == "" {
		name = id
	}
	version, _ := cmd.Payload["version"].(string)
	source, _ := cmd.Payload["source"].(string)

	mods := a.readGameMods(gs.DataDir)
	entry := gameModEntry{ID: id, Name: name, Version: version, Source: source}
	replaced := false
	for i := range mods {
		if mods[i].ID == id {
			mods[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		mods = append(mods, entry)
	}
	a.writeGameJSON(gs.DataDir, gameModsFile, mods)
	log.Printf("[GamePanel] %s: recorded mod %q from %s (download/extract is TODO)", gs.Game, id, source)
	a.pushGameState(cmd.ServerUUID, gs)
	return nil
}

// removeGameMod drops a mod from the manifest and refreshes the panel.
func (a *Agent) removeGameMod(cmd api.Command, gs *gameserver.Server) error {
	id, _ := cmd.Payload["mod_id"].(string)
	if id == "" {
		return fmt.Errorf("mod_id is required")
	}
	mods := a.readGameMods(gs.DataDir)
	kept := make([]gameModEntry, 0, len(mods))
	for _, m := range mods {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	a.writeGameJSON(gs.DataDir, gameModsFile, kept)
	a.pushGameState(cmd.ServerUUID, gs)
	return nil
}

// pushGameState reports {game}_* status props (installed mods, plus live players /
// map from A2S where the game supports it) so the panel's status card stays fresh.
func (a *Agent) pushGameState(uuid string, gs *gameserver.Server) {
	g := gs.Game
	props := map[string]string{}

	if data, err := json.Marshal(a.readGameMods(gs.DataDir)); err == nil {
		props[g+"_mods"] = string(data)
	}

	// Editable config files discovered on disk → {game}_config_files (panel editor).
	if data, err := json.Marshal(a.discoverGameConfigs(gs)); err == nil {
		props[g+"_config_files"] = string(data)
	}

	spec := gs.Spec()
	if spec.QueryOffset >= 0 {
		if info, err := gameserver.QueryA2S(fmt.Sprintf("127.0.0.1:%d", gs.BasePort+spec.QueryOffset)); err == nil {
			props[g+"_players_online"] = fmt.Sprintf("%d", info.Players)
			if info.Map != "" {
				props[g+"_map"] = info.Map
			}
		}
	}

	if err := a.client.SyncProperties(&api.PropertiesRequest{ServerUUID: uuid, Properties: props}); err != nil {
		log.Printf("[GamePanel] failed to push state for %s: %v", uuid, err)
	}
}

// --- manifest helpers ---

func (a *Agent) writeGameJSON(dataDir, file string, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dataDir, file), data, 0644); err != nil {
		log.Printf("Warning: failed to write %s: %v", file, err)
	}
}

func (a *Agent) readGameMods(dataDir string) []gameModEntry {
	data, err := os.ReadFile(filepath.Join(dataDir, gameModsFile))
	if err != nil {
		return []gameModEntry{}
	}
	var mods []gameModEntry
	if json.Unmarshal(data, &mods) != nil {
		return []gameModEntry{}
	}
	return mods
}

// discoverGameConfigs scans the game's real config locations and returns the files
// that exist (paths relative to /data, for servers.files.read/write). Best-effort
// per game; missing files simply aren't returned.
func (a *Agent) discoverGameConfigs(gs *gameserver.Server) []gameConfigFile {
	seen := map[string]bool{}
	files := []gameConfigFile{}
	for _, pat := range configGlobs[gs.Game] {
		matches, err := filepath.Glob(filepath.Join(gs.DataDir, pat))
		if err != nil {
			continue
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || info.IsDir() {
				continue
			}
			rel, err := filepath.Rel(gs.DataDir, m)
			if err != nil || seen[rel] {
				continue
			}
			seen[rel] = true
			files = append(files, gameConfigFile{Label: filepath.Base(rel), Path: rel})
			if len(files) >= 50 {
				return files
			}
		}
	}
	return files
}
