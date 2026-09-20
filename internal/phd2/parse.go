package phd2

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A guide log is a session header followed by a CSV block, repeated. The
// header lines that matter:
//
//	Guiding Begins at 2026-09-02 20:52:49
//	Pixel scale = 0.69 arc-sec/px, Binning = 1, Focal length = 1200 mm
//	Frame,Time,mount,dx,dy,RARawDistance,DECRawDistance,...
//	1,6.227,"Mount",0.458,1.028,0.304,1.116,...
//	Guiding Ends at 2026-09-02 21:04:05
//
// Columns are read by name from the CSV header, never by position: PHD2's
// log version has added columns over the years and will again.
var (
	reGuideBegins = regexp.MustCompile(`^Guiding Begins at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`)
	reGuideEnds   = regexp.MustCompile(`^Guiding Ends at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`)
	rePixelScale  = regexp.MustCompile(`^Pixel scale = ([0-9.]+) arc-sec/px`)
	reDataRow     = regexp.MustCompile(`^\d+,`)
)

const timeLayout = "2006-01-02 15:04:05"

// Parse reads one guide log. loc is the observatory's timezone: PHD2 writes
// local wall-clock times with no offset, exactly as N.I.N.A. does.
func Parse(r io.Reader, loc *time.Location) (*Result, error) {
	out := &Result{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		open      bool           // inside a Guiding Begins block
		start     time.Time      // that block's start
		cur       Session        // that block's session, flushed on Ends or EOF
		cols      map[string]int // CSV header of the current block
		lastScale float64        // carried across sessions whose header omits it
		lastAt    time.Time      // last sample, to close an unterminated session
	)
	flush := func(end time.Time) {
		if !open {
			return
		}
		cur.End = end
		out.Sessions = append(out.Sessions, cur)
		open, cols = false, nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Guiding Begins at "):
			m := reGuideBegins.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			flush(lastAt) // a log that never wrote "Guiding Ends" — a crash, or a live tail
			at, err := time.ParseInLocation(timeLayout, m[1], loc)
			if err != nil {
				continue
			}
			open, start = true, at
			cur = Session{Start: at, PixelScale: lastScale}
			cols = nil

		case strings.HasPrefix(line, "Guiding Ends at "):
			m := reGuideEnds.FindStringSubmatch(line)
			if m == nil {
				flush(lastAt)
				continue
			}
			at, err := time.ParseInLocation(timeLayout, m[1], loc)
			if err != nil {
				at = lastAt
			}
			flush(at)

		case strings.HasPrefix(line, "Pixel scale = "):
			if m := rePixelScale.FindStringSubmatch(line); m != nil {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
					lastScale = v
					if open {
						cur.PixelScale = v
					}
				}
			}

		case strings.HasPrefix(line, "Frame,"):
			// The CSV header. A calibration block has its own header
			// (Dir,Step,dx,dy,...) with no RARawDistance, which is why the
			// column lookup below rejects it rather than a block matcher.
			cols = headerIndex(line)

		case strings.HasPrefix(line, "Calibration Begins at "), strings.HasPrefix(line, "Calibration Ends at "):
			cols = nil // calibration rows are not guiding

		case open && cols != nil && reDataRow.MatchString(line):
			s, dropped, ok := parseRow(line, cols, start, cur.PixelScale)
			if !ok {
				continue
			}
			if dropped {
				cur.Dropped++
				continue
			}
			cur.Frames++
			lastAt = s.At
			out.Samples = append(out.Samples, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush(lastAt)
	return out, nil
}

// headerIndex maps a CSV header's column names to their positions.
func headerIndex(line string) map[string]int {
	cols := map[string]int{}
	for i, name := range strings.Split(line, ",") {
		cols[strings.TrimSpace(name)] = i
	}
	return cols
}

// parseRow turns one CSV row into a sample. A row whose mount column says
// DROP is a frame PHD2 discarded — star lost, saturated, below the SNR
// floor — and carries no distances; it counts as dropped, never as a
// measurement of zero error.
func parseRow(line string, cols map[string]int, start time.Time, scale float64) (Sample, bool, bool) {
	f := strings.Split(line, ",")
	get := func(name string) (string, bool) {
		i, ok := cols[name]
		if !ok || i >= len(f) {
			return "", false
		}
		return strings.Trim(strings.TrimSpace(f[i]), `"`), true
	}
	if mount, ok := get("mount"); ok && strings.EqualFold(mount, "DROP") {
		return Sample{}, true, true
	}
	secs, ok := getFloat(get, "Time")
	if !ok {
		return Sample{}, false, false
	}
	ra, okRA := getFloat(get, "RARawDistance")
	dec, okDec := getFloat(get, "DECRawDistance")
	if !okRA || !okDec {
		// Either a calibration row or a version of the log format that
		// renamed the columns. Skipping beats inventing a number.
		return Sample{}, false, false
	}
	if scale <= 0 {
		// Without a pixel scale the distances are pixels, and a pixel means
		// nothing across rigs. Drop them rather than plot the wrong unit.
		return Sample{}, false, false
	}
	snr, _ := getFloat(get, "SNR")
	return Sample{
		At:  start.Add(time.Duration(secs * float64(time.Second))),
		RA:  ra * scale,
		Dec: dec * scale,
		SNR: snr,
	}, false, true
}

func getFloat(get func(string) (string, bool), name string) (float64, bool) {
	s, ok := get(name)
	if !ok || s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
