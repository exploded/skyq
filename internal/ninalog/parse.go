// Package ninalog parses N.I.N.A. log files into frames and events.
//
// All patterns below were validated against N.I.N.A. 3.2.0.9001 (the
// 2026-09-02 fixture). The log is pipe-delimited
// TIMESTAMP|LEVEL|SOURCE|MEMBER|LINE|MESSAGE with local-time timestamps and
// is full of multi-line stack traces, so everything is line-anchored and the
// file is streamed, never loaded whole. Filter on source file, not level —
// the Touch-N-Stars plugin emits hundreds of ERROR lines a night that are
// not observatory events.
package ninalog

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"
)

// tsLayout parses the first 23 characters of a timestamp like
// 2026-09-02T20:48:39.4668 (four fractional digits; we keep three).
const tsLayout = "2006-01-02T15:04:05.000"

var (
	// Star detection result — the transparency measurement. Older Hocus
	// Focus versions (seen in June 2026 logs) emit this from Detect, newer
	// ones from BuildStarDetectionResult; no version logs both.
	reStars = regexp.MustCompile(`^(\S+)\|INFO\|HocusFocusStarDetection\.cs\|(?:BuildStarDetectionResult|Detect)\|\d+\|Average HFR: ([\d.]+), HFR MAD: ([\d.]+), Detected Stars (\d+)`)

	// Exposure start — gives the exposure length and (when populated) the
	// filter for the NEXT star-detection line. The Filter field is empty for
	// long exposures and populated for short ones.
	reCapture = regexp.MustCompile(`^(\S+)\|INFO\|CameraVM\.cs\|Capture\|\d+\|Starting Exposure - Exposure Time: ([\d.]+)s; Filter: (\w*);`)

	// Filter changes — the Filter field on the capture line is often EMPTY for
	// long exposures, so filter must be tracked from these instead.
	reFilter = regexp.MustCompile(`^(\S+)\|INFO\|FilterWheelVM\.cs\|ChangeFilter\|\d+\|Moving to Filter (\w+)`)

	// Actual slews and target changes. Restricted to SequenceItem "Starting"
	// lines: MeridianFlipTrigger.cs quotes identical Item/Target text in its
	// every-few-seconds trigger evaluations and must not match.
	reSlew   = regexp.MustCompile(`^(\S+)\|INFO\|SequenceItem\.cs\|Run\|\d+\|Starting Category: Telescope, Item: SlewScopeToRaDec, Coordinates: RA: ([0-9:]+); Dec: ([^;]+);`)
	reTarget = regexp.MustCompile(`^(\S+)\|INFO\|SequenceItem\.cs\|Run\|\d+\|Starting .*DeepSkyObjectContainer.*Target: (.*?) RA: ([0-9:]+); Dec: ([^;]+);`)

	// Autofocus window boundaries — frames inside these windows are excluded
	// from transparency. Every start eventually hits exactly one of the two
	// closers: the completion line, or the restore line of a cancelled/failed
	// run (the fixture has 14 of the former, 3 of the latter).
	reAFStart  = regexp.MustCompile(`^(\S+)\|\w+\|FocuserMediator\.cs\|BroadcastAutoFocusRunStarting\|`)
	reAFEnd    = regexp.MustCompile(`^(\S+)\|\w+\|HocusFocusVM\.cs\|AutoFocusEngine_Completed\|\d+\|AutoFocus completed with focuser starting at (\d+) and ending at (\d+)`)
	reAFFail   = regexp.MustCompile(`^(\S+)\|\w+\|AutoFocusEngine\.cs\|PerformPostAutoFocusActions\|\d+\|AutoFocus did not complete successfully`)
	reAFTemp   = regexp.MustCompile(`BroadcastSuccessfulAutoFocusRun\|\d+\|Autofocus notification received - Temperature ([\d.]+)`)
	reAFReject = regexp.MustCompile(`^(\S+)\|\w+\|\S+\|ValidateCalculatedFocusPosition\|\d+\|New focus point HFR ([\d.]+) is significantly worse`)

	// Sky flats: the auto-exposure search, its convergence, and each saved
	// flat (filter read from the FLAT filename). Save lines are pinned to
	// BaseImageData.cs — ImageSaveController logs the same path again.
	reFlatSearch = regexp.MustCompile(`^(\S+)\|INFO\|AutoExposureFlat\.cs\|Execute\|\d+\|Determining Dynamic Exposure Time`)
	reFlatFound  = regexp.MustCompile(`\|AutoExposureFlat\.cs\|DetermineExposureTime\|\d+\|Found exposure time at ([\d.]+)s`)
	reFlatSave   = regexp.MustCompile(`^(\S+)\|INFO\|BaseImageData\.cs\|SaveToDisk\|\d+\|Saved image to .*[\\/]FLAT[\\/]FLAT_\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}_(\w+)_`)

	// Failures and successes.
	reSolveOK    = regexp.MustCompile(`\|ImageSolver\.cs\|Solve\|\d+\|Platesolve successful`)
	reSolveFail  = regexp.MustCompile(`^(\S+)\|\w+\|ASTAPSolver\.cs\|ReadResult\|\d+\|ASTAP - Plate solve failed`)
	rePHD2Err    = regexp.MustCompile(`^(\S+)\|\w+\|PHD2Guider\.cs\|ProcessEvent\|\d+\|PHD2 error:(.*)$`)
	reDomeRefuse = regexp.MustCompile(`^(\S+)\|\w+\|DomeVM\.cs\|OpenShutter\|\d+\|Dome shutter ordered to open but the mount is unparked`)
)

// Parse streams one night's log. loc is the observatory's time zone; the log
// carries no zone information.
func Parse(r io.Reader, loc *time.Location) (*Result, error) {
	res := &Result{}
	var (
		curFilter string // wheel position, tracked from ChangeFilter lines
		curTarget string
		openAF    *AFRun // non-nil while inside an autofocus window

		// Frame state is latched at capture time, not detection time: the
		// wheel often moves for the next exposure before the previous frame's
		// star-detection line is emitted, so reading curFilter at detection
		// time mis-tags frames at every filter transition.
		pendingExposure float64
		pendingFilter   string

		// A flat exposure search that never logs "Found exposure time"
		// before the next search (or EOF) failed to converge.
		openFlatSearch *time.Time
	)
	failOpenFlatSearch := func() {
		if openFlatSearch != nil {
			res.Events = append(res.Events, Event{At: *openFlatSearch, Kind: FlatsFailed,
				Detail: "flat auto-exposure search never converged"})
			openFlatSearch = nil
		}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()

		if m := reStars.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			hfr, _ := strconv.ParseFloat(m[2], 64)
			mad, _ := strconv.ParseFloat(m[3], 64)
			stars, _ := strconv.Atoi(m[4])
			res.Frames = append(res.Frames, Frame{
				At: at, ExposureSec: pendingExposure, Filter: pendingFilter,
				Target: curTarget, DetectedStars: stars, HFR: hfr, HFRMad: mad,
				InAutofocus: openAF != nil,
			})
			continue
		}
		if m := reCapture.FindStringSubmatch(line); m != nil {
			pendingExposure, _ = strconv.ParseFloat(m[2], 64)
			if m[3] != "" {
				pendingFilter = m[3]
			} else {
				pendingFilter = curFilter
			}
			continue
		}
		if m := reFilter.FindStringSubmatch(line); m != nil {
			curFilter = m[2]
			continue
		}
		if m := reAFStart.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			openAF = &AFRun{Start: at}
			continue
		}
		if m := reAFEnd.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			if openAF != nil {
				openAF.End = at
				openAF.StartPos, _ = strconv.Atoi(m[2])
				openAF.EndPos, _ = strconv.Atoi(m[3])
				openAF.Completed = true
				res.AFRuns = append(res.AFRuns, *openAF)
				openAF = nil
			}
			continue
		}
		if m := reAFFail.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			if openAF != nil {
				// A cancelled or failed run never defocused far enough to
				// invalidate its frames the way a completed sweep does, and
				// the validated prototype classes its short L frames as
				// centering (§9's 18). Un-tag the frames of this window.
				for i := len(res.Frames) - 1; i >= 0 && !res.Frames[i].At.Before(openAF.Start); i-- {
					res.Frames[i].InAutofocus = false
				}
				res.Events = append(res.Events, Event{At: at, Kind: AutofocusFailed,
					Detail: fmt.Sprintf("autofocus run started %s did not complete", openAF.Start.Format("15:04:05"))})
				openAF = nil
			}
			continue
		}
		if m := reAFTemp.FindStringSubmatch(line); m != nil {
			if n := len(res.AFRuns); n > 0 {
				res.AFRuns[n-1].Temperature, _ = strconv.ParseFloat(m[1], 64)
			}
			continue
		}
		if m := reAFReject.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			if openAF != nil {
				openAF.Rejected = true
			}
			res.Events = append(res.Events, Event{At: at, Kind: AutofocusRejected,
				Detail: "new focus point HFR " + m[2] + " significantly worse than original"})
			continue
		}
		if m := reSlew.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			res.Slews = append(res.Slews, Slew{At: at, RA: m[2], Dec: m[3]})
			continue
		}
		if m := reTarget.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			curTarget = m[2]
			res.TargetChanges = append(res.TargetChanges, TargetChange{At: at, Name: m[2], RA: m[3], Dec: m[4]})
			continue
		}
		if m := reFlatSearch.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			failOpenFlatSearch()
			openFlatSearch = &at
			continue
		}
		if m := reFlatFound.FindStringSubmatch(line); m != nil {
			exp, _ := strconv.ParseFloat(m[1], 64)
			res.FlatExposures = append(res.FlatExposures, exp)
			openFlatSearch = nil
			continue
		}
		if m := reFlatSave.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			res.Flats = append(res.Flats, FlatFrame{At: at, Filter: m[2]})
			continue
		}
		if reSolveOK.MatchString(line) {
			res.SolveSuccesses++
			continue
		}
		if m := reSolveFail.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			res.Events = append(res.Events, Event{At: at, Kind: SolveFailed, Detail: "ASTAP plate solve failed"})
			continue
		}
		if m := rePHD2Err.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			res.Events = append(res.Events, Event{At: at, Kind: PHD2Error, Detail: m[2]})
			continue
		}
		if m := reDomeRefuse.FindStringSubmatch(line); m != nil {
			at, err := parseTS(m[1], loc, lineNo)
			if err != nil {
				return nil, err
			}
			res.Events = append(res.Events, Event{At: at, Kind: DomeRefused,
				Detail: "dome shutter ordered to open but the mount is unparked"})
			continue
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	failOpenFlatSearch()
	return res, nil
}

func parseTS(s string, loc *time.Location, lineNo int) (time.Time, error) {
	if len(s) < len(tsLayout) {
		return time.Time{}, fmt.Errorf("line %d: short timestamp %q", lineNo, s)
	}
	t, err := time.ParseInLocation(tsLayout, s[:len(tsLayout)], loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("line %d: %w", lineNo, err)
	}
	return t, nil
}
