package maprender

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
)

// RenderRegionTile renders a region as a 512x512 PNG tile (1 block = 1 pixel)
func RenderRegionTile(region *Region) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, regionBlocks, regionBlocks))
	debugWater := true

	for z := 0; z < regionBlocks; z++ {
		for x := 0; x < regionBlocks; x++ {
			blockName, y := region.TopBlock(x, z)

			if blockName == "minecraft:air" || blockName == "" {
				// Transparent for ungenerated areas
				img.SetRGBA(x, z, color.RGBA{0, 0, 0, 0})
				continue
			}

			c := GetBlockColor(blockName)
			if debugWater && blockName == "minecraft:water" && x == 0 && z == 0 {
				log.Printf("[Map] DEBUG water color: RGBA(%d,%d,%d,%d) for %s at y=%d", c.R, c.G, c.B, c.A, blockName, y)
				debugWater = false
			}
			c = applyHeightShading(c, y)
			img.SetRGBA(x, z, c)
		}
	}

	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// applyHeightShading adjusts color brightness based on Y level
// Sea level is ~63, max build height ~320, min is -64
func applyHeightShading(c color.RGBA, y int) color.RGBA {
	// Normalize Y to a -1.0 to 1.0 range around sea level (63)
	// Below sea level: darker, above: brighter
	factor := float64(y-63) / 256.0 // subtle shading
	factor = clamp(factor, -0.3, 0.3)

	return color.RGBA{
		R: clampByte(float64(c.R) * (1 + factor)),
		G: clampByte(float64(c.G) * (1 + factor)),
		B: clampByte(float64(c.B) * (1 + factor)),
		A: c.A,
	}
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
