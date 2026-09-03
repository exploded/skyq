package ninalog

import "time"

// Class is the exposure class of a frame. Only Light frames measure
// transparency; mixing classes was the prototype's first wrong answer.
type Class string

const (
	ClassLight     Class = "light"     // >= LightExposureSec, outside autofocus
	ClassCentering Class = "centering" // short L-filter frames for plate solving
	ClassAutofocus Class = "autofocus" // inside an AF window — defocused on purpose
	ClassOther     Class = "other"     // short non-L frames outside AF; not used
)

// LightExposureSec is the boundary between centering and light frames.
const LightExposureSec = 60

// Frame is one star-detection result, tagged with the parser state that was
// current when it happened.
type Frame struct {
	At            time.Time
	ExposureSec   float64
	Filter        string // H, O, S, L
	Target        string // "IC 4628", "NGC 2070"
	DetectedStars int
	HFR           float64
	HFRMad        float64
	InAutofocus   bool
}

// Class classifies the frame per §4.2 of the spec.
func (f Frame) Class() Class {
	switch {
	case f.InAutofocus:
		return ClassAutofocus
	case f.ExposureSec >= LightExposureSec:
		return ClassLight
	case f.Filter == "L":
		return ClassCentering
	default:
		return ClassOther
	}
}

type EventKind string

const (
	SolveFailed       EventKind = "solve_failed"
	PHD2Error         EventKind = "phd2_error"
	AutofocusRejected EventKind = "autofocus_rejected"
	AutofocusFailed   EventKind = "autofocus_failed" // run cancelled or gave up
	DomeRefused       EventKind = "dome_refused"
	FlatsFailed       EventKind = "flats_failed" // exposure search never converged
)

type Event struct {
	At     time.Time
	Kind   EventKind
	Detail string
}

// Slew is an actual telescope slew instruction (SequenceItem "Starting"
// lines only — MeridianFlipTrigger previews quote the same text and must
// not count).
type Slew struct {
	At time.Time
	RA string
	Dec string
}

// TargetChange is a DeepSkyObjectContainer starting with a named target.
type TargetChange struct {
	At   time.Time
	Name string
	RA   string
	Dec  string
}

// AFRun is one autofocus window. Every BroadcastAutoFocusRunStarting opens
// one; it closes on either the completion line (Completed=true) or the
// "did not complete successfully" restore line (Completed=false). Frames
// inside any window — completed or not — are ClassAutofocus.
type AFRun struct {
	Start       time.Time
	End         time.Time
	StartPos    int
	EndPos      int
	Temperature float64 // from the post-run broadcast; 0 if absent
	Rejected    bool    // logged "New focus point HFR ... significantly worse"
	Completed   bool
}

// FlatFrame is one saved flat. Flats never feed the transparency index —
// they carry no stars — but the report accounts for them: a flats session
// that quietly failed is found before stacking, not after.
type FlatFrame struct {
	At     time.Time
	Filter string
}

// Result is everything one night's log yields.
type Result struct {
	Frames         []Frame
	Events         []Event
	Slews          []Slew
	TargetChanges  []TargetChange
	AFRuns         []AFRun // completed runs only; §9 counts these
	SolveSuccesses int
	Flats          []FlatFrame
	FlatExposures  []float64 // auto-exposure times the flat wizard settled on
}

// CompletedAFRuns is len(Result.AFRuns) by construction; kept as a method
// so callers don't depend on that detail.
func (r *Result) CompletedAFRuns() int { return len(r.AFRuns) }
