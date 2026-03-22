package maprender

import (
	"archive/zip"
	"image"
	"image/color"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	textureColors     map[string]color.RGBA
	textureColorsOnce sync.Once
)

// BuildTextureColorMap scans all JARs in the server directory for block textures
// and builds a color map by averaging each texture's pixels.
// Results are cached in memory for the lifetime of the process.
func BuildTextureColorMap(serverDir string) map[string]color.RGBA {
	textureColorsOnce.Do(func() {
		textureColors = make(map[string]color.RGBA)
		jarPaths := findJars(serverDir)
		log.Printf("[Map] Scanning %d JARs for block textures...", len(jarPaths))

		for _, jarPath := range jarPaths {
			extractTexturesFromJar(jarPath, textureColors)
		}

		log.Printf("[Map] Extracted %d block colors from textures", len(textureColors))
	})
	return textureColors
}

// ResetTextureCache clears the cached texture colors (e.g., after mod changes)
func ResetTextureCache() {
	textureColorsOnce = sync.Once{}
	textureColors = nil
}

func findJars(serverDir string) []string {
	var jars []string

	// Check mods/ directory
	modsDir := filepath.Join(serverDir, "mods")
	entries, err := os.ReadDir(modsDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
				jars = append(jars, filepath.Join(modsDir, e.Name()))
			}
		}
	}

	// Check server.jar in root
	serverJar := filepath.Join(serverDir, "server.jar")
	if _, err := os.Stat(serverJar); err == nil {
		jars = append(jars, serverJar)
	}

	// Check libraries for minecraft client/server JARs
	libDir := filepath.Join(serverDir, "libraries")
	filepath.Walk(libDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".jar") {
			jars = append(jars, path)
		}
		return nil
	})

	return jars
}

func extractTexturesFromJar(jarPath string, colors map[string]color.RGBA) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return
	}
	defer zr.Close()

	for _, f := range zr.File {
		// Match: assets/{namespace}/textures/block/{name}.png
		if !strings.Contains(f.Name, "textures/block/") || !strings.HasSuffix(f.Name, ".png") {
			continue
		}

		parts := strings.Split(f.Name, "/")
		// Expected: assets / {namespace} / textures / block / {name}.png
		if len(parts) < 5 || parts[0] != "assets" || parts[2] != "textures" || parts[3] != "block" {
			continue
		}

		namespace := parts[1]
		textureName := strings.TrimSuffix(parts[len(parts)-1], ".png")

		// Handle subdirectories (e.g., assets/minecraft/textures/block/copper/cut_copper.png)
		// Just use the filename for simplicity
		blockName := namespace + ":" + textureName

		// Prefer _top texture for top-down view
		if strings.HasSuffix(textureName, "_top") {
			baseName := namespace + ":" + strings.TrimSuffix(textureName, "_top")
			avgColor := averageTextureColor(f)
			if avgColor.A > 0 {
				colors[baseName] = avgColor
			}
			continue
		}

		// Skip _side, _bottom, _front, _back variants
		for _, suffix := range []string{"_side", "_bottom", "_front", "_back", "_inner", "_outer", "_left", "_right", "_on", "_off"} {
			if strings.HasSuffix(textureName, suffix) {
				goto skip
			}
		}

		// Don't override if a _top variant was already added
		if _, exists := colors[blockName]; !exists {
			avgColor := averageTextureColor(f)
			if avgColor.A > 0 {
				colors[blockName] = avgColor
			}
		}
	skip:
	}
}

func averageTextureColor(f *zip.File) color.RGBA {
	rc, err := f.Open()
	if err != nil {
		return color.RGBA{}
	}
	defer rc.Close()

	img, _, err := image.Decode(rc)
	if err != nil {
		return color.RGBA{}
	}

	bounds := img.Bounds()
	var rTotal, gTotal, bTotal, aTotal uint64
	var count uint64

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// Only count non-transparent pixels
			if a > 0 {
				// RGBA values are pre-multiplied and 16-bit, convert to 8-bit
				rTotal += uint64(r >> 8)
				gTotal += uint64(g >> 8)
				bTotal += uint64(b >> 8)
				aTotal += uint64(a >> 8)
				count++
			}
		}
	}

	if count == 0 {
		return color.RGBA{}
	}

	return color.RGBA{
		R: uint8(rTotal / count),
		G: uint8(gTotal / count),
		B: uint8(bTotal / count),
		A: uint8(aTotal / count),
	}
}
