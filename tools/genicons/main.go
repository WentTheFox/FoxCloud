// Command genicons renders the status SVGs under assets/status into the PNG
// and ICO files the tray embeds (internal/tray/icons). Run it from the
// repository root after changing an SVG:
//
//	cd tools/genicons && go run . ../..
//
// It is its own module so the SVG rasteriser does not become a dependency of
// FoxCloud itself.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// The SVGs draw with currentColor; the tray has no CSS to supply it, so it is
// substituted: pale for a dark panel, near-black for a light one.
var glyph = map[string]string{"dark": "#e8e8ef", "light": "#2b2b33"}

const size = 64

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genicons <repo root>")
		os.Exit(2)
	}
	root := os.Args[1]
	out := filepath.Join(root, "internal", "tray", "icons")
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	for theme, color := range glyph {
		svgs, _ := filepath.Glob(filepath.Join(root, "assets", "status", theme, "*.svg"))
		for _, svg := range svgs {
			name := strings.TrimSuffix(filepath.Base(svg), ".svg")
			pngBytes, err := render(svg, color)
			if err != nil {
				fatal(fmt.Errorf("%s: %w", svg, err))
			}
			base := filepath.Join(out, theme+"-"+name)
			must(os.WriteFile(base+".png", pngBytes, 0o644))
			must(os.WriteFile(base+".ico", ico(pngBytes), 0o644))
		}
	}
}

func render(path, color string) ([]byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	svg := strings.ReplaceAll(string(src), "currentColor", color)
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, size, size)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	icon.Draw(rasterx.NewDasher(size, size, rasterx.NewScannerGV(size, size, img, img.Bounds())), 1)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ico wraps one PNG in an ICO container, which Windows has accepted since Vista.
func ico(pngBytes []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type icon, count
	b.Write([]byte{size, size, 0, 0})                        // width, height, palette, reserved
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})   // planes, bits per pixel
	binary.Write(&b, binary.LittleEndian, uint32(len(pngBytes)))
	binary.Write(&b, binary.LittleEndian, uint32(22)) // offset: 6 header + 16 entry
	b.Write(pngBytes)
	return b.Bytes()
}

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genicons:", err)
	os.Exit(1)
}
