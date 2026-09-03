package ninalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseRealLogs runs the parser across every real N.I.N.A. log in
// .local/nina-logs (not committed; the test skips when absent). It asserts
// nothing about content — only that months of real logs, including crashed
// and seconds-long sessions, parse without error and without impossible
// state.
func TestParseRealLogs(t *testing.T) {
	dir := "../../.local/nina-logs"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip("no .local/nina-logs; skipping robustness sweep")
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			res, err := Parse(f, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			for _, fr := range res.Frames {
				if fr.At.IsZero() {
					t.Fatal("frame with zero time")
				}
				if fr.DetectedStars < 0 || fr.ExposureSec < 0 {
					t.Fatalf("impossible frame: %+v", fr)
				}
			}
			for _, run := range res.AFRuns {
				if run.End.Before(run.Start) {
					t.Fatalf("AF run ends before it starts: %+v", run)
				}
			}
		})
	}
}
