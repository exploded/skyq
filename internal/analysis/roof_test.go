package analysis

import (
	"testing"
	"time"

	"github.com/exploded/skyq/internal/ninalog"
)

var testLoc = time.FixedZone("AEST", 10*3600)

func at(day int, hh, mm int) time.Time {
	return time.Date(2026, 9, day, hh, mm, 0, 0, testLoc)
}

func nightWindow() (time.Time, time.Time) { return at(2, 12, 0), at(3, 12, 0) }

// The validated night logs no open at all — the roof was opened by hand.
// Its only roof line is the morning close, and the "before closing
// ShutterOpen" half of it is what proves the night was open.
func TestRoofTimelineCarriesMorningCloseBackwards(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline([]ninalog.RoofMove{{
		Start: at(3, 6, 36), End: at(3, 6, 37),
		From: ninalog.RoofOpen, To: ninalog.RoofClosed,
	}}, from, to)

	if got := tl.StateAt(at(2, 21, 0)); got != ninalog.RoofOpen {
		t.Errorf("evening: got %s, want open", got)
	}
	if got := tl.StateAt(at(3, 3, 48)); got != ninalog.RoofOpen {
		t.Errorf("at the validated cloud onset: got %s, want open", got)
	}
	if got := tl.StateAt(at(3, 6, 36)); got != ninalog.RoofMoving {
		t.Errorf("during the close: got %s, want moving", got)
	}
	if got := tl.StateAt(at(3, 8, 0)); got != ninalog.RoofClosed {
		t.Errorf("after the close: got %s, want closed", got)
	}
}

// 2026-08-31/09-01 in the sample logs: shut at 01:20, reopened at 05:04.
// Those four hours are the camera looking at the roof.
func TestRoofTimelineMidNightClose(t *testing.T) {
	from := time.Date(2026, 8, 31, 12, 0, 0, 0, testLoc)
	to := at(1, 12, 0)
	tl := NewRoofTimeline([]ninalog.RoofMove{
		{Start: at(1, 1, 20), End: at(1, 1, 21), From: ninalog.RoofOpen, To: ninalog.RoofClosed},
		{Start: at(1, 5, 4), End: at(1, 5, 5), From: ninalog.RoofClosed, To: ninalog.RoofOpen},
	}, from, to)

	for _, tc := range []struct {
		at   time.Time
		want ninalog.RoofState
	}{
		{at(1, 0, 30), ninalog.RoofOpen},
		{at(1, 3, 0), ninalog.RoofClosed},
		{at(1, 6, 0), ninalog.RoofOpen},
	} {
		if got := tl.StateAt(tc.at); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.at.Format("15:04"), got, tc.want)
		}
	}

}

// A night with no roof lines is unknown throughout, never shut: it gets no
// spans, so the report draws no roof bands over it.
func TestRoofTimelineUnknownIsNotShut(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline(nil, from, to)
	if got := tl.StateAt(at(3, 2, 0)); got != ninalog.RoofUnknown {
		t.Errorf("got %s, want unknown", got)
	}
	if spans := tl.Spans(); len(spans) != 0 {
		t.Errorf("spans = %+v, want none", spans)
	}
}

// A move whose finish never reaches the log (a stall, or the log ending
// mid-move) stays moving rather than pretending it completed.
func TestRoofTimelineUnfinishedMove(t *testing.T) {
	from, to := nightWindow()
	tl := NewRoofTimeline([]ninalog.RoofMove{{
		Start: at(3, 6, 36), From: ninalog.RoofOpen,
	}}, from, to)
	if got := tl.StateAt(at(3, 9, 0)); got != ninalog.RoofMoving {
		t.Errorf("got %s, want moving", got)
	}
	if got := tl.StateAt(at(2, 22, 0)); got != ninalog.RoofOpen {
		t.Errorf("before the stall: got %s, want open", got)
	}
}
