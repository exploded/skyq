package ninalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var reLogName = regexp.MustCompile(`^(\d{8}-\d{6})-.*\.log$`)

// FindNightLogs returns the session logs whose start timestamp (from the
// filename) falls inside [start, end). A session that rolls over a month
// boundary writes a second file with the same start stamp — both match and
// both get parsed.
func FindNightLogs(dir string, start, end time.Time, loc *time.Location) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var logs []string
	for _, e := range entries {
		m := reLogName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		at, err := time.ParseInLocation("20060102-150405", m[1], loc)
		if err != nil {
			continue
		}
		if !at.Before(start) && at.Before(end) {
			logs = append(logs, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(logs)
	return logs, nil
}

// ParseFiles parses and merges every session log of one night, and hashes
// the raw bytes for the re-ingest guard.
func ParseFiles(paths []string, loc *time.Location) (*Result, string, error) {
	merged := &Result{}
	h := sha256.New()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, "", err
		}
		r, err := Parse(io.TeeReader(f, h), loc)
		f.Close()
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		merged.Frames = append(merged.Frames, r.Frames...)
		merged.Events = append(merged.Events, r.Events...)
		merged.Slews = append(merged.Slews, r.Slews...)
		merged.TargetChanges = append(merged.TargetChanges, r.TargetChanges...)
		merged.AFRuns = append(merged.AFRuns, r.AFRuns...)
		merged.SolveSuccesses += r.SolveSuccesses
		merged.Flats = append(merged.Flats, r.Flats...)
		merged.FlatExposures = append(merged.FlatExposures, r.FlatExposures...)
		merged.RoofMoves = append(merged.RoofMoves, r.RoofMoves...)
	}
	sort.Slice(merged.Frames, func(i, j int) bool { return merged.Frames[i].At.Before(merged.Frames[j].At) })
	sort.Slice(merged.Events, func(i, j int) bool { return merged.Events[i].At.Before(merged.Events[j].At) })
	sort.Slice(merged.Flats, func(i, j int) bool { return merged.Flats[i].At.Before(merged.Flats[j].At) })
	sort.Slice(merged.RoofMoves, func(i, j int) bool { return merged.RoofMoves[i].Start.Before(merged.RoofMoves[j].Start) })
	return merged, hex.EncodeToString(h.Sum(nil)), nil
}
