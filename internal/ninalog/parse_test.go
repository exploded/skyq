package ninalog

import (
	"os"
	"testing"
	"time"
)

// The assertions in this file are §9 of SPEC.md: hand-validated numbers from
// the night of 2026-09-02/03. The parser must reproduce them exactly.

func parseFixture(t *testing.T) *Result {
	t.Helper()
	f, err := os.Open("../../testdata/20260902.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Parse(f, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestFrameClassCounts(t *testing.T) {
	res := parseFixture(t)
	counts := map[Class]int{}
	byFilter := map[string]int{}
	for _, f := range res.Frames {
		counts[f.Class()]++
		if f.Class() == ClassLight {
			byFilter[f.Filter]++
		}
	}
	if counts[ClassLight] != 79 {
		t.Errorf("light frames = %d, want 79", counts[ClassLight])
	}
	if counts[ClassCentering] != 18 {
		t.Errorf("centering frames = %d, want 18", counts[ClassCentering])
	}
	want := map[string]int{"H": 32, "O": 25, "S": 22}
	for filt, n := range want {
		if byFilter[filt] != n {
			t.Errorf("light frames filter %s = %d, want %d", filt, byFilter[filt], n)
		}
	}
	for filt := range byFilter {
		if _, ok := want[filt]; !ok {
			t.Errorf("unexpected light-frame filter %q (%d frames)", filt, byFilter[filt])
		}
	}
}

func TestAutofocusRuns(t *testing.T) {
	res := parseFixture(t)
	if got := len(res.AFRuns); got != 14 {
		t.Errorf("completed autofocus runs = %d, want 14", got)
	}
	failed := 0
	for _, e := range res.Events {
		if e.Kind == AutofocusFailed {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("failed autofocus runs = %d, want 3", failed)
	}
	// The 04:42 run rejected its own first answer.
	var found bool
	for _, r := range res.AFRuns {
		if r.Start.Format("15:04") == "04:42" {
			found = true
			if !r.Rejected {
				t.Error("04:42 autofocus run should be flagged Rejected")
			}
		}
	}
	if !found {
		t.Error("no autofocus run starting 04:42")
	}
}

func TestTargetsAndSlews(t *testing.T) {
	res := parseFixture(t)
	if len(res.TargetChanges) != 2 {
		t.Fatalf("target changes = %d, want 2 (%v)", len(res.TargetChanges), res.TargetChanges)
	}
	if res.TargetChanges[0].Name != "IC 4628" || res.TargetChanges[1].Name != "NGC 2070" {
		t.Errorf("targets = %q, %q; want IC 4628 then NGC 2070",
			res.TargetChanges[0].Name, res.TargetChanges[1].Name)
	}
	// The slew to NGC 2070 at 00:10:39 anchors post-slew event attribution.
	slewAt := at(t, "2026-09-03T00:10:39")
	var found bool
	for _, s := range res.Slews {
		if s.At.Truncate(time.Second).Equal(slewAt) {
			found = true
		}
	}
	if !found {
		t.Errorf("no slew at 00:10:39; slews: %v", res.Slews)
	}
	// No light frame carries the second target before that slew.
	for _, f := range res.Frames {
		if f.Class() == ClassLight && f.Target == "NGC 2070" && f.At.Before(slewAt) {
			t.Errorf("light frame at %s tagged NGC 2070 before the 00:10:39 slew", f.At.Format("15:04:05"))
		}
	}
}

func TestFailureEvents(t *testing.T) {
	res := parseFixture(t)
	counts := map[EventKind]int{}
	for _, e := range res.Events {
		counts[e.Kind]++
	}
	if counts[SolveFailed] != 6 {
		t.Errorf("plate-solve failures = %d, want 6", counts[SolveFailed])
	}
	if counts[PHD2Error] != 6 {
		t.Errorf("PHD2 errors = %d, want 6", counts[PHD2Error])
	}
	if counts[DomeRefused] == 0 {
		t.Error("expected dome-refused events")
	}
	// The one PHD2 error that is not weather: 00:17:12, ten minutes after the slew.
	var found bool
	for _, e := range res.Events {
		if e.Kind == PHD2Error && e.At.Format("15:04:05") == "00:17:12" {
			found = true
		}
	}
	if !found {
		t.Error("no PHD2 error at 00:17:12")
	}
}
