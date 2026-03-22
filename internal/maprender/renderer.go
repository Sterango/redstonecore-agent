package maprender

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// RenderRegionTile renders a region as a 512x512 PNG tile (1 block = 1 pixel)
// mode: "flat" for top-down, "3d" for relief-shaded 3D look
func RenderRegionTile(region *Region, mode string) ([]byte, error) {
	if mode == "3d" {
		return renderRelief3D(region)
	}
	return renderFlat(region)
}

func renderFlat(region *Region) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, regionBlocks, regionBlocks))

	for z := 0; z < regionBlocks; z++ {
		for x := 0; x < regionBlocks; x++ {
			blockName, y := region.TopBlock(x, z)

			if blockName == "minecraft:air" || blockName == "" {
				img.SetRGBA(x, z, color.RGBA{0, 0, 0, 0})
				continue
			}

			c := GetBlockColor(blockName)
			c = applyHeightShading(c, y)
			img.Set(x, z, color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A})
		}
	}

	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderRelief3D renders with directional lighting, shadow casting, and edge highlights
// to create a dramatic 3D terrain effect. No pixel displacement — tiles align perfectly.
func renderRelief3D(region *Region) ([]byte, error) {
	// First pass: build heightmap and block name map
	heights := [regionBlocks][regionBlocks]int{}
	blocks := [regionBlocks][regionBlocks]string{}

	for z := 0; z < regionBlocks; z++ {
		for x := 0; x < regionBlocks; x++ {
			name, y := region.TopBlock(x, z)
			heights[z][x] = y
			blocks[z][x] = name
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, regionBlocks, regionBlocks))

	for z := 0; z < regionBlocks; z++ {
		for x := 0; x < regionBlocks; x++ {
			blockName := blocks[z][x]
			y := heights[z][x]

			if blockName == "minecraft:air" || blockName == "" {
				continue
			}

			c := GetBlockColor(blockName)

			// Directional lighting from NW (light hits north-west facing slopes)
			var slopeS, slopeE, slopeN, slopeW float64
			if z < regionBlocks-1 {
				slopeS = float64(y - heights[z+1][x])
			}
			if x < regionBlocks-1 {
				slopeE = float64(y - heights[z][x+1])
			}
			if z > 0 {
				slopeN = float64(y - heights[z-1][x])
			}
			if x > 0 {
				slopeW = float64(y - heights[z][x-1])
			}

			// NW illumination: bright where terrain faces NW (south+east slopes positive)
			lightFactor := (slopeS + slopeE) * 0.1
			lightFactor = clamp(lightFactor, -0.4, 0.4)
			c = applyLighting(c, lightFactor)

			// Edge darkening: draw dark edges where height drops sharply to south or east
			// This creates the visible "cliff lines" that give depth
			if slopeS > 2 {
				edgeDarken := clamp(slopeS*0.04, 0, 0.5)
				c = darken(c, edgeDarken)
			}
			if slopeE > 2 {
				edgeDarken := clamp(slopeE*0.03, 0, 0.4)
				c = darken(c, edgeDarken)
			}

			// Highlight edges facing the light (north and west drops = lit cliff top)
			if slopeN > 2 {
				edgeBright := clamp(slopeN*0.03, 0, 0.3)
				c = applyLighting(c, edgeBright)
			}
			if slopeW > 2 {
				edgeBright := clamp(slopeW*0.02, 0, 0.2)
				c = applyLighting(c, edgeBright)
			}

			// Shadow casting: blocks to the NW that are taller cast shadows SE
			if x > 0 && z > 0 {
				nwHeight := heights[z-1][x-1]
				if nwHeight > y+3 {
					shadowStrength := clamp(float64(nwHeight-y)*0.025, 0, 0.35)
					c = darken(c, shadowStrength)
				}
			}
			// Extended shadow: check 2 blocks NW for longer shadows
			if x > 1 && z > 1 {
				nw2Height := heights[z-2][x-2]
				if nw2Height > y+6 {
					shadowStrength := clamp(float64(nw2Height-y)*0.015, 0, 0.2)
					c = darken(c, shadowStrength)
				}
			}

			// Subtle ambient occlusion: if surrounded by taller blocks, darken slightly
			taller := 0
			if z > 0 && heights[z-1][x] > y {
				taller++
			}
			if z < regionBlocks-1 && heights[z+1][x] > y {
				taller++
			}
			if x > 0 && heights[z][x-1] > y {
				taller++
			}
			if x < regionBlocks-1 && heights[z][x+1] > y {
				taller++
			}
			if taller >= 3 {
				c = darken(c, 0.1)
			}

			img.Set(x, z, color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A})
		}
	}

	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func applyLighting(c color.RGBA, lightFactor float64) color.RGBA {
	return color.RGBA{
		R: clampByte(float64(c.R) * (1 + lightFactor)),
		G: clampByte(float64(c.G) * (1 + lightFactor)),
		B: clampByte(float64(c.B) * (1 + lightFactor)),
		A: c.A,
	}
}

func darken(c color.RGBA, amount float64) color.RGBA {
	return color.RGBA{
		R: clampByte(float64(c.R) * (1 - amount)),
		G: clampByte(float64(c.G) * (1 - amount)),
		B: clampByte(float64(c.B) * (1 - amount)),
		A: c.A,
	}
}

// applyHeightShading adjusts color brightness based on Y level
func applyHeightShading(c color.RGBA, y int) color.RGBA {
	factor := float64(y-63) / 256.0
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

// Ensure math import is used
var _ = math.Floor
