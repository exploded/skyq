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

	"github.com/exploded/skyq/internal/skycam"
)

const thumbWidth = 400

// Shot is one all-sky still the report shows, already in the local cache.
type Shot struct {
	At   time.Time
	Note string
	Path string
}

// ShotPick is a frame worth a thumbnail, and why.
type ShotPick struct {
	Frame skycam.Frame
	Note  string
}

// PickShots chooses the interesting all-sky moments — start of the imaging
// session, cloud onset, the cloudiest frame, end of session — from skycam's
// frames in [from, to] that name a still. Fetching them is the caller's
// job: skycam keeps stills for 14 nights, so any pick may come back 404 and
// is then left out.
func PickShots(frames []skycam.Frame, onset time.Time, hasOnset bool, from, to time.Time) []ShotPick {
	var in []skycam.Frame
	for _, f := range frames {
		if f.Image != "" && !f.At.Before(from) && !f.At.After(to) {
			in = append(in, f)
		}
	}
	if len(in) == 0 {
		return nil
	}
	nearest := func(t time.Time) skycam.Frame {
		best := in[0]
		bd := time.Duration(1<<62 - 1)
		for _, f := range in {
			d := f.At.Sub(t)
			if d < 0 {
				d = -d
			}
			if d < bd {
				bd, best = d, f
			}
		}
		return best
	}

	picks := []ShotPick{{in[0], "start of night"}}
	if hasOnset {
		picks = append(picks, ShotPick{nearest(onset), "cloud onset"})
	}
	var worst *skycam.Frame
	for i, f := range in {
		if f.Cloud != nil && (worst == nil || *f.Cloud > *worst.Cloud) {
			worst = &in[i]
		}
	}
	if worst != nil && *worst.Cloud > 0 {
		picks = append(picks, ShotPick{*worst, fmt.Sprintf("most cloud, %.0f%%", *worst.Cloud*100)})
	}
	picks = append(picks, ShotPick{in[len(in)-1], "end of night"})

	seen := map[string]bool{}
	var out []ShotPick
	for _, p := range picks {
		if !seen[p.Frame.Image] {
			seen[p.Frame.Image] = true
			out = append(out, p)
		}
	}
	return out
}

// shots embeds each still as a data: URI so the report stays a single
// file. A still that will not decode is left out, never fatal.
func shots(in []Shot) []shot {
	var out []shot
	for _, s := range in {
		uri, err := thumbDataURI(s.Path)
		if err != nil {
			continue
		}
		out = append(out, shot{Time: s.At.Format("15:04"), Note: s.Note, Src: template.URL(uri)})
	}
	return out
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
