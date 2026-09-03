package analysis

import (
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

// Cause classifies why a failure event happened, per §4.5 of the spec.
type Cause string

const (
	CauseCloud       Cause = "cloud"
	CausePostSlew    Cause = "post_slew"
	CauseUnexplained Cause = "unexplained"
)

// postSlewWindow is how long after a slew or target change a PHD2 error is
// blamed on guide-star reacquisition rather than the sky.
const postSlewWindow = 10 * time.Minute

// AttributedEvent pairs an event with its cause.
type AttributedEvent struct {
	ninalog.Event
	Cause Cause
}

// Attribute assigns a cause to every event. Order matters: a PHD2 error
// shortly after a slew is mechanical even if cloud is also present; on the
// validated night this correctly reclassified the 00:17:12 error that
// followed the 00:10:39 slew. Everything else after cloud onset is cloud;
// the remainder is unexplained and worth a human's attention.
func Attribute(events []ninalog.Event, slews []ninalog.Slew, targets []ninalog.TargetChange, onset time.Time, hasOnset bool) []AttributedEvent {
	var moves []time.Time
	for _, s := range slews {
		moves = append(moves, s.At)
	}
	for _, tc := range targets {
		moves = append(moves, tc.At)
	}

	out := make([]AttributedEvent, 0, len(events))
	for _, e := range events {
		out = append(out, AttributedEvent{Event: e, Cause: causeOf(e, moves, onset, hasOnset)})
	}
	return out
}

func causeOf(e ninalog.Event, moves []time.Time, onset time.Time, hasOnset bool) Cause {
	if e.Kind == ninalog.PHD2Error {
		for _, m := range moves {
			if !e.At.Before(m) && e.At.Sub(m) <= postSlewWindow {
				return CausePostSlew
			}
		}
	}
	if hasOnset && !e.At.Before(onset) {
		return CauseCloud
	}
	return CauseUnexplained
}

// AFTrustMinStars: an autofocus run fitted on frames with fewer stars than
// this is untrustworthy even if it converged — on the validated night the
// 04:42 run fitted its curve on 3–28 stars, rejected its own first answer,
// and landing somewhere sane was luck.
const AFTrustMinStars = 30

// UntrustworthyAF reports whether a completed autofocus run should be
// flagged. frames is the whole night; the run's own frames are selected by
// its time window.
func UntrustworthyAF(run ninalog.AFRun, frames []ninalog.Frame) bool {
	if run.Rejected {
		return true
	}
	var stars []float64
	for _, f := range frames {
		if f.InAutofocus && !f.At.Before(run.Start) && !f.At.After(run.End) {
			stars = append(stars, float64(f.DetectedStars))
		}
	}
	return len(stars) > 0 && median(stars) < AFTrustMinStars
}
