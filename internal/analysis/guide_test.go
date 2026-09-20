package analysis

import (
	"math"
	"testing"
	"time"

	"github.com/exploded/skyq/internal/phd2"
)

func gat(hh, mm, ss int) time.Time { return time.Date(2026, 9, 2, hh, mm, ss, 0, time.UTC) }

func guideFixture() *phd2.Result {
	return &phd2.Result{
		Samples: []phd2.Sample{
			{At: gat(21, 0, 0), RA: 1.0, Dec: 0.0},
			{At: gat(21, 0, 10), RA: -1.0, Dec: 0.8},
			{At: gat(21, 0, 30), RA: 1.0, Dec: -0.8},
			{At: gat(21, 10, 0), RA: 2.0, Dec: 2.0},
			{At: gat(21, 10, 30), RA: -2.0, Dec: -2.0},
		},
		Sessions: []phd2.Session{
			{Start: gat(21, 0, 0), End: gat(21, 1, 0), PixelScale: 2, Frames: 3, Dropped: 1},
			{Start: gat(21, 10, 0), End: gat(21, 10, 30), PixelScale: 2, Frames: 2},
		},
	}
}

func closeTo(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %.6f, want %.6f", name, got, want)
	}
}

func TestGuideBins(t *testing.T) {
	bins := GuideBins(guideFixture().Samples, time.Minute)
	if len(bins) != 2 {
		t.Fatalf("bins = %d, want 2", len(bins))
	}
	// 21:00 holds three samples: RA² mean 1, Dec² mean 1.28/3.
	if !bins[0].At.Equal(gat(21, 0, 0)) || bins[0].N != 3 {
		t.Errorf("bin 0 = %s n=%d, want 21:00:00 n=3", bins[0].At, bins[0].N)
	}
	closeTo(t, "bin 0 RA RMS", bins[0].RARMS, 1.0)
	closeTo(t, "bin 0 Dec RMS", bins[0].DecRMS, math.Sqrt(1.28/3))
	closeTo(t, "bin 0 total", bins[0].Total, math.Hypot(1.0, math.Sqrt(1.28/3)))
	// The empty minutes between the two guiding sessions must be absent,
	// not zero — the chart breaks its line over a gap, it does not draw a
	// run through it at perfect guiding.
	if !bins[1].At.Equal(gat(21, 10, 0)) {
		t.Errorf("bin 1 = %s, want 21:10:00", bins[1].At)
	}
	closeTo(t, "bin 1 RA RMS", bins[1].RARMS, 2.0)
}

// RMS is taken about zero, not about the bin's mean: a minute that drifted
// steadily 2" off is a bad minute, and de-meaning it would report zero.
func TestGuideBinRMSIsAboutZero(t *testing.T) {
	var samples []phd2.Sample
	for i := 0; i < 6; i++ {
		samples = append(samples, phd2.Sample{At: gat(22, 0, i*5), RA: 2.0, Dec: 0})
	}
	bins := GuideBins(samples, time.Minute)
	if len(bins) != 1 {
		t.Fatalf("bins = %d, want 1", len(bins))
	}
	closeTo(t, "constant-offset RA RMS", bins[0].RARMS, 2.0)
}

// A single frame is not an RMS; a bin that thin is dropped rather than
// plotted as a spike.
func TestGuideBinsNeedTwoSamples(t *testing.T) {
	bins := GuideBins([]phd2.Sample{{At: gat(23, 0, 0), RA: 5, Dec: 5}}, time.Minute)
	if len(bins) != 0 {
		t.Fatalf("bins = %d, want 0", len(bins))
	}
}

func TestSummariseGuiding(t *testing.T) {
	res := guideFixture()
	bins := GuideBins(res.Samples, time.Minute)
	g := SummariseGuiding(res, bins, gat(21, 5, 0), true)

	closeTo(t, "night RA RMS", g.All.RA, math.Sqrt(11.0/5))
	closeTo(t, "night Dec RMS", g.All.Dec, math.Sqrt(9.28/5))
	if g.All.N != 5 || g.Dropped != 1 || g.Sessions != 2 {
		t.Errorf("summary = %d frames, %d dropped, %d sessions; want 5/1/2", g.All.N, g.Dropped, g.Sessions)
	}
	if g.Duration != 90*time.Second {
		t.Errorf("duration = %s, want 1m30s", g.Duration)
	}
	// Split at the onset: three frames before, two after.
	if !g.HasSplit || g.Before.N != 3 || g.After.N != 2 {
		t.Errorf("split = %v before=%d after=%d, want true 3 2", g.HasSplit, g.Before.N, g.After.N)
	}
	closeTo(t, "before RA RMS", g.Before.RA, 1.0)
	closeTo(t, "after RA RMS", g.After.RA, 2.0)
	if !g.Worst.At.Equal(gat(21, 10, 0)) {
		t.Errorf("worst bin = %s, want 21:10:00", g.Worst.At)
	}
}

// No onset means no split: the report must not quote a "before the cloud"
// number for a night that had no cloud.
func TestSummariseGuidingWithoutOnset(t *testing.T) {
	res := guideFixture()
	g := SummariseGuiding(res, GuideBins(res.Samples, time.Minute), time.Time{}, false)
	if g.HasSplit || g.Before.N != 0 || g.After.N != 0 {
		t.Errorf("split reported without an onset: %+v", g)
	}
}

// A night PHD2 never ran must summarise to nothing rather than to zero
// error — unknown is not perfect (SPEC §8).
func TestSummariseGuidingNoData(t *testing.T) {
	g := SummariseGuiding(&phd2.Result{}, nil, time.Time{}, false)
	if g.All.N != 0 || g.All.Total != 0 {
		t.Errorf("empty night summarised as %+v", g.All)
	}
	if g2 := SummariseGuiding(nil, nil, time.Time{}, false); g2.All.N != 0 {
		t.Errorf("nil result summarised as %+v", g2.All)
	}
}
