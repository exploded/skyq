package analysis

import (
	"math"
	"time"
)

// LumSample is one all-sky luminance measurement.
type LumSample struct {
	At        time.Time
	Luminance float64
}

// Defaults validated on the night of 2026-09-02. The threshold is specific
// to this camera, its gain, and moonlit cloud — it must stay configurable,
// and `skyq calibrate` should derive it from a known clear night rather
// than trusting this number forever. AllSky auto-exposes, so luminance is a
// detection metric, not a photometric one: under cloud the sky brightened
// AND the exposure dropped, which conveniently amplified the signal.
const (
	DefaultVolatilityWindow    = 10
	DefaultVolatilityThreshold = 6.0
)

// VolatilitySeries returns, for each sample, the population standard
// deviation of the trailing window ending at that sample. Samples with
// fewer than window predecessors get NaN — volatility is undefined until
// the window fills.
func VolatilitySeries(samples []LumSample, window int) []float64 {
	out := make([]float64, len(samples))
	for i := range samples {
		if i+1 < window {
			out[i] = math.NaN()
			continue
		}
		out[i] = popStdDev(samples[i+1-window : i+1])
	}
	return out
}

// DetectOnset returns the time of the first sample whose trailing-window
// volatility exceeds threshold. ok is false if the night never crossed it.
func DetectOnset(samples []LumSample, window int, threshold float64) (time.Time, bool) {
	vol := VolatilitySeries(samples, window)
	for i, v := range vol {
		if !math.IsNaN(v) && v > threshold {
			return samples[i].At, true
		}
	}
	return time.Time{}, false
}

func popStdDev(s []LumSample) float64 {
	n := float64(len(s))
	var sum float64
	for _, x := range s {
		sum += x.Luminance
	}
	mean := sum / n
	var ss float64
	for _, x := range s {
		d := x.Luminance - mean
		ss += d * d
	}
	return math.Sqrt(ss / n)
}
