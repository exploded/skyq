package analysis

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// §9 of SPEC.md: baselines, index statistics and attribution from the
// validated night. The onset time (03:48) is an input here — computing it
// needs the all-sky stills, which are not part of this fixture.

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
	attributed := Attribute(res.Events, res.Slews, res.TargetChanges, onset, true)
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

func TestDetectOnsetSynthetic(t *testing.T) {
	// Flat luminance, then oscillation: onset fires when the trailing-window
	// stddev crosses the threshold, not before.
	start := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	var samples []LumSample
	for i := 0; i < 40; i++ {
		lum := 28.0
		if i >= 30 && i%2 == 0 {
			lum = 55.0 // cloud: big swings
		}
		samples = append(samples, LumSample{At: start.Add(time.Duration(i) * time.Minute), Luminance: lum})
	}
	if _, ok := DetectOnset(samples[:30], DefaultVolatilityWindow, DefaultVolatilityThreshold); ok {
		t.Error("onset detected on flat series")
	}
	at, ok := DetectOnset(samples, DefaultVolatilityWindow, DefaultVolatilityThreshold)
	if !ok {
		t.Fatal("no onset detected on volatile series")
	}
	if at.Before(samples[30].At) {
		t.Errorf("onset %s before volatility began %s", at, samples[30].At)
	}
}
