package analysis

import (
	"math"
	"time"

	"github.com/exploded/skyq/internal/phd2"
)

// A night holds thousands of guide frames — roughly 6,500 at a 5 s guide
// exposure — and a night-wide chart has about 800 pixels to draw them in.
// Plotting them raw produces a noise band, not a curve, so the report bins
// them and plots the RMS of each bin: the same shape PHD2's own graph
// shows, at the scale the rest of the report works at.
const (
	// DefaultGuideBin is the bin width the report uses.
	DefaultGuideBin = time.Minute
	// MinGuideBinSamples is the fewest frames a bin needs before its RMS is
	// worth plotting. Bins at the edges of a guiding session are partial,
	// and a single frame's error is not an RMS.
	MinGuideBinSamples = 2
)

// GuideBin is one bin of guiding reduced to RMS tracking error, in arcsec.
//
// RMS here is taken about zero, not about the bin's own mean — the number
// wanted is how far the star actually sat from where it should have been.
// Subtracting the mean, as PHD2's live graph does, would hide exactly the
// slow drift the morning report is looking for.
type GuideBin struct {
	At     time.Time // bin start
	RARMS  float64
	DecRMS float64
	Total  float64 // hypot(RARMS, DecRMS)
	N      int
}

// GuideRMS is the RMS tracking error over some set of guide frames.
type GuideRMS struct {
	RA, Dec, Total float64
	N              int
}

// GuideSummary is the night's guiding in the few numbers the report states.
type GuideSummary struct {
	All      GuideRMS
	Before   GuideRMS // before the cloud onset; zero-valued when there is none
	After    GuideRMS
	HasSplit bool // Before/After are populated and both have frames

	Dropped  int
	Sessions int
	Duration time.Duration
	Worst    GuideBin // the worst bin by total RMS
}

// GuideBins groups samples into fixed-width bins and takes each bin's RMS.
// Bins with no samples are absent rather than zero: the chart must break
// its line over a gap in guiding, never draw a run through it at zero.
func GuideBins(samples []phd2.Sample, width time.Duration) []GuideBin {
	if width <= 0 {
		width = DefaultGuideBin
	}
	var bins []GuideBin
	var cur GuideBin
	var sumRA, sumDec float64
	flush := func() {
		if cur.N >= MinGuideBinSamples {
			cur.RARMS = math.Sqrt(sumRA / float64(cur.N))
			cur.DecRMS = math.Sqrt(sumDec / float64(cur.N))
			cur.Total = math.Hypot(cur.RARMS, cur.DecRMS)
			bins = append(bins, cur)
		}
		cur, sumRA, sumDec = GuideBin{}, 0, 0
	}
	for _, s := range samples {
		at := s.At.Truncate(width)
		if cur.N > 0 && !at.Equal(cur.At) {
			flush()
		}
		cur.At, cur.N = at, cur.N+1
		sumRA += s.RA * s.RA
		sumDec += s.Dec * s.Dec
	}
	flush()
	return bins
}

// SummariseGuiding reduces a night's guiding to the numbers the verdict and
// the stat row quote. When the all-sky camera found a cloud onset, the RMS
// is also split either side of it — the report's whole question is which
// failures the sky caused, and guiding that fell apart only after the cloud
// arrived answers it.
func SummariseGuiding(r *phd2.Result, bins []GuideBin, onset time.Time, hasOnset bool) GuideSummary {
	if r == nil {
		return GuideSummary{}
	}
	out := GuideSummary{
		All:      rmsOf(r.Samples),
		Dropped:  r.Dropped(),
		Sessions: len(r.Sessions),
		Duration: r.Guiding(),
	}
	if hasOnset {
		var before, after []phd2.Sample
		for _, s := range r.Samples {
			if s.At.Before(onset) {
				before = append(before, s)
			} else {
				after = append(after, s)
			}
		}
		out.Before, out.After = rmsOf(before), rmsOf(after)
		out.HasSplit = out.Before.N > 0 && out.After.N > 0
	}
	for _, b := range bins {
		if b.Total > out.Worst.Total {
			out.Worst = b
		}
	}
	return out
}

func rmsOf(samples []phd2.Sample) GuideRMS {
	if len(samples) == 0 {
		return GuideRMS{}
	}
	var sumRA, sumDec float64
	for _, s := range samples {
		sumRA += s.RA * s.RA
		sumDec += s.Dec * s.Dec
	}
	n := float64(len(samples))
	ra, dec := math.Sqrt(sumRA/n), math.Sqrt(sumDec/n)
	return GuideRMS{RA: ra, Dec: dec, Total: math.Hypot(ra, dec), N: len(samples)}
}
