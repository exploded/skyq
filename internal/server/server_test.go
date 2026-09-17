package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
	"github.com/exploded/skyq/internal/report"
)

// eveningSamples is the shape of a real evening: the camera looking at the
// shut roof (flat, dark) until N.I.N.A. opens it at 19:53, then sky.
func eveningSamples() []analysis.LumSample {
	var out []analysis.LumSample
	for at := time.Date(2026, 9, 17, 18, 40, 0, 0, time.UTC); at.Before(time.Date(2026, 9, 17, 22, 30, 0, 0, time.UTC)); at = at.Add(time.Minute) {
		lum := 3.2
		if !at.Before(time.Date(2026, 9, 17, 19, 53, 0, 0, time.UTC)) {
			lum = 37 - at.Sub(time.Date(2026, 9, 17, 19, 53, 0, 0, time.UTC)).Hours()*3.5
		}
		out = append(out, analysis.LumSample{At: at, Luminance: lum})
	}
	return out
}

func TestLumChartHatchesKnownShutRoof(t *testing.T) {
	open := time.Date(2026, 9, 17, 19, 53, 0, 0, time.UTC)
	night := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	roof := analysis.NewRoofTimeline([]ninalog.RoofMove{{
		Start: open, End: open.Add(40 * time.Second),
		From: ninalog.RoofClosed, To: ninalog.RoofOpen,
	}}, night, night.Add(24*time.Hour))

	svg := lumChart(eveningSamples(), roof.Spans())
	if got := strings.Count(svg, `fill="url(#roof-hatch)"`); got != 1 {
		t.Errorf("roof bands = %d, want 1 (shut then moving, merged)", got)
	}
	if !strings.Contains(svg, ">roof shut</text>") {
		t.Error("roof band is not labelled")
	}

	page := `<!doctype html><meta charset="utf-8"><style>` + report.CSS() +
		`</style><body><div class="wrap"><div class="card"><div class="chartbox"><svg viewBox="0 0 940 210">` +
		svg + `</svg></div></div></div></body>`
	if err := os.WriteFile("../../.local/preview-live-chart.html", []byte(page), 0o644); err == nil {
		t.Log("preview written to .local/preview-live-chart.html")
	}
}

// N.I.N.A. never sees a roof opened by hand. No roof history must draw
// exactly the chart it drew before roof bands existed.
func TestLumChartUnknownRoofDrawsNoBand(t *testing.T) {
	svg := lumChart(eveningSamples(), nil)
	if strings.Contains(svg, "roof") {
		t.Error("roof band drawn with no roof history")
	}
}
