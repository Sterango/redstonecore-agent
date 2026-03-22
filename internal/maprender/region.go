package maprender

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	sectorSize     = 4096
	chunksPerAxis  = 32
	blocksPerChunk = 16
	regionBlocks   = chunksPerAxis * blocksPerChunk // 512
)

// Region represents a parsed .mca region file
type Region struct {
	X, Z   int
	chunks [chunksPerAxis][chunksPerAxis]*chunk
}

type chunk struct {
	sections []*section
	minY     int
}

type section struct {
	y       int8
	palette []string // block names
	states  []int64  // packed block state indices
	bitsPerEntry int
}

// ParseRegion parses an .mca file and returns a Region
func ParseRegion(data []byte, rx, rz int) (*Region, error) {
	if len(data) < sectorSize*2 {
		return nil, fmt.Errorf("region file too small: %d bytes", len(data))
	}

	region := &Region{X: rx, Z: rz}

	// Read chunk offset table (first 4096 bytes)
	for cz := 0; cz < chunksPerAxis; cz++ {
		for cx := 0; cx < chunksPerAxis; cx++ {
			idx := 4 * (cx + cz*chunksPerAxis)
			offset := int(data[idx])<<16 | int(data[idx+1])<<8 | int(data[idx+2])
			sectors := int(data[idx+3])

			if offset == 0 || sectors == 0 {
				continue // Chunk not present
			}

			chunkStart := offset * sectorSize
			if chunkStart+5 > len(data) {
				continue
			}

			// Read chunk header
			chunkLen := int(binary.BigEndian.Uint32(data[chunkStart : chunkStart+4]))
			compressionType := data[chunkStart+4]

			chunkData := data[chunkStart+5 : chunkStart+4+chunkLen]
			if len(chunkData) == 0 {
				continue
			}

			c, err := parseChunk(chunkData, compressionType)
			if err != nil {
				continue // Skip malformed chunks
			}
			region.chunks[cz][cx] = c
		}
	}

	return region, nil
}

// TopBlock returns the name and Y of the highest non-air block at (x, z) within the region
// x and z are 0-511 (region-local coordinates)
func (r *Region) TopBlock(x, z int) (string, int) {
	cx := x / blocksPerChunk
	cz := z / blocksPerChunk
	localX := x % blocksPerChunk
	localZ := z % blocksPerChunk

	if cx >= chunksPerAxis || cz >= chunksPerAxis {
		return "minecraft:air", 0
	}

	c := r.chunks[cz][cx]
	if c == nil {
		return "minecraft:air", 0
	}

	// Scan from top section down
	for i := len(c.sections) - 1; i >= 0; i-- {
		sec := c.sections[i]
		if sec == nil || len(sec.palette) == 0 {
			continue
		}

		// Check all 16 Y levels in this section, top down
		for ly := 15; ly >= 0; ly-- {
			blockIdx := getBlockIndex(sec, localX, ly, localZ)
			if blockIdx < 0 || blockIdx >= len(sec.palette) {
				continue
			}
			name := sec.palette[blockIdx]
			if name != "minecraft:air" && name != "minecraft:cave_air" && name != "minecraft:void_air" {
				worldY := int(sec.y)*16 + ly
				return name, worldY
			}
		}
	}

	return "minecraft:air", 0
}

// HasChunk returns true if the chunk at (cx, cz) exists
func (r *Region) HasChunk(cx, cz int) bool {
	if cx < 0 || cx >= chunksPerAxis || cz < 0 || cz >= chunksPerAxis {
		return false
	}
	return r.chunks[cz][cx] != nil
}

func parseChunk(data []byte, compression byte) (*chunk, error) {
	var reader io.Reader

	switch compression {
	case 1: // GZip
		// Rarely used but handle it
		return nil, fmt.Errorf("gzip chunks not supported")
	case 2: // Zlib
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		reader = zr
	case 3: // Uncompressed (since 1.15.1)
		reader = bytes.NewReader(data)
	default:
		return nil, fmt.Errorf("unknown compression: %d", compression)
	}

	// Read all decompressed data
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	nbt, err := NewNBTReader(decompressed)
	if err != nil {
		return nil, err
	}

	// Read root compound tag
	tagType, _, _, err := nbt.ReadTag()
	if err != nil {
		return nil, err
	}
	if tagType != tagCompound {
		return nil, fmt.Errorf("expected compound, got %d", tagType)
	}

	root, err := nbt.ReadCompound()
	if err != nil {
		return nil, err
	}

	return parseChunkNBT(root)
}

func parseChunkNBT(root map[string]interface{}) (*chunk, error) {
	c := &chunk{}

	// Post-1.18 format: sections are at root level
	sectionsRaw, ok := root["sections"]
	if !ok {
		return c, nil
	}

	sectionsList, ok := sectionsRaw.([]interface{})
	if !ok {
		return c, nil
	}

	for _, secRaw := range sectionsList {
		secMap, ok := secRaw.(map[string]interface{})
		if !ok {
			continue
		}

		sec := parseSection(secMap)
		if sec != nil {
			c.sections = append(c.sections, sec)
		}
	}

	// Sort sections by Y (they should already be sorted but ensure it)
	for i := 0; i < len(c.sections); i++ {
		for j := i + 1; j < len(c.sections); j++ {
			if c.sections[i].y > c.sections[j].y {
				c.sections[i], c.sections[j] = c.sections[j], c.sections[i]
			}
		}
	}

	return c, nil
}

func parseSection(secMap map[string]interface{}) *section {
	// Get Y value
	yVal, ok := secMap["Y"]
	if !ok {
		return nil
	}
	var y int8
	switch v := yVal.(type) {
	case byte:
		y = int8(v)
	case int32:
		y = int8(v)
	default:
		return nil
	}

	// Get block_states
	blockStates, ok := secMap["block_states"]
	if !ok {
		return nil
	}
	bsMap, ok := blockStates.(map[string]interface{})
	if !ok {
		return nil
	}

	// Get palette
	paletteRaw, ok := bsMap["palette"]
	if !ok {
		return nil
	}
	paletteList, ok := paletteRaw.([]interface{})
	if !ok || len(paletteList) == 0 {
		return nil
	}

	palette := make([]string, len(paletteList))
	allAir := true
	for i, p := range paletteList {
		pMap, ok := p.(map[string]interface{})
		if !ok {
			palette[i] = "minecraft:air"
			continue
		}
		name, _ := pMap["Name"].(string)
		palette[i] = name
		if name != "minecraft:air" && name != "minecraft:cave_air" && name != "minecraft:void_air" {
			allAir = false
		}
	}

	if allAir {
		return nil // Skip entirely air sections
	}

	sec := &section{
		y:       y,
		palette: palette,
	}

	// If palette has only 1 entry, no data array needed (entire section is that block)
	if len(palette) == 1 {
		sec.bitsPerEntry = 0
		return sec
	}

	// Get data (long array of packed block states)
	dataRaw, ok := bsMap["data"]
	if !ok {
		return sec
	}
	dataLongs, ok := dataRaw.([]int64)
	if !ok {
		return sec
	}

	sec.states = dataLongs

	// Calculate bits per entry
	// 4096 blocks per section (16x16x16), packed into int64 array
	bitsPerEntry := len(dataLongs) * 64 / 4096
	if bitsPerEntry < 4 {
		bitsPerEntry = 4
	}
	sec.bitsPerEntry = bitsPerEntry

	return sec
}

func getBlockIndex(sec *section, x, y, z int) int {
	if len(sec.palette) <= 1 {
		return 0
	}
	if len(sec.states) == 0 {
		return 0
	}

	bitsPerEntry := sec.bitsPerEntry
	if bitsPerEntry == 0 {
		return 0
	}

	blockIndex := (y*16+z)*16 + x
	entriesPerLong := 64 / bitsPerEntry
	if entriesPerLong == 0 {
		return 0
	}

	longIndex := blockIndex / entriesPerLong
	bitOffset := (blockIndex % entriesPerLong) * bitsPerEntry

	if longIndex >= len(sec.states) {
		return 0
	}

	mask := int64((1 << bitsPerEntry) - 1)
	value := int((sec.states[longIndex] >> bitOffset) & mask)

	if value >= len(sec.palette) {
		return 0
	}
	return value
}
