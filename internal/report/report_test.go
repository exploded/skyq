package report

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/allsky"
	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
)

// The embedded stylesheet must stay byte-identical to the design source of
// truth. If this fails, re-copy design/report.css — never edit the embed.
func TestCSSMatchesDesign(t *testing.T) {
	design, err := os.ReadFile("../../design/report.css")
	if err != nil {
		t.Fatal(err)
	}
	if string(design) != reportCSS {
		t.Fatal("internal/report/report.css has drifted from design/report.css — re-copy it")
	}
}

func TestRenderFixtureNight(t *testing.T) {
	f, err := os.Open("../../testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := ninalog.Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	onset := time.Date(2026, 9, 3, 3, 48, 0, 0, time.UTC)
	base := analysis.Baselines(res.Frames, onset, true)
	events := analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, true)
	var untrust []ninalog.AFRun
	for _, r := range res.AFRuns {
		if analysis.UntrustworthyAF(r, res.Frames) {
			untrust = append(untrust, r)
		}
	}

	// Real luminance data: the validated reference report embeds the series
	// measured from the night's all-sky video. Chart origin was 20:45 local.
	samples := referenceLumSeries(t)

	var stills []allsky.Still
	if st, err := allsky.ScanDir("../../testdata/stills", time.UTC); err == nil {
		for _, s := range st {
			stills = append(stills, s)
		}
	}

	html, err := Render(Input{
		NightOf:       "2026-09-02",
		Frames:        res.Frames,
		Events:        events,
		AFRuns:        res.AFRuns,
		TargetChanges: res.TargetChanges,
		Samples:       samples,
		Onset:         onset,
		HasOnset:      true,
		Baselines:     base,
		BaselineMode:  "self",
		Stills:        stills,
		Untrustworthy: untrust,
		Flats:         res.Flats,
		FlatExposures: res.FlatExposures,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)

	for _, want := range []string{
		"Morning sky flats: 60 frames (H 20, O 20, S 20)",
		`viewBox="0 0 940 320"`, // index chart
		`viewBox="0 0 940 210"`, // luminance chart
		"clear-sky baseline",
		"03:48",
		"NGC 2070",
		"var(--series-1)",
		"post_slew",
		`<style>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("report references an external URL; it must be self-contained")
	}
	// 14 completed AF runs → 14 shaded bands.
	if got := strings.Count(page, `fill="var(--band)"`); got != 14 {
		t.Errorf("autofocus bands = %d, want 14", got)
	}

	if err := os.WriteFile("../../.local/preview-report.html", html, 0o644); err == nil {
		t.Logf("preview written to .local/preview-report.html (%d KB)", len(html)/1024)
	}
}

var reLumData = regexp.MustCompile(`"lum":\[((?:\[[\d.,-]+\],?)+)\]`)

func referenceLumSeries(t *testing.T) []analysis.LumSample {
	t.Helper()
	raw, err := os.ReadFile("../../design/reference-report.html")
	if err != nil {
		t.Skipf("no reference report: %v", err)
	}
	m := reLumData.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no lum series in reference report")
	}
	origin := time.Date(2026, 9, 2, 20, 45, 0, 0, time.UTC)
	var samples []analysis.LumSample
	for _, pair := range strings.Split(strings.Trim(string(m[1]), "[]"), "],[") {
		parts := strings.Split(pair, ",")
		if len(parts) != 2 {
			continue
		}
		mm, err1 := strconv.ParseFloat(parts[0], 64)
		v, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		samples = append(samples, analysis.LumSample{
			At:        origin.Add(time.Duration(mm * float64(time.Minute))),
			Luminance: v,
		})
	}
	if len(samples) < 100 {
		t.Fatalf("only %d luminance samples parsed from reference", len(samples))
	}
	return samples
}
