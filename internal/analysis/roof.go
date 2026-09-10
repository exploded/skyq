package analysis

import (
	"sort"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// DefaultRoofSettle is how long after the roof finishes moving the all-sky
// camera is still not measuring sky: the roof edge clears the frame, and
// whoever opened up is usually still inside with a light on.
const DefaultRoofSettle = 5 * time.Minute

// RoofSpan is a stretch of one night with a single known roof state.
type RoofSpan struct {
	From, To time.Time
	State    ninalog.RoofState
}

// RoofTimeline answers "was the sky visible?" for a night, from the roof
// moves N.I.N.A. logged.
//
// The log is an incomplete record on purpose (see ninalog.RoofMove): it holds
// only the moves N.I.N.A. itself commanded, so a roof opened by hand from the
// controller's web page is invisible to it. That shapes the two rules here:
//
//   - Each move's From state is carried BACKWARDS to the previous known
//     change, and its To state FORWARDS to the next one. The morning close
//     line therefore proves the roof was open all night, which is the only
//     evidence the validated night of 2026-09-02/03 has.
//   - Everything outside a known span is RoofUnknown, and unknown counts as
//     visible. Absence of evidence is not evidence the roof was shut, and
//     blanking a night on a missing log line would lose real sky (SPEC §8:
//     unknown is never treated as a worse state than it is).
type RoofTimeline struct {
	spans []RoofSpan
}

// NewRoofTimeline builds the timeline for a night. Moves need not be sorted;
// moves outside [nightFrom, nightTo] are ignored.
func NewRoofTimeline(moves []ninalog.RoofMove, nightFrom, nightTo time.Time) RoofTimeline {
	var in []ninalog.RoofMove
	for _, m := range moves {
		if m.Start.Before(nightFrom) || m.Start.After(nightTo) {
			continue
		}
		in = append(in, m)
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start.Before(in[j].Start) })

	var spans []RoofSpan
	prevEnd := nightFrom
	for i, m := range in {
		// What held before this move, back to the last thing we knew.
		if m.From != ninalog.RoofUnknown && m.Start.After(prevEnd) {
			spans = append(spans, RoofSpan{From: prevEnd, To: m.Start, State: m.From})
		}
		// The move itself. A move with no logged finish stalled or outlived
		// the log; it stays "moving" to the end of the night rather than
		// pretending it completed.
		end := m.End
		if end.IsZero() {
			end = nightTo
		}
		spans = append(spans, RoofSpan{From: m.Start, To: end, State: ninalog.RoofMoving})
		prevEnd = end

		// What holds after it, up to the next move or the end of the night.
		to := nightTo
		if i+1 < len(in) {
			to = in[i+1].Start
		}
		if m.To != ninalog.RoofUnknown && to.After(end) {
			spans = append(spans, RoofSpan{From: end, To: to, State: m.To})
			prevEnd = to
		}
	}
	return RoofTimeline{spans: spans}
}

// Spans returns the known spans, in order. Gaps between them are unknown.
func (t RoofTimeline) Spans() []RoofSpan { return append([]RoofSpan(nil), t.spans...) }

// StateAt reports the roof state at a moment, or RoofUnknown if the log
// never said.
func (t RoofTimeline) StateAt(at time.Time) ninalog.RoofState {
	for _, s := range t.spans {
		if !at.Before(s.From) && at.Before(s.To) {
			return s.State
		}
	}
	return ninalog.RoofUnknown
}

// SkyVisible reports whether the all-sky camera could see sky at a moment:
// false only when the roof is known shut or moving, or within settle of a
// move finishing. Unknown is visible — see the type comment.
func (t RoofTimeline) SkyVisible(at time.Time, settle time.Duration) bool {
	switch t.StateAt(at) {
	case ninalog.RoofClosed, ninalog.RoofMoving:
		return false
	}
	for _, s := range t.spans {
		if s.State != ninalog.RoofMoving {
			continue
		}
		if !at.Before(s.To) && at.Before(s.To.Add(settle)) {
			return false
		}
	}
	return true
}

// VisibleSamples drops the luminance samples the camera took of the roof
// rather than the sky. Filtering the samples out — rather than suppressing
// the verdict around them — is what keeps the open/close step out of the
// trailing volatility window entirely.
func (t RoofTimeline) VisibleSamples(samples []LumSample, settle time.Duration) []LumSample {
	if len(t.spans) == 0 {
		return samples
	}
	out := make([]LumSample, 0, len(samples))
	for _, s := range samples {
		if t.SkyVisible(s.At, settle) {
			out = append(out, s)
		}
	}
	return out
}
