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

// icoFrames are the sizes Windows picks from: 16 px at 100% display scaling, 20
// at 125%, 24 at 150%, 32 at 200%. Each is drawn at its own size, not scaled
// down from a big one, with a stroke (in the SVG's 24-unit space) heavy enough
// to stay about 2 px wide at the small sizes.
// icoCrop is how many of the SVG's 24 units to trim off each edge. Lucide
// leaves a margin that is a fifth of a 16 px tray icon; the artwork stays
// inside 2..22, so one unit is safe to lose.
const icoCrop = 0.5

var icoFrames = []struct {
	size   int
	stroke string
}{{16, "2.1"}, {20, "2.3"}, {24, "2.4"}, {32, "2.3"}, {48, "2.3"}, {64, "2.3"}}

// The PNGs are scaled down by the Linux tray and the Plasma widget to about
// 22 px, where Lucide's stroke of 2 (8% of the width) blurs into faint lines
// beside the panel's pixel-aligned icons. They are drawn heavier. The ICO is a
// single 64 px image that Windows scales down to 16-24 px, so it needs the same
// weight or the strokes come out hairline thin.
const pngStroke = "3"

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
			pngBytes, err := render(svg, color, pngStroke, size, 0)
			if err != nil {
				fatal(fmt.Errorf("%s: %w", svg, err))
			}
			var frames [][]byte
			for _, f := range icoFrames {
				b, err := render(svg, color, f.stroke, f.size, icoCrop)
				if err != nil {
					fatal(fmt.Errorf("%s: %w", svg, err))
				}
				frames = append(frames, b)
			}
			base := filepath.Join(out, theme+"-"+name)
			must(os.WriteFile(base+".png", pngBytes, 0o644))
			must(os.WriteFile(base+".ico", ico(frames), 0o644))
		}
	}
}

func render(path, color, stroke string, size int, crop float64) ([]byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	svg := strings.ReplaceAll(string(src), "currentColor", color)
	svg = strings.ReplaceAll(svg, `stroke-width="2"`, `stroke-width="`+stroke+`"`)
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return nil, err
	}
	// Draw the 24-unit artwork larger than the image, shifted so that crop units
	// fall off each edge.
	scale := float64(size) / (24 - 2*crop)
	icon.SetTarget(-crop*scale, -crop*scale, 24*scale, 24*scale)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	icon.Draw(rasterx.NewDasher(size, size, rasterx.NewScannerGV(size, size, img, img.Bounds())), 1)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ico wraps PNG frames in an ICO container, which Windows has accepted since
// Vista. The frames are in icoFrames order.
func ico(frames [][]byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, uint16(len(frames))}) // reserved, type icon, count
	off := 6 + 16*len(frames)
	for i, f := range frames {
		sz := icoFrames[i].size
		dim := byte(sz)
		if sz >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})                        // width, height, palette, reserved
		binary.Write(&b, binary.LittleEndian, []uint16{1, 32}) // planes, bits per pixel
		binary.Write(&b, binary.LittleEndian, uint32(len(f)))
		binary.Write(&b, binary.LittleEndian, uint32(off))
		off += len(f)
	}
	for _, f := range frames {
		b.Write(f)
	}
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
