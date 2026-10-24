package analysis

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// §9 of SPEC.md: baselines, index statistics and attribution from the
// validated night. The onset time (03:48) is a recorded input: it was
// validated by hand from that night's all-sky stills, and skycam, which now
// finds the onset, did not exist yet.

var onset = time.Date(2026, 9, 3, 3, 48, 0, 0, time.UTC)

func fixture(t *testing.T) *ninalog.Result {
	t.Helper()
	f, err := os.Open("../../testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := ninalog.Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBaselines(t *testing.T) {
	res := fixture(t)
	base := Baselines(res.Frames, onset, true)
	want := map[BaselineKey]float64{
		{"IC 4628", "H"}:  332,
		{"IC 4628", "O"}:  716,
		{"IC 4628", "S"}:  872,
		{"NGC 2070", "H"}: 310,
		{"NGC 2070", "O"}: 228,
		{"NGC 2070", "S"}: 579,
	}
	for k, w := range want {
		b, ok := base[k]
		if !ok {
			t.Errorf("no baseline for %v", k)
			continue
		}
		if b.MedianStars != w {
			t.Errorf("baseline %v = %v, want %v (n=%d)", k, b.MedianStars, w, b.NFrames)
		}
	}
	if len(base) != len(want) {
		t.Errorf("baseline pairs = %d, want %d: %v", len(base), len(want), base)
	}
}

// A target that only begins after the cloud onset has no clear frames, so
// Baselines gives it nothing and its frames would have no index. The
// whole-night fallback must fill exactly those pairs, mark them so the
// report can say the index is relative, and leave the clear pairs alone.
func TestFillWholeNightAfterOnset(t *testing.T) {
	res := fixture(t)
	early := time.Date(2026, 9, 2, 23, 0, 0, 0, time.UTC) // before NGC 2070 starts at 23:53
	base := Baselines(res.Frames, early, true)
	for _, f := range []string{"H", "O", "S"} {
		if _, ok := base[BaselineKey{"NGC 2070", f}]; ok {
			t.Fatalf("NGC 2070/%s should have no pre-onset baseline", f)
		}
	}
	ic := base[BaselineKey{"IC 4628", "H"}]

	added := FillWholeNight(res.Frames, base)
	if len(added) != 3 {
		t.Fatalf("added %v, want the three NGC 2070 filters", added)
	}
	whole := Baselines(res.Frames, time.Time{}, false)
	for _, k := range added {
		if k.Target != "NGC 2070" {
			t.Errorf("filled %v, which had clear frames", k)
		}
		b := base[k]
		if b.Source != SourceWholeNight {
			t.Errorf("%v source = %q, want %q", k, b.Source, SourceWholeNight)
		}
		if b.MedianStars != whole[k].MedianStars || b.NFrames != whole[k].NFrames {
			t.Errorf("%v = %+v, want whole-night %+v", k, b, whole[k])
		}
		if _, ok := Index(ninalog.Frame{Target: k.Target, Filter: k.Filter, DetectedStars: 1}, base); !ok {
			t.Errorf("%v still has no index", k)
		}
	}
	if got := base[BaselineKey{"IC 4628", "H"}]; got != ic || got.Source != SourceSelf {
		t.Errorf("IC 4628/H changed: %+v -> %+v", ic, got)
	}
	if FillWholeNight(res.Frames, base) != nil {
		t.Error("second fill added pairs")
	}
}

func TestIndexStats(t *testing.T) {
	res := fixture(t)
	base := Baselines(res.Frames, onset, true)
	var before, after []float64
	for _, f := range res.Frames {
		if f.Class() != ninalog.ClassLight {
			continue
		}
		idx, ok := Index(f, base)
		if !ok {
			t.Fatalf("no baseline for light frame %+v", f)
		}
		if f.At.Before(onset) {
			before = append(before, idx)
		} else {
			after = append(after, idx)
		}
	}
	b := Summarise(before)
	a := Summarise(after)
	check := func(name string, got, want float64) {
		if math.Round(got) != want {
			t.Errorf("%s = %v (rounds to %v), want %v", name, got, math.Round(got), want)
		}
	}
	if b.N != 63 {
		t.Errorf("frames before onset = %d, want 63", b.N)
	}
	if a.N != 16 {
		t.Errorf("frames after onset = %d, want 16", a.N)
	}
	check("median before", b.Median, 100)
	check("Q1 before", b.Q1, 86)
	check("Q3 before", b.Q3, 107)
	check("median after", a.Median, 50)
	check("Q1 after", a.Q1, 13)
	check("Q3 after", a.Q3, 75)
}

func TestAttribution(t *testing.T) {
	res := fixture(t)
	attributed := Attribute(res.Events, res.Slews, res.TargetChanges, onset, true, nil)
	for _, e := range attributed {
		if e.Kind != ninalog.PHD2Error {
			continue
		}
		want := CauseCloud
		if e.At.Format("15:04:05") == "00:17:12" {
			want = CausePostSlew
		}
		if e.Cause != want {
			t.Errorf("PHD2 error at %s attributed %s, want %s", e.At.Format("15:04:05"), e.Cause, want)
		}
	}
	for _, e := range attributed {
		if e.Kind == ninalog.SolveFailed && e.Cause != CauseCloud {
			t.Errorf("solve failure at %s attributed %s, want cloud", e.At.Format("15:04:05"), e.Cause)
		}
	}
}

func TestUntrustworthyAF(t *testing.T) {
	res := fixture(t)
	var found bool
	for _, run := range res.AFRuns {
		if run.Start.Format("15:04") == "04:42" {
			found = true
			if !UntrustworthyAF(run, res.Frames) {
				t.Error("04:42 autofocus run should be untrustworthy")
			}
		}
		// The healthy early-evening runs must not be flagged.
		if run.Start.Format("15:04") == "20:48" && UntrustworthyAF(run, res.Frames) {
			t.Error("20:48 autofocus run wrongly flagged untrustworthy")
		}
	}
	if !found {
		t.Fatal("no 04:42 autofocus run")
	}
}

// A failure after the onset, but where skycam had no cloud reading — the
// roof shut, say — is not blamed on cloud. Unknown is never cloudy.
func TestAttributionNeedsKnownCloud(t *testing.T) {
	res := fixture(t)
	gap0, gap1 := onset.Add(52*time.Minute), onset.Add(67*time.Minute) // 04:40–04:55
	known := func(at time.Time) bool { return at.Before(gap0) || !at.Before(gap1) }
	inGap := 0
	for _, e := range Attribute(res.Events, res.Slews, res.TargetChanges, onset, true, known) {
		if e.Kind != ninalog.SolveFailed && e.Kind != ninalog.PHD2Error {
			continue
		}
		want := CauseCloud
		switch {
		case e.At.Format("15:04:05") == "00:17:12":
			want = CausePostSlew
		case !e.At.Before(gap0) && e.At.Before(gap1):
			want = CauseUnexplained
			inGap++
		}
		if e.Cause != want {
			t.Errorf("%s at %s attributed %s, want %s", e.Kind, e.At.Format("15:04:05"), e.Cause, want)
		}
	}
	if inGap == 0 {
		t.Fatal("no failure fell in the unknown stretch; move the gap")
	}
}
