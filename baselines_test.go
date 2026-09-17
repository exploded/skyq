package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
	"github.com/exploded/skyq/internal/store"
	db "github.com/exploded/skyq/internal/store/db"
)

// The night of 2026-09-03: cloud arrived at 21:20 and NGC 2070 did not start
// until 23:50, so it had no clear frames and the report gave it no index
// at all. With one clear night on record the pair must fall back to that
// history; with none, to its own whole-night median. IC 4628, which does
// have clear frames tonight, keeps its self baseline either way.
func TestChooseBaselinesTargetAfterOnset(t *testing.T) {
	f, err := os.Open("testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := ninalog.Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Onset before NGC 2070's first light (23:53) — every NGC 2070 frame is post-cloud.
	onset := time.Date(2026, 9, 2, 23, 0, 0, 0, time.UTC)
	ngc := func(filter string) analysis.BaselineKey { return analysis.BaselineKey{Target: "NGC 2070", Filter: filter} }
	ic := analysis.BaselineKey{Target: "IC 4628", Filter: "H"}

	sdb, err := store.Open(filepath.Join(t.TempDir(), "skyq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	q := db.New(sdb)
	ctx := context.Background()

	// No history at all: whole-night fallback.
	base, mode := chooseBaselines(ctx, q, res.Frames, onset, true, "2026-09-03")
	if mode != "self" {
		t.Errorf("mode = %q, want self", mode)
	}
	whole := analysis.Baselines(res.Frames, time.Time{}, false)
	for _, k := range []analysis.BaselineKey{ngc("H"), ngc("O"), ngc("S")} {
		b, ok := base[k]
		if !ok {
			t.Fatalf("%v has no baseline", k)
		}
		if b.Source != analysis.SourceWholeNight || b.MedianStars != whole[k].MedianStars {
			t.Errorf("%v = %+v, want whole-night %v", k, b, whole[k].MedianStars)
		}
	}
	self := analysis.Baselines(res.Frames, onset, true)
	if b := base[ic]; b.Source != analysis.SourceSelf || b.MedianStars != self[ic].MedianStars {
		t.Errorf("IC 4628/H = %+v, want self %v", b, self[ic].MedianStars)
	}

	// One clear night on record (the validated 2026-09-02, onset 03:48 —
	// NGC 2070's frames before that are clear): history wins over whole-night,
	// even though it is well short of the five nights the rolling mode needs.
	prev := time.Date(2026, 9, 3, 3, 48, 0, 0, time.UTC)
	prevBase := analysis.Baselines(res.Frames, prev, true)
	if err := ingest(ctx, sdb, q, ingestInput{
		night: "2026-09-02", logPath: "testdata/20260902.log", sha: "fixture",
		res: res, onset: prev, hasOnset: true, baselines: prevBase, mode: "self", volWindow: 10,
	}); err != nil {
		t.Fatal(err)
	}
	base, mode = chooseBaselines(ctx, q, res.Frames, onset, true, "2026-09-03")
	if mode != "self" {
		t.Errorf("mode = %q, want self (IC 4628 is still self)", mode)
	}
	for k, want := range map[analysis.BaselineKey]float64{ngc("H"): 310, ngc("O"): 228, ngc("S"): 579} {
		b := base[k]
		if b.Source != analysis.SourceHistorical || b.MedianStars != want {
			t.Errorf("%v = %+v, want historical %v", k, b, want)
		}
	}
	if b := base[ic]; b.Source != analysis.SourceSelf || b.MedianStars != self[ic].MedianStars {
		t.Errorf("IC 4628/H = %+v, want self %v", b, self[ic].MedianStars)
	}
	if _, ok := analysis.Index(ninalog.Frame{Target: "NGC 2070", Filter: "H", DetectedStars: 300}, base); !ok {
		t.Error("NGC 2070/H frame still has no index")
	}
}

// 2026-06-28 left NGC 346 H = 2 stars in the baselines table from a single
// cloudy frame, and the live page divided by it. A pair with fewer than
// MinBaselineFrames clear frames serves its own night only: it is not
// stored, and that night is not history for later nights.
func TestThinBaselinesStayOnTheirNight(t *testing.T) {
	sdb, err := store.Open(filepath.Join(t.TempDir(), "skyq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	q := db.New(sdb)
	ctx := context.Background()

	light := func(at time.Time, target, filter string, stars int) ninalog.Frame {
		return ninalog.Frame{At: at, ExposureSec: 300, Target: target, Filter: filter, DetectedStars: stars}
	}
	thin := analysis.BaselineKey{Target: "NGC 346", Filter: "H"}
	solid := analysis.BaselineKey{Target: "Gum 41", Filter: "H"}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	res := &ninalog.Result{Frames: []ninalog.Frame{
		light(t0, "Gum 41", "H", 100),
		light(t0.Add(5*time.Minute), "Gum 41", "H", 110),
		light(t0.Add(10*time.Minute), "Gum 41", "H", 120),
		light(t0.Add(15*time.Minute), "NGC 346", "H", 2),
	}}
	base := analysis.Baselines(res.Frames, time.Time{}, false)
	if base[thin].MedianStars != 2 {
		t.Fatalf("NGC 346/H = %+v, want its own 2-star median tonight", base[thin])
	}
	if err := ingest(ctx, sdb, q, ingestInput{
		night: "2026-06-28", logPath: "fixture", sha: "fixture",
		res: res, baselines: base, mode: "self", volWindow: 10,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := q.ListBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Target != solid.Target || rows[0].MedianStars != 110 {
		t.Errorf("stored baselines = %+v, want only Gum 41/H = 110", rows)
	}
	if b, _, ok := historicalBaseline(ctx, q, thin, "2026-09-17"); ok {
		t.Errorf("NGC 346/H history = %+v from a one-frame night", b)
	}
	if b, nights, ok := historicalBaseline(ctx, q, solid, "2026-09-17"); !ok || nights != 1 || b.MedianStars != 110 {
		t.Errorf("Gum 41/H history = %+v over %d nights (ok=%v), want 110 over 1", b, nights, ok)
	}
}
