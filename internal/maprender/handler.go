package maprender

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	cache     *TileCache
	cacheOnce sync.Once
	// Limit concurrent renders to prevent memory spikes
	renderSem = make(chan struct{}, 4)
)

func getCache(dataDir string) *TileCache {
	cacheOnce.Do(func() {
		cacheDir := filepath.Join(dataDir, "cache", "map-tiles")
		cache = NewTileCache(cacheDir)
	})
	return cache
}

// dimensionPath returns the region directory for a given dimension
func dimensionPath(serverDir, dimension string) string {
	switch dimension {
	case "nether", "the_nether":
		// Check for both Vanilla and Bukkit/Spigot/Paper paths
		netherPath := filepath.Join(serverDir, "world", "DIM-1", "region")
		if _, err := os.Stat(netherPath); err == nil {
			return netherPath
		}
		return filepath.Join(serverDir, "world_nether", "DIM-1", "region")
	case "end", "the_end":
		endPath := filepath.Join(serverDir, "world", "DIM1", "region")
		if _, err := os.Stat(endPath); err == nil {
			return endPath
		}
		return filepath.Join(serverDir, "world_the_end", "DIM1", "region")
	default: // overworld
		return filepath.Join(serverDir, "world", "region")
	}
}

// HandleMapTile renders a single region tile and returns it as base64 PNG
func HandleMapTile(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	dimension, _ := params["dimension"].(string)
	if dimension == "" {
		dimension = "overworld"
	}

	xf, _ := params["x"].(float64)
	zf, _ := params["z"].(float64)
	x := int(xf)
	z := int(zf)

	tc := getCache(dataDir)

	// Check cache
	if data, ok := tc.Get(serverUUID, dimension, x, z); ok {
		return map[string]interface{}{
			"content": base64.StdEncoding.EncodeToString(data),
			"cached":  true,
		}
	}

	// Acquire render semaphore
	renderSem <- struct{}{}
	defer func() { <-renderSem }()

	// Read region file
	regionDir := dimensionPath(serverDir, dimension)
	regionFile := filepath.Join(regionDir, fmt.Sprintf("r.%d.%d.mca", x, z))

	data, err := os.ReadFile(regionFile)
	if err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("region file not found: r.%d.%d.mca", x, z),
		}
	}

	// Parse region
	region, err := ParseRegion(data, x, z)
	if err != nil {
		log.Printf("[Map] Failed to parse region r.%d.%d.mca: %v", x, z, err)
		return map[string]interface{}{
			"error": fmt.Sprintf("parse error: %v", err),
		}
	}

	// Render tile
	pngData, err := RenderRegionTile(region)
	if err != nil {
		log.Printf("[Map] Failed to render region r.%d.%d.mca: %v", x, z, err)
		return map[string]interface{}{
			"error": fmt.Sprintf("render error: %v", err),
		}
	}

	// Cache the tile
	if cacheErr := tc.Put(serverUUID, dimension, x, z, pngData); cacheErr != nil {
		log.Printf("[Map] Failed to cache tile: %v", cacheErr)
	}

	return map[string]interface{}{
		"content": base64.StdEncoding.EncodeToString(pngData),
		"cached":  false,
	}
}

// HandleMapRegions returns a list of available region coordinates
func HandleMapRegions(serverDir string, params map[string]interface{}) map[string]interface{} {
	dimension, _ := params["dimension"].(string)
	if dimension == "" {
		dimension = "overworld"
	}

	regionDir := dimensionPath(serverDir, dimension)

	entries, err := os.ReadDir(regionDir)
	if err != nil {
		return map[string]interface{}{
			"regions": []interface{}{},
		}
	}

	re := regexp.MustCompile(`^r\.(-?\d+)\.(-?\d+)\.mca$`)
	var regions []interface{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := re.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}

		rx, _ := strconv.Atoi(matches[1])
		rz, _ := strconv.Atoi(matches[2])

		info, _ := entry.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		regions = append(regions, map[string]interface{}{
			"x":    rx,
			"z":    rz,
			"size": size,
		})
	}

	if regions == nil {
		regions = []interface{}{}
	}

	return map[string]interface{}{
		"regions": regions,
	}
}

// HandleMapInvalidate clears the tile cache for a server
func HandleMapInvalidate(serverUUID, dataDir string) map[string]interface{} {
	tc := getCache(dataDir)
	if err := tc.Invalidate(serverUUID); err != nil {
		return map[string]interface{}{
			"error": err.Error(),
		}
	}
	return map[string]interface{}{
		"cleared": true,
	}
}

// getWorldFolder reads the level-name from server.properties, defaults to "world"
func GetWorldFolder(serverDir string) string {
	propsPath := filepath.Join(serverDir, "server.properties")
	data, err := os.ReadFile(propsPath)
	if err != nil {
		return "world"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "level-name=") {
			val := strings.TrimPrefix(line, "level-name=")
			val = strings.TrimSpace(val)
			if val != "" {
				return val
			}
		}
	}
	return "world"
}
