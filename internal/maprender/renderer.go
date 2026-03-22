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

// renderRelief3D renders with directional lighting, shadow casting, and cliff edges
// to create a dramatic 3D terrain effect.
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

	// Render with oblique displacement + directional lighting
	// The tile is taller to accommodate height displacement
	tileW := regionBlocks
	tileH := regionBlocks + 128 // extra space for height displacement
	img := image.NewRGBA(image.Rect(0, 0, tileW, tileH))

	// Height displacement scale: each Y level shifts up by 0.25 pixels
	heightScale := 0.25
	// Vertical offset so sea level (63) maps to the center of the tile
	baseOffset := 128 // pixels of extra space at the top

	// Render back to front (south to north) for painter's algorithm
	for z := regionBlocks - 1; z >= 0; z-- {
		for x := 0; x < regionBlocks; x++ {
			blockName := blocks[z][x]
			y := heights[z][x]

			if blockName == "minecraft:air" || blockName == "" {
				continue
			}

			c := GetBlockColor(blockName)

			// Calculate directional lighting (light from NW, 315 degrees)
			// Compare height with south and east neighbors for slope
			var slopeS, slopeE float64
			if z < regionBlocks-1 {
				slopeS = float64(y - heights[z+1][x])
			}
			if x < regionBlocks-1 {
				slopeE = float64(y - heights[z][x+1])
			}

			// NW light: slopes facing NW (higher to the south and east) are brighter
			lightFactor := (slopeS + slopeE) * 0.08
			lightFactor = clamp(lightFactor, -0.35, 0.35)

			// Apply ambient + directional light
			c = applyLighting(c, lightFactor)

			// Screen Y position with height displacement
			screenY := z + baseOffset - int(float64(y-63)*heightScale)
			if screenY < 0 || screenY >= tileH {
				continue
			}

			// Draw the top face
			img.Set(x, screenY, color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A})

			// Draw cliff face (vertical wall) if this block is higher than the one to the south
			if z < regionBlocks-1 {
				southY := heights[z+1][x]
				if y > southY {
					cliffHeight := float64(y-southY) * heightScale
					cliffColor := darken(c, 0.45) // cliff faces are much darker
					for dy := 1; dy <= int(cliffHeight)+1; dy++ {
						py := screenY + dy
						if py >= 0 && py < tileH {
							img.Set(x, py, color.NRGBA{R: cliffColor.R, G: cliffColor.G, B: cliffColor.B, A: cliffColor.A})
						}
					}
				}
			}

			// Cast shadow from NW: if a neighbor to the north-west is significantly higher,
			// darken this block
			if x > 0 && z > 0 {
				nwHeight := heights[z-1][x-1]
				if nwHeight > y+3 {
					shadowStrength := clamp(float64(nwHeight-y)*0.03, 0, 0.4)
					shadowColor := darken(c, shadowStrength)
					img.Set(x, screenY, color.NRGBA{R: shadowColor.R, G: shadowColor.G, B: shadowColor.B, A: shadowColor.A})
				}
			}
		}
	}

	// Crop the image to remove unused top space
	// Find the first non-transparent row
	cropTop := 0
	for row := 0; row < tileH; row++ {
		hasPixel := false
		for col := 0; col < tileW; col++ {
			_, _, _, a := img.At(col, row).RGBA()
			if a > 0 {
				hasPixel = true
				break
			}
		}
		if hasPixel {
			cropTop = row
			break
		}
	}

	// Create final 512x512 tile by mapping the content
	finalImg := image.NewRGBA(image.Rect(0, 0, regionBlocks, regionBlocks))
	srcOffset := cropTop
	for z := 0; z < regionBlocks; z++ {
		for x := 0; x < regionBlocks; x++ {
			srcY := z + srcOffset
			if srcY >= 0 && srcY < tileH {
				r, g, b, a := img.At(x, srcY).RGBA()
				finalImg.Set(x, z, color.NRGBA{
					R: uint8(r >> 8), G: uint8(g >> 8),
					B: uint8(b >> 8), A: uint8(a >> 8),
				})
			}
		}
	}

	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, finalImg); err != nil {
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
