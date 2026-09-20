// Package phd2 parses PHD2 guide logs — pure, no I/O beyond an io.Reader,
// like ninalog. It answers one question for the morning report: how well
// did the mount track, minute by minute, over the same night the
// transparency index covers.
package phd2

import (
	"math"
	"time"
)

// Sample is one guide frame's raw tracking error, converted from pixels to
// arcseconds with the session's own pixel scale. The signs are PHD2's own
// (RARawDistance, DECRawDistance); the report takes RMS, so it never
// depends on which way is positive.
//
// Guide-corrected distances (RAGuideDistance) are deliberately not kept:
// raw distance is the error the mount actually had, before PHD2's own
// correction was applied to it.
type Sample struct {
	At  time.Time
	RA  float64 // arcsec
	Dec float64 // arcsec
	SNR float64
}

// Total is the combined tracking error of one guide frame.
func (s Sample) Total() float64 { return math.Hypot(s.RA, s.Dec) }

// Session is one "Guiding Begins"/"Guiding Ends" block. A night has many:
// PHD2 stops guiding for every dither settle, slew, autofocus run and
// meridian flip, so the gaps between sessions are the story as much as the
// numbers inside them.
type Session struct {
	Start      time.Time
	End        time.Time // zero when the log ends mid-session (a crash, or a log still being written)
	PixelScale float64   // arc-sec/px, from the session header
	Frames     int       // guide frames that yielded a measurement
	Dropped    int       // frames PHD2 threw away — "star lost", saturation, low SNR
}

// Result is everything one night's guide logs yield.
type Result struct {
	Samples  []Sample
	Sessions []Session
}

// Dropped totals the frames PHD2 discarded across the night.
func (r *Result) Dropped() int {
	n := 0
	for _, s := range r.Sessions {
		n += s.Dropped
	}
	return n
}

// Guiding totals the time spent inside guiding sessions. A session the log
// never closes is measured to its last sample, not to the end of the night.
func (r *Result) Guiding() time.Duration {
	var d time.Duration
	for _, s := range r.Sessions {
		if s.End.After(s.Start) {
			d += s.End.Sub(s.Start)
		}
	}
	return d
}

// Clip narrows a result to [from, to). One guide log can span two nights —
// PHD2 keeps writing to the same file until it is restarted — so the
// night's window, not the filename, decides what belongs to a night.
func (r *Result) Clip(from, to time.Time) *Result {
	out := &Result{}
	for _, s := range r.Samples {
		if s.At.Before(from) || !s.At.Before(to) {
			continue
		}
		out.Samples = append(out.Samples, s)
	}
	for _, s := range r.Sessions {
		end := s.End
		if end.IsZero() {
			end = s.Start
		}
		if end.Before(from) || !s.Start.Before(to) {
			continue
		}
		out.Sessions = append(out.Sessions, s)
	}
	return out
}
