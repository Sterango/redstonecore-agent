package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/maprender"
)

// PollPlayerPositions reads player positions from playerdata NBT files
// and reports them to the cloud API.
func (a *Agent) PollPlayerPositions() {
	a.serversMu.RLock()
	type serverInfo struct {
		uuid, dataDir string
		isRunning     bool
		players       []string
	}
	var servers []serverInfo
	for uuid, srv := range a.servers {
		servers = append(servers, serverInfo{
			uuid:      uuid,
			dataDir:   srv.DataDir,
			isRunning: string(srv.Status) == "running",
			players:   srv.GetCurrentPlayers(),
		})
	}
	a.serversMu.RUnlock()

	for _, srv := range servers {
		if !srv.isRunning || len(srv.players) == 0 {
			continue
		}

		positions := readPlayerPositions(srv.dataDir, srv.players)
		if len(positions) == 0 {
			continue
		}

		err := a.client.ReportPlayerPositions(&api.PlayerPositionsRequest{
			ServerUUID: srv.uuid,
			Players:    positions,
		})
		if err != nil {
			log.Printf("[Positions] Failed to report positions for %s: %v", srv.uuid, err)
		}
	}
}

// usercacheEntry represents an entry in Minecraft's usercache.json
type usercacheEntry struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

// loadUsercache reads usercache.json to build a UUID→username mapping
func loadUsercache(serverDir string) map[string]string {
	path := filepath.Join(serverDir, "usercache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var entries []usercacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}

	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.UUID] = e.Name
	}
	return m
}

func readPlayerPositions(serverDir string, playerNames []string) []api.PlayerPositionData {
	// Build UUID→username map from usercache.json
	uuidToName := loadUsercache(serverDir)

	// Build a set of online player names (lowercase for case-insensitive matching)
	onlineSet := make(map[string]string, len(playerNames))
	for _, name := range playerNames {
		onlineSet[strings.ToLower(name)] = name
	}

	playerDataDir := filepath.Join(serverDir, "world", "playerdata")
	entries, err := os.ReadDir(playerDataDir)
	if err != nil {
		return nil
	}

	var positions []api.PlayerPositionData

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".dat") || strings.HasSuffix(name, ".dat_old") {
			continue
		}

		// Get UUID from filename (e.g., "20ddb852-c1c8-4e3f-84ad-5d27f812a5de.dat")
		fileUUID := strings.TrimSuffix(name, ".dat")

		// Look up username from usercache
		username, found := uuidToName[fileUUID]
		if !found {
			continue
		}

		// Check if this player is online
		originalName, isOnline := onlineSet[strings.ToLower(username)]
		if !isOnline {
			continue
		}

		// Read position from NBT
		filePath := filepath.Join(playerDataDir, name)
		pos, dimension := readPlayerPosition(filePath)
		if pos == nil {
			continue
		}

		positions = append(positions, api.PlayerPositionData{
			Username:  originalName,
			X:         pos[0],
			Y:         pos[1],
			Z:         pos[2],
			Dimension: dimension,
		})
	}

	return positions
}

func readPlayerPosition(path string) ([]float64, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}

	reader, err := maprender.NewNBTReader(data)
	if err != nil {
		return nil, ""
	}

	tagType, _, val, err := reader.ReadTag()
	if err != nil || tagType != 10 {
		return nil, ""
	}

	root, ok := val.(map[string]interface{})
	if !ok {
		return nil, ""
	}

	// Extract position: "Pos" is a TAG_List of 3 doubles
	var pos []float64
	if posRaw, ok := root["Pos"]; ok {
		if posList, ok := posRaw.([]interface{}); ok && len(posList) == 3 {
			pos = make([]float64, 3)
			for i, v := range posList {
				switch fv := v.(type) {
				case float64:
					pos[i] = fv
				case float32:
					pos[i] = float64(fv)
				}
			}
		}
	}

	// Extract dimension
	dimension := "overworld"
	if dimRaw, ok := root["Dimension"]; ok {
		if dimStr, ok := dimRaw.(string); ok {
			switch dimStr {
			case "minecraft:overworld":
				dimension = "overworld"
			case "minecraft:the_nether":
				dimension = "nether"
			case "minecraft:the_end":
				dimension = "end"
			default:
				dimension = dimStr
			}
		}
	}

	return pos, dimension
}
