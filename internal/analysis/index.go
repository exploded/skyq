// Package analysis turns parsed frames and luminance samples into the
// transparency index, baselines, cloud detection and event attribution.
// It depends only on ninalog — never on the store or the web layer.
package analysis

import (
	"math"
	"sort"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// BaselineKey identifies a (target, filter) pair. Raw star counts are
// meaningless across either dimension: on the validated night O III found
// 716 stars on IC 4628 and 228 on NGC 2070 under the same sky.
type BaselineKey struct {
	Target string
	Filter string
}

type Baseline struct {
	MedianStars float64
	NFrames     int
}

// Baselines computes per-(target, filter) median detected-star counts from
// light frames. If hasCutoff, only frames strictly before cutoff count —
// "clear" for baseline purposes means before the cloud onset.
func Baselines(frames []ninalog.Frame, cutoff time.Time, hasCutoff bool) map[BaselineKey]Baseline {
	counts := map[BaselineKey][]float64{}
	for _, f := range frames {
		if f.Class() != ninalog.ClassLight {
			continue
		}
		if hasCutoff && !f.At.Before(cutoff) {
			continue
		}
		k := BaselineKey{f.Target, f.Filter}
		counts[k] = append(counts[k], float64(f.DetectedStars))
	}
	out := make(map[BaselineKey]Baseline, len(counts))
	for k, v := range counts {
		out[k] = Baseline{MedianStars: median(v), NFrames: len(v)}
	}
	return out
}

// Index returns the transparency index for one light frame:
// 100 * stars / baseline. ok is false when no baseline exists for the
// frame's (target, filter).
func Index(f ninalog.Frame, baselines map[BaselineKey]Baseline) (float64, bool) {
	b, ok := baselines[BaselineKey{f.Target, f.Filter}]
	if !ok || b.MedianStars <= 0 {
		return 0, false
	}
	return 100 * float64(f.DetectedStars) / b.MedianStars, true
}

// Stats summarises a set of index values.
type Stats struct {
	Median float64
	Q1     float64
	Q3     float64
	N      int
}

// Summarise computes median and interquartile range. Conventions match the
// validated prototype (Python's statistics module): median averages the
// middle pair, quartiles are the "exclusive" R-6 method. The baseline
// median (median above) deliberately differs — see its comment.
func Summarise(values []float64) Stats {
	if len(values) == 0 {
		return Stats{}
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	n := len(s)
	med := s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	return Stats{
		Median: med,
		Q1:     quantile(s, 0.25),
		Q3:     quantile(s, 0.75),
		N:      n,
	}
}

// MedianHigh is the baseline median convention, exported for the rolling
// historical baseline (median over per-night medians).
func MedianHigh(v []float64) float64 { return median(v) }

// median returns the upper middle element for even n (median_high). The
// validated §9 baselines were produced with this convention; changing it
// breaks the acceptance numbers.
func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return math.NaN()
	}
	return s[n/2]
}

// quantile uses the "exclusive" R-6 method — 1-based position q*(n+1) with
// linear interpolation, clamped to the data range. This is what Python's
// statistics.quantiles does and what the §9 IQRs were validated with.
func quantile(s []float64, q float64) float64 {
	n := len(s)
	if n == 0 {
		return math.NaN()
	}
	h := q*float64(n+1) - 1 // 0-based fractional index
	if h <= 0 {
		return s[0]
	}
	if h >= float64(n-1) {
		return s[n-1]
	}
	lo := int(math.Floor(h))
	return s[lo] + (h-float64(lo))*(s[lo+1]-s[lo])
}
