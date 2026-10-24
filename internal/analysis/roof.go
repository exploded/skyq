package analysis

import (
	"sort"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// RoofSpan is a stretch of one night with a single known roof state.
type RoofSpan struct {
	From, To time.Time
	State    ninalog.RoofState
}

// RoofTimeline is a night's roof history from the moves N.I.N.A. logged.
// It is the fallback for nights skycam cannot serve — every night before
// skycam — and it only draws the report's roof bands. skycam's own roof
// state comes from the roll-off controller and sees every move.
//
// The log is an incomplete record on purpose (see ninalog.RoofMove): it holds
// only the moves N.I.N.A. itself commanded, so a roof opened by hand from the
// controller's web page is invisible to it. That shapes the two rules here:
//
//   - Each move's From state is carried BACKWARDS to the previous known
//     change, and its To state FORWARDS to the next one. The morning close
//     line therefore proves the roof was open all night, which is the only
//     evidence the validated night of 2026-09-02/03 has.
//   - Everything outside a known span is RoofUnknown, and unknown gets no
//     band. Absence of evidence is not evidence the roof was shut (SPEC §8:
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
