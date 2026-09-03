// Package allsky measures sky luminance from all-sky stills and syncs them
// from the AllSky box. Luminance code is pure (io.Reader in, number out);
// network and filesystem live in fetch.go and scan.go.
package allsky

import (
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
)

// Default crop band, validated for this camera's orientation: full width,
// 6%–30% of image height. That avoids the telescope silhouette (centre),
// the horizon glow and the moon (bottom). The AllSky overlay text clips the
// band's top-left corner; the validated numbers were produced with it there,
// so it stays. Configurable because it depends on camera orientation.
const (
	DefaultBandTop    = 0.06
	DefaultBandBottom = 0.30
)

// grid size the crop is reduced to before averaging, per the prototype.
const (
	gridW = 64
	gridH = 16
)

// Luminance decodes a still and returns the mean luminance (0–255) of the
// horizontal band between topFrac and bottomFrac of image height.
//
// AllSky auto-exposes, so this is a detection metric, not photometry: under
// cloud the sky brightens while the exposure shortens, and the two move
// together. Never present it as sky brightness in physical units.
func Luminance(r io.Reader, topFrac, bottomFrac float64) (float64, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return 0, fmt.Errorf("decoding still: %w", err)
	}
	b := img.Bounds()
	y0 := b.Min.Y + int(float64(b.Dy())*topFrac)
	y1 := b.Min.Y + int(float64(b.Dy())*bottomFrac)
	if y1 <= y0 {
		return 0, fmt.Errorf("crop band %v–%v is empty for %dpx image", topFrac, bottomFrac, b.Dy())
	}

	// Box-average the band into a gridW×gridH grid, then average the cells.
	// Equivalent to the prototype's downsample-to-64×16-then-mean.
	var total float64
	cells := 0
	for gy := 0; gy < gridH; gy++ {
		cy0 := y0 + (y1-y0)*gy/gridH
		cy1 := y0 + (y1-y0)*(gy+1)/gridH
		for gx := 0; gx < gridW; gx++ {
			cx0 := b.Min.X + b.Dx()*gx/gridW
			cx1 := b.Min.X + b.Dx()*(gx+1)/gridW
			var sum float64
			n := 0
			for y := cy0; y < cy1; y++ {
				for x := cx0; x < cx1; x++ {
					r16, g16, b16, _ := img.At(x, y).RGBA()
					// Rec.601 luma on 8-bit values.
					sum += 0.299*float64(r16>>8) + 0.587*float64(g16>>8) + 0.114*float64(b16>>8)
					n++
				}
			}
			if n > 0 {
				total += sum / float64(n)
				cells++
			}
		}
	}
	if cells == 0 {
		return 0, fmt.Errorf("crop band produced no cells")
	}
	return total / float64(cells), nil
}
