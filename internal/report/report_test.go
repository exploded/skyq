package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
	"github.com/exploded/skyq/internal/phd2"
	"github.com/exploded/skyq/internal/skycam"
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
	events := analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, true, nil)
	var untrust []ninalog.AFRun
	for _, r := range res.AFRuns {
		if analysis.UntrustworthyAF(r, res.Frames) {
			untrust = append(untrust, r)
		}
	}

	// The hand-built sky fixture: skycam did not exist that night.
	sky := skyFixture(t)
	from, to := res.Frames[0].At, res.Frames[len(res.Frames)-1].At.Add(30*time.Minute)
	shots := fixtureShots(sky, onset, from, to)
	if len(shots) == 0 {
		t.Fatal("no thumbnails from the fixture")
	}

	html, err := Render(Input{
		NightOf:       "2026-09-02",
		Frames:        res.Frames,
		Events:        events,
		AFRuns:        res.AFRuns,
		TargetChanges: res.TargetChanges,
		Onset:         onset,
		HasOnset:      true,
		CloudSource:   analysis.CloudSkycam,
		Sky:           sky.Frames,
		Shots:         shots,
		Baselines:     base,
		BaselineMode:  "self",
		Untrustworthy: untrust,
		Flats:         res.Flats,
		FlatExposures: res.FlatExposures,
		RoofSpans:     sky.RoofSpans(),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)

	for _, want := range []string{
		"Morning sky flats: 60 frames (H 20, O 20, S 20)",
		`viewBox="0 0 940 320"`, // index chart
		`viewBox="0 0 940 210"`, // cloud chart
		"All&#8209;sky cloud cover", "onset level", "most cloud, 97%",
		"clear-sky baseline",
		"03:48",
		"NGC 2070",
		"var(--series-1)",
		"post_slew",
		`<style>`,
		`<!--nav:top-->`, `<!--nav:bottom-->`, `href="index.html"`,
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
	// skycam saw the roof open all night.
	if strings.Contains(page, "url(#roof-hatch)") {
		t.Error("roof band drawn on a night the roof was open throughout")
	}

	if err := os.WriteFile("../../.local/preview-report.html", html, 0o644); err == nil {
		t.Logf("preview written to .local/preview-report.html (%d KB)", len(html)/1024)
	}
}

// When a target has only a whole-night divisor the report must say so —
// its index is relative, and a reader comparing nights needs to know.
func TestRoofBands(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 8, 31, h, m, 0, 0, time.UTC) }
	origin := at(19, 0)
	span := 240.0 // 19:00–23:00
	spans := []analysis.RoofSpan{
		{From: at(12, 0), To: at(19, 50), State: ninalog.RoofClosed},
		{From: at(19, 50), To: at(19, 51), State: ninalog.RoofMoving},
		{From: at(19, 51), To: at(21, 0), State: ninalog.RoofOpen},
		{From: at(21, 0), To: at(21, 1), State: ninalog.RoofMoving},
		// 21:01–21:30 unknown: the close finished with no logged state.
		{From: at(21, 30), To: at(22, 0), State: ninalog.RoofClosed},
		{From: at(22, 30), To: at(23, 59), State: ninalog.RoofClosed},
	}
	got := RoofBands(spans, origin, span)
	want := []Band{
		{M0: 0, M1: 51},    // shut before the chart, clipped to its start; the move joins it
		{M0: 120, M1: 121}, // a lone move
		{M0: 150, M1: 180}, // unknown on both sides keeps it separate
		{M0: 210, M1: 240}, // clipped to the chart's end
	}
	if len(got) != len(want) {
		t.Fatalf("bands = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("band %d = %v, want %v", i, got[i], want[i])
		}
	}

	// A night the log says nothing about draws no roof at all.
	if b := RoofBands(nil, origin, span); len(b) != 0 {
		t.Errorf("no roof history: bands = %v, want none", b)
	}
}

func TestRenderBaselineFallbackNote(t *testing.T) {
	base := map[analysis.BaselineKey]analysis.Baseline{
		{Target: "IC 4628", Filter: "H"}:  {MedianStars: 332, NFrames: 8, Source: analysis.SourceSelf},
		{Target: "NGC 2070", Filter: "H"}: {MedianStars: 250, NFrames: 18, Source: analysis.SourceWholeNight},
		{Target: "NGC 2070", Filter: "O"}: {MedianStars: 228, NFrames: 12, Source: analysis.SourceHistorical},
	}
	at := time.Date(2026, 9, 3, 22, 0, 0, 0, time.UTC)
	html, err := Render(Input{
		NightOf:      "2026-09-03",
		Frames:       []ninalog.Frame{{At: at, ExposureSec: 300, Filter: "H", Target: "IC 4628", DetectedStars: 330}},
		Baselines:    base,
		BaselineMode: "self",
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{
		"NGC 2070 had no clear frames tonight",
		"<td>whole night, cloud included</td>",
		"<td>earlier clear nights</td>",
		"<td>tonight, clear frames</td>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func skyFixture(t *testing.T) *skycam.Night {
	t.Helper()
	f, err := os.Open("../../testdata/sky-20260902.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, err := skycam.Decode(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fixtureShots stands in for the fetch: the picks whose still is in
// testdata/stills are found, the rest are gone, as past skycam's 14 nights.
func fixtureShots(sky *skycam.Night, onset time.Time, from, to time.Time) []Shot {
	var out []Shot
	for _, p := range PickShots(sky.Frames, onset, true, from, to) {
		path := filepath.Join("../../testdata/stills", filepath.Base(p.Frame.Image))
		if _, err := os.Stat(path); err == nil {
			out = append(out, Shot{At: p.Frame.At, Note: p.Note, Path: path})
		}
	}
	return out
}

func TestPickShots(t *testing.T) {
	sky := skyFixture(t)
	from := time.Date(2026, 9, 2, 20, 48, 0, 0, time.UTC)
	to := time.Date(2026, 9, 3, 6, 0, 0, 0, time.UTC)
	onset := time.Date(2026, 9, 3, 3, 48, 0, 0, time.UTC)
	got := map[string]string{}
	for _, p := range PickShots(sky.Frames, onset, true, from, to) {
		got[p.Note] = p.Frame.At.Format("15:04:05")
	}
	want := map[string]string{
		"start of night": "20:48:00", "cloud onset": "03:48:00",
		"most cloud, 97%": "05:19:12", "end of night": "06:00:00",
	}
	for note, at := range want {
		if got[note] != at {
			t.Errorf("%s = %q, want %s (all picks %v)", note, got[note], at, got)
		}
	}
	if p := PickShots(sky.Frames, onset, true, to.Add(time.Hour), to.Add(2*time.Hour)); p != nil {
		t.Errorf("picks outside every frame: %v", p)
	}
}

// A night skycam could not serve must say cloud is unknown, never imply a
// clear sky, and draw no cloud line.
func TestRenderCloudUnknown(t *testing.T) {
	at := time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)
	html, err := Render(Input{
		NightOf:      "2026-10-01",
		Frames:       []ninalog.Frame{{At: at, ExposureSec: 300, Filter: "H", Target: "IC 4628", DetectedStars: 330}},
		Baselines:    map[analysis.BaselineKey]analysis.Baseline{{Target: "IC 4628", Filter: "H"}: {MedianStars: 332, NFrames: 8}},
		BaselineMode: "self",
		CloudSource:  analysis.CloudUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{"Cloud unknown — skycam had no cloud reading", "skycam could not serve this night"} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}
	for _, bad := range []string{"No cloud onset detected", "Cloud arrived", "Clear all night"} {
		if strings.Contains(page, bad) {
			t.Errorf("unknown-cloud report says %q", bad)
		}
	}
	if _, pills := summarise(html); len(pills) != 2 || pills[1] != "Cloud unknown" {
		t.Errorf("index pills = %v, want [Index 99 Cloud unknown]", pills)
	}
}

// A night from before skycam keeps its luminance onset, and says so.
func TestRenderLuminanceCarriedForward(t *testing.T) {
	at := time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC)
	html, err := Render(Input{
		NightOf:      "2026-08-31",
		Frames:       []ninalog.Frame{{At: at, ExposureSec: 300, Filter: "H", Target: "IC 4628", DetectedStars: 330}},
		Baselines:    map[analysis.BaselineKey]analysis.Baseline{{Target: "IC 4628", Filter: "H"}: {MedianStars: 332, NFrames: 8}},
		BaselineMode: "self",
		Onset:        at.Add(3 * time.Hour), HasOnset: true,
		CloudSource: analysis.CloudLuminance,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{"Cloud arrived at 01:00", "retired luminance detector", "luminance, carried forward"} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

// An LRGB night must plot every filter: the slots are fixed per filter
// (CHARTS.md rule 2) and a filter with no slot folds into "other" rather
// than disappearing. The night of 2026-09-12 shipped with an empty chart
// because only H, O and S had slots.
func TestRenderLRGBNight(t *testing.T) {
	base := map[analysis.BaselineKey]analysis.Baseline{}
	var frames []ninalog.Frame
	at := time.Date(2026, 9, 12, 21, 0, 0, 0, time.UTC)
	for i, fl := range []string{"L", "R", "G", "B", "Q", ""} {
		base[analysis.BaselineKey{Target: "NGC 2070", Filter: fl}] = analysis.Baseline{MedianStars: 800, NFrames: 4, Source: analysis.SourceSelf}
		frames = append(frames, ninalog.Frame{
			At: at.Add(time.Duration(i) * 5 * time.Minute), ExposureSec: 300,
			Filter: fl, Target: "NGC 2070", DetectedStars: 780,
		})
	}
	html, err := Render(Input{
		NightOf:      "2026-09-12",
		Frames:       frames,
		Baselines:    base,
		BaselineMode: "self",
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{
		"var(--series-4)", "var(--series-5)", "var(--series-6)", "var(--series-7)",
		"var(--series-8)", "other (Q, no filter)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report missing %q", want)
		}
	}
	// one marker per light frame plus one hover halo per chart
	if got := strings.Count(page, "<circle "); got != len(frames)+2 {
		t.Errorf("index chart markers = %d, want %d", got-2, len(frames))
	}
}

// guideFixture reads the committed PHD2 log and reduces it the way the
// report run does.
func guideFixture(t *testing.T) ([]analysis.GuideBin, analysis.GuideSummary) {
	t.Helper()
	f, err := os.Open("../../testdata/phd2-guidelog.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := phd2.Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	bins := analysis.GuideBins(res.Samples, analysis.DefaultGuideBin)
	return bins, analysis.SummariseGuiding(res, bins, time.Time{}, false)
}

func guideNightInput(t *testing.T) Input {
	t.Helper()
	bins, summary := guideFixture(t)
	base := map[analysis.BaselineKey]analysis.Baseline{
		{Target: "NGC 2070", Filter: "H"}: {MedianStars: 800, NFrames: 4, Source: analysis.SourceSelf},
	}
	var frames []ninalog.Frame
	at := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		frames = append(frames, ninalog.Frame{
			At: at.Add(time.Duration(i) * 5 * time.Minute), ExposureSec: 300,
			Filter: "H", Target: "NGC 2070", DetectedStars: 790,
		})
	}
	return Input{
		NightOf: "2026-09-02", Frames: frames, Baselines: base, BaselineMode: "self",
		GuideBins: bins, Guide: summary,
	}
}

// The guiding card plots RA and Dec RMS on their own fixed palette slots,
// states the night's RMS, and carries its own hover chart.
func TestRenderGuideChart(t *testing.T) {
	html, err := Render(guideNightInput(t))
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{
		"PHD2 guiding",
		"RMS guide error (arcsec)",
		`id="c3"`, `id="b3"`, `id="t3"`,
		`"fmt":"arcsec"`,
		guideRAColor, guideDecColor,
		">RA<", ">Dec<",
		"night RMS",
		"Guide RMS", // the stat tile
	} {
		if !strings.Contains(page, want) {
			t.Errorf("guiding report missing %q", want)
		}
	}
	// Two bins, two series, plus one hover halo per chart.
	if got, want := strings.Count(page, "<circle "), 2*2+4+3; got != want {
		t.Errorf("markers = %d, want %d", got, want)
	}
}

// A night PHD2 never ran still gets a full report — the card is simply
// absent. Unknown guiding is never drawn as zero error (SPEC §8).
func TestRenderWithoutGuiding(t *testing.T) {
	in := guideNightInput(t)
	in.GuideBins, in.Guide = nil, analysis.GuideSummary{}
	html, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, unwanted := range []string{"PHD2 guiding", `id="c3"`, "Guide RMS", `"fmt":"arcsec"`} {
		if strings.Contains(page, unwanted) {
			t.Errorf("report with no guide log still rendered %q", unwanted)
		}
	}
}

// Guiding usually starts before the first light frame. All three charts
// share one x-axis, so the span has to reach back far enough to show it.
func TestGuideBinsWidenTheChartSpan(t *testing.T) {
	in := guideNightInput(t)
	in.Frames[0].At = time.Date(2026, 9, 2, 22, 0, 0, 0, time.UTC)
	for i := range in.Frames {
		in.Frames[i].At = in.Frames[0].At.Add(time.Duration(i) * 5 * time.Minute)
	}
	origin, _ := chartSpan(in)
	if origin.After(in.GuideBins[0].At) {
		t.Errorf("chart origin %s starts after the first guide bin %s", origin, in.GuideBins[0].At)
	}
}

// TestPreviewGuidingNight renders the validated night with its real PHD2
// guide log and leaves the result in .local for eyeballing — CHARTS.md:
// do not ship a chart you have not looked at. The log is not committed, so
// the test skips without it.
func TestPreviewGuidingNight(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, 9, 2, 12, 0, 0, 0, loc)
	logs, err := phd2.FindNightLogs("../../.local/PHD2", from, from.Add(24*time.Hour), loc)
	if err != nil || len(logs) == 0 {
		t.Skip("no .local/PHD2 guide log for 2026-09-02; skipping preview")
	}
	guide, err := phd2.ParseFiles(logs, loc)
	if err != nil {
		t.Fatal(err)
	}
	guide = guide.Clip(from, from.Add(24*time.Hour))

	f, err := os.Open("../../testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := ninalog.Parse(f, loc)
	if err != nil {
		t.Fatal(err)
	}
	onset := time.Date(2026, 9, 3, 3, 48, 0, 0, loc)
	bins := analysis.GuideBins(guide.Samples, analysis.DefaultGuideBin)
	summary := analysis.SummariseGuiding(guide, bins, onset, true)
	t.Logf("guiding: %d frames, %d sessions, RMS %.2f arcsec, worst minute %.2f at %s",
		summary.All.N, summary.Sessions, summary.All.Total, summary.Worst.Total, summary.Worst.At.Format("15:04"))

	html, err := Render(Input{
		NightOf:       "2026-09-02",
		Frames:        res.Frames,
		Events:        analysis.Attribute(res.Events, res.Slews, res.TargetChanges, onset, true, nil),
		AFRuns:        res.AFRuns,
		TargetChanges: res.TargetChanges,
		Sky:           skyFixture(t).Frames,
		Onset:         onset,
		HasOnset:      true,
		CloudSource:   analysis.CloudSkycam,
		Baselines:     analysis.Baselines(res.Frames, onset, true),
		BaselineMode:  "self",
		Flats:         res.Flats,
		FlatExposures: res.FlatExposures,
		GuideBins:     bins,
		Guide:         summary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `viewBox="0 0 940 224"`) {
		t.Error("guide chart missing from a night with a guide log")
	}
	if err := os.WriteFile("../../.local/preview-report-guiding.html", html, 0o644); err == nil {
		t.Logf("preview written to .local/preview-report-guiding.html (%d KB)", len(html)/1024)
	}
}
