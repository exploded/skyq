package phd2

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// PHD2 names a guide log for the moment it was opened:
// PHD2_GuideLog_2026-09-02_204038.txt. The debug log sitting beside it
// (PHD2_DebugLog_*) is a different format and must not match.
var reGuideLogName = regexp.MustCompile(`^PHD2_GuideLog_(\d{4}-\d{2}-\d{2}_\d{6})\.txt$`)

// logLead is how far before the night's window a guide log may have been
// opened and still hold that night's guiding. PHD2 is usually launched with
// the rest of the rig in the afternoon, and it keeps writing to the one
// file until it is restarted.
const logLead = 12 * time.Hour

// FindNightLogs returns the guide logs that could hold guiding inside
// [start, end): those opened from logLead before the window up to its end.
// Which samples actually belong to the night is decided afterwards by
// Result.Clip, not by the filename.
func FindNightLogs(dir string, start, end time.Time, loc *time.Location) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var logs []string
	for _, e := range entries {
		m := reGuideLogName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02_150405", m[1], loc)
		if err != nil {
			continue
		}
		if at.Before(start.Add(-logLead)) || !at.Before(end) {
			continue
		}
		logs = append(logs, filepath.Join(dir, e.Name()))
	}
	sort.Strings(logs)
	return logs, nil
}

// ParseFiles parses and merges every guide log of one night. Unlike the
// N.I.N.A. logs there is no hash: guiding never changes an ingested number,
// so a re-run just re-reads whatever is on disk.
func ParseFiles(paths []string, loc *time.Location) (*Result, error) {
	merged := &Result{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		r, err := Parse(f, loc)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		merged.Samples = append(merged.Samples, r.Samples...)
		merged.Sessions = append(merged.Sessions, r.Sessions...)
	}
	sort.Slice(merged.Samples, func(i, j int) bool { return merged.Samples[i].At.Before(merged.Samples[j].At) })
	sort.Slice(merged.Sessions, func(i, j int) bool { return merged.Sessions[i].Start.Before(merged.Sessions[j].Start) })
	return merged, nil
}
