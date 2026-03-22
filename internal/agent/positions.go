package agent

import (
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
		uuid, dataDir, status string
		players               []string
	}
	var servers []serverInfo
	for uuid, srv := range a.servers {
		servers = append(servers, serverInfo{
			uuid:    uuid,
			dataDir: srv.DataDir,
			status:  srv.Status,
			players: srv.GetCurrentPlayers(),
		})
	}
	a.serversMu.RUnlock()

	for _, srv := range servers {
		if srv.status != "running" || len(srv.players) == 0 {
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

func readPlayerPositions(serverDir string, playerNames []string) []api.PlayerPositionData {
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

		filePath := filepath.Join(playerDataDir, name)
		pos, username, dimension := readPlayerNBT(filePath)
		if pos == nil || username == "" {
			continue
		}

		// Only include online players
		isOnline := false
		for _, pn := range playerNames {
			if strings.EqualFold(pn, username) {
				isOnline = true
				break
			}
		}
		if !isOnline {
			continue
		}

		positions = append(positions, api.PlayerPositionData{
			Username:  username,
			X:         pos[0],
			Y:         pos[1],
			Z:         pos[2],
			Dimension: dimension,
		})
	}

	return positions
}

func readPlayerNBT(path string) ([]float64, string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", ""
	}

	reader, err := maprender.NewNBTReader(data)
	if err != nil {
		return nil, "", ""
	}

	tagType, _, val, err := reader.ReadTag()
	if err != nil || tagType != 10 {
		return nil, "", ""
	}

	root, ok := val.(map[string]interface{})
	if !ok {
		return nil, "", ""
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

	// Extract username
	var username string
	// Bukkit/Spigot/Paper
	if bukkit, ok := root["bukkit"]; ok {
		if bMap, ok := bukkit.(map[string]interface{}); ok {
			if name, ok := bMap["lastKnownName"].(string); ok {
				username = name
			}
		}
	}
	if username == "" {
		if name, ok := root["lastKnownName"].(string); ok {
			username = name
		}
	}
	// NeoForge/Forge
	if username == "" {
		if name, ok := root["PlayerName"].(string); ok {
			username = name
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

	return pos, username, dimension
}
