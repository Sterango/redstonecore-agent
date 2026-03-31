package maprender

import (
	"fmt"
	"os"
	"path/filepath"
)

// TileCache manages disk-cached map tiles
type TileCache struct {
	baseDir string
}

// NewTileCache creates a tile cache at the given directory
func NewTileCache(baseDir string) *TileCache {
	return &TileCache{baseDir: baseDir}
}

func (c *TileCache) tilePath(serverUUID, dimension string, x, z int) string {
	return filepath.Join(c.baseDir, serverUUID, dimension, fmt.Sprintf("%d_%d.png", x, z))
}

// Get returns a cached tile or nil if not found
func (c *TileCache) Get(serverUUID, dimension string, x, z int) ([]byte, bool) {
	path := c.tilePath(serverUUID, dimension, x, z)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores a tile in the cache
func (c *TileCache) Put(serverUUID, dimension string, x, z int, data []byte) error {
	path := c.tilePath(serverUUID, dimension, x, z)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0775); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Invalidate removes all cached tiles for a server
func (c *TileCache) Invalidate(serverUUID string) error {
	dir := filepath.Join(c.baseDir, serverUUID)
	return os.RemoveAll(dir)
}
