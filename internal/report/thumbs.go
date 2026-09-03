package report

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	"os"
	"time"

	"golang.org/x/image/draw"

	"github.com/exploded/skyq/internal/allsky"
)

const thumbWidth = 400

// pickShots chooses the interesting all-sky moments — start of night, cloud
// onset, the brightest (worst) frame, end of night — and embeds each as a
// data: URI so the report stays a single file.
func pickShots(in Input) []shot {
	if len(in.Stills) == 0 {
		return nil
	}
	type pick struct {
		still allsky.Still
		note  string
	}
	nearest := func(t time.Time) allsky.Still {
		best := in.Stills[0]
		bd := time.Duration(1<<62 - 1)
		for _, s := range in.Stills {
			d := s.At.Sub(t)
			if d < 0 {
				d = -d
			}
			if d < bd {
				bd = d
				best = s
			}
		}
		return best
	}

	var picks []pick
	picks = append(picks, pick{in.Stills[0], "start of night"})
	if in.HasOnset {
		picks = append(picks, pick{nearest(in.Onset), "cloud onset"})
	}
	if len(in.Samples) > 0 {
		worst := in.Samples[0]
		for _, s := range in.Samples {
			if s.Luminance > worst.Luminance {
				worst = s
			}
		}
		picks = append(picks, pick{nearest(worst.At), fmt.Sprintf("peak brightness %.1f", worst.Luminance)})
	}
	picks = append(picks, pick{in.Stills[len(in.Stills)-1], "end of night"})

	seen := map[string]bool{}
	var shots []shot
	for _, p := range picks {
		if seen[p.still.Path] {
			continue
		}
		seen[p.still.Path] = true
		uri, err := thumbDataURI(p.still.Path)
		if err != nil {
			continue
		}
		shots = append(shots, shot{Time: p.still.At.Format("15:04"), Note: p.note, Src: template.URL(uri)})
	}
	return shots
}

func thumbDataURI(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	h := b.Dy() * thumbWidth / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, thumbWidth, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 62}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
