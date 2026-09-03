package allsky

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Still is one cached all-sky image. The capture time comes from the
// filename — image-YYYYMMDDHHMMSS.jpg, local time — so no video timing
// calibration is ever needed.
type Still struct {
	Path string
	At   time.Time
}

var reStillName = regexp.MustCompile(`^image-(\d{14})\.jpg$`)

// ParseStillTime extracts the capture time from an AllSky still filename.
func ParseStillTime(name string, loc *time.Location) (time.Time, bool) {
	m := reStillName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102150405", m[1], loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ScanDir lists the stills in one local cache directory, sorted by capture
// time. Files that don't look like stills are ignored.
func ScanDir(dir string, loc *time.Location) ([]Still, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var stills []Still
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if at, ok := ParseStillTime(e.Name(), loc); ok {
			stills = append(stills, Still{Path: filepath.Join(dir, e.Name()), At: at})
		}
	}
	sort.Slice(stills, func(i, j int) bool { return stills[i].At.Before(stills[j].At) })
	return stills, nil
}
