// Package report renders the self-contained morning report: one HTML file,
// CSS inlined, thumbnails as data: URIs, no external requests. The look is
// defined by design/report.css and design/CHARTS.md — the copy embedded here
// is guarded by a test against drift.
package report

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/exploded/skyq/internal/allsky"
	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
)

//go:embed report.css
var reportCSS string

// CSS exposes the design-system stylesheet for the live page, which shares
// the report's look.
func CSS() string { return reportCSS }

//go:embed page.tmpl
var pageTmpl string

var tmpl = template.Must(template.New("page").Parse(pageTmpl))

// Input is everything one night's report needs, already analysed.
type Input struct {
	NightOf       string // evening date, e.g. "2026-09-02"
	Frames        []ninalog.Frame
	Events        []analysis.AttributedEvent
	AFRuns        []ninalog.AFRun
	TargetChanges []ninalog.TargetChange
	Samples       []analysis.LumSample
	Onset         time.Time
	HasOnset      bool
	Baselines     map[analysis.BaselineKey]analysis.Baseline
	BaselineMode  string // "self" | "historical"
	Stills        []allsky.Still
	Untrustworthy []ninalog.AFRun

	// RoofNote explains any stretch missing from the luminance chart because
	// the roof was shut over it. Empty when the log recorded no roof moves.
	RoofNote string

	Flats            []ninalog.FlatFrame
	FlatExposures    []float64
	FlatsUnstableSky bool // all-sky volatility crossed the threshold during flats
}

// filterSlots maps filters to their fixed palette slots (CHARTS.md rule 2:
// assigned in slot order, never cycled, never re-hued). A light frame whose
// filter is not listed here is never dropped: it folds into one "other"
// series on otherSlotColor, so a filter the wheel has never shown before
// still appears on the chart instead of vanishing.
var filterSlots = []struct {
	Filter, Label, Color string
}{
	{"H", "H α", "var(--series-1)"},
	{"O", "O III", "var(--series-2)"},
	{"S", "S II", "var(--series-3)"},
	{"L", "L", "var(--series-4)"},
	{"R", "R", "var(--series-5)"},
	{"G", "G", "var(--series-6)"},
	{"B", "B", "var(--series-7)"},
}

const otherSlotColor = "var(--series-8)"

// otherSeries collects the light frames whose filter has no slot into one
// series labelled with the filter names it holds.
func otherSeries(frames []ninalog.Frame, baselines map[analysis.BaselineKey]analysis.Baseline, toMin func(time.Time) float64) (Series, bool) {
	known := map[string]bool{}
	for _, slot := range filterSlots {
		known[slot.Filter] = true
	}
	var pts []Pt
	var names []string
	seen := map[string]bool{}
	for _, f := range frames {
		if f.Class() != ninalog.ClassLight || known[f.Filter] {
			continue
		}
		idx, ok := analysis.Index(f, baselines)
		if !ok {
			continue
		}
		pts = append(pts, Pt{M: toMin(f.At), V: idx, Stars: f.DetectedStars, Target: f.Target})
		name := f.Filter
		if name == "" {
			name = "no filter"
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(pts) == 0 {
		return Series{}, false
	}
	label := "other (" + strings.Join(names, ", ") + ")"
	return Series{Name: label, Label: label, Color: otherSlotColor, Pts: pts}, true
}

type stat struct{ K, V, Small string }
type legendItem struct{ Label, Color string }
type shot struct{ Time, Note string; Src template.URL }
type baselineRow struct {
	Target, Filter string
	Median         string
	N              int
	Source         string
}
type eventRow struct{ Time, Kind, Detail, Cause string }
type frameRow struct {
	Time, Class, Target, Filter, Exp, Stars, HFR, Index string
}

type pageData struct {
	Title            string
	Verdict          template.HTML
	Stats            []stat
	Legend           []legendItem
	IndexChart       template.HTML
	LumChart         template.HTML
	MultiTarget      bool
	BaselineModeNote string
	BaselineFallback string // set when some pair had no clear frames tonight
	RoofNote         string // set when the roof gate left a gap in the chart
	HasOnset         bool
	OnsetHM          string
	Shots            []shot
	Baselines        []baselineRow
	Events           []eventRow
	EventNote        string
	Frames           []frameRow
	FlatCount        int
	Foot             string
	CSS              template.CSS
	ChartsJSON       template.JS
	NavTop           template.HTML // rewritten by Relink once neighbours exist
	NavBottom        template.HTML
}

// Render produces the finished report HTML.
func Render(in Input) ([]byte, error) {
	origin, span := chartSpan(in)
	toMin := func(t time.Time) float64 { return t.Sub(origin).Minutes() }
	hm := func(t time.Time) string { return t.Format("15:04") }

	// Index series, one per filter in fixed slot order.
	var series []Series
	var legend []legendItem
	for _, slot := range filterSlots {
		var pts []Pt
		for _, f := range in.Frames {
			if f.Class() != ninalog.ClassLight || f.Filter != slot.Filter {
				continue
			}
			idx, ok := analysis.Index(f, in.Baselines)
			if !ok {
				continue
			}
			pts = append(pts, Pt{M: toMin(f.At), V: idx, Stars: f.DetectedStars, Target: f.Target})
		}
		if len(pts) > 0 {
			series = append(series, Series{Name: slot.Label, Label: slot.Filter, Color: slot.Color, Pts: pts})
			legend = append(legend, legendItem{Label: slot.Label, Color: slot.Color})
		}
	}
	if other, ok := otherSeries(in.Frames, in.Baselines, toMin); ok {
		series = append(series, other)
		legend = append(legend, legendItem{Label: other.Label, Color: other.Color})
	}

	var bands []Band
	for _, r := range in.AFRuns {
		bands = append(bands, Band{M0: toMin(r.Start), M1: toMin(r.End)})
	}
	var marks []EventMark
	for _, e := range in.Events {
		switch e.Kind {
		case ninalog.SolveFailed:
			marks = append(marks, EventMark{M: toMin(e.At), Kind: "solve"})
		case ninalog.PHD2Error:
			marks = append(marks, EventMark{M: toMin(e.At), Kind: "phd"})
		}
	}

	firstHour := origin.Truncate(time.Hour).Add(time.Hour)
	tickOff := firstHour.Sub(origin).Minutes()

	tickLabel := func(m float64) string { return origin.Add(time.Duration(m * float64(time.Minute))).Format("15:04") }

	ref := 100.0
	opts := ChartOpts{
		H: 320, YMax: 200, YTicks: 4, GapMin: 75, YLabel: "% of clear-sky median",
		Rail: true, Ref: &ref, RefLabel: "clear-sky baseline",
		SpanMin: span, TickStart: tickOff, TickLabel: tickLabel,
		Bands: bands, Events: marks,
		YFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) },
	}
	// Name the night's first target at the plot's top left, and rule every
	// subsequent target change — a night can have more than two targets.
	if len(in.TargetChanges) > 0 {
		opts.StartLabel = in.TargetChanges[0].Name
		for _, tc := range in.TargetChanges[1:] {
			opts.Dividers = append(opts.Dividers, Divider{M: toMin(tc.At), Label: "→ " + tc.Name})
		}
	}
	indexChart := RenderChart(series, opts)

	var lumPts []Pt
	for _, s := range in.Samples {
		lumPts = append(lumPts, Pt{M: toMin(s.At), V: s.Luminance})
	}
	lumSeries := []Series{{Name: "Sky brightness", Label: "all-sky", Color: "var(--series-3)", Pts: lumPts}}
	lumChart := RenderChart(lumSeries, ChartOpts{
		H: 210, YMax: lumYMax(lumPts), YTicks: 4, GapMin: 6, YLabel: "Mean sky luminance (0–255)",
		SpanMin: span, TickStart: tickOff, TickLabel: tickLabel,
		YFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) },
	})

	verdict, stats, eventNote := narrate(in)

	data := pageData{
		Title:            "Sky transparency — night of " + NightLabel(in.NightOf),
		Verdict:          template.HTML(verdict),
		Stats:            stats,
		Legend:           legend,
		IndexChart:       template.HTML(indexChart),
		LumChart:         template.HTML(lumChart),
		MultiTarget:      len(in.TargetChanges) > 1,
		BaselineModeNote: baselineNote(in.BaselineMode),
		HasOnset:         in.HasOnset,
		OnsetHM:          hm(in.Onset),
		Shots:            pickShots(in),
		Baselines:        baselineRows(in.Baselines),
		BaselineFallback: baselineFallbackNote(in.Baselines),
		RoofNote:         in.RoofNote,
		Events:           eventRows(in.Events),
		EventNote:        eventNote,
		Frames:           frameRows(in),
		FlatCount:        len(in.Flats),
		Foot: "Star counts come from Hocus Focus detection on each saved sub, tagged with the filter in place " +
			"at exposure start and the target then being imaged. Frames inside completed autofocus runs are excluded. " +
			"Still capture times come from the AllSky filenames. All times are local.",
		CSS:        template.CSS(reportCSS),
		ChartsJSON: chartsJSON(origin, span, series, lumSeries),
		NavTop:     template.HTML(navBlock("top", in.NightOf, "", "")),
		NavBottom:  template.HTML(navBlock("bottom", in.NightOf, "", "")),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func chartSpan(in Input) (time.Time, float64) {
	var lo, hi time.Time
	seen := func(t time.Time) {
		if lo.IsZero() || t.Before(lo) {
			lo = t
		}
		if hi.IsZero() || t.After(hi) {
			hi = t
		}
	}
	for _, f := range in.Frames {
		seen(f.At)
	}
	for _, s := range in.Samples {
		seen(s.At)
	}
	for _, e := range in.Events {
		seen(e.At)
	}
	if lo.IsZero() {
		lo = time.Now()
		hi = lo.Add(time.Hour)
	}
	origin := lo.Truncate(15 * time.Minute)
	span := math.Ceil(hi.Sub(origin).Minutes()/15) * 15
	if span < 60 {
		span = 60
	}
	return origin, span
}

func lumYMax(pts []Pt) float64 {
	max := 0.0
	for _, p := range pts {
		if p.V > max {
			max = p.V
		}
	}
	// round up to a clean 20; luminance is 0–255 but a whole-range axis
	// buries the clear-sky variation.
	return math.Max(40, math.Ceil(max/20)*20)
}

// NightLabel renders a night as the report titles it — "10–11 September
// 2026" — from a YYYY-MM-DD evening date. The live page uses it too so
// both surfaces name a night the same way.
func NightLabel(nightOf string) string {
	t, err := time.Parse("2006-01-02", nightOf)
	if err != nil {
		return nightOf
	}
	next := t.AddDate(0, 0, 1)
	if t.Month() == next.Month() {
		return fmt.Sprintf("%d–%d %s %d", t.Day(), next.Day(), t.Format("January"), t.Year())
	}
	return fmt.Sprintf("%s – %s", t.Format("2 January"), next.Format("2 January 2006"))
}

func narrate(in Input) (string, []stat, string) {
	light := 0
	lightBefore := 0
	for _, f := range in.Frames {
		if f.Class() != ninalog.ClassLight {
			continue
		}
		light++
		if !in.HasOnset || f.At.Before(in.Onset) {
			lightBefore++
		}
	}
	var beforeIdx, afterIdx []float64
	for _, f := range in.Frames {
		if f.Class() != ninalog.ClassLight {
			continue
		}
		idx, ok := analysis.Index(f, in.Baselines)
		if !ok {
			continue
		}
		if !in.HasOnset || f.At.Before(in.Onset) {
			beforeIdx = append(beforeIdx, idx)
		} else {
			afterIdx = append(afterIdx, idx)
		}
	}
	b := analysis.Summarise(beforeIdx)
	a := analysis.Summarise(afterIdx)

	causes := map[analysis.Cause]int{}
	afterOnset := 0
	failures := 0
	for _, e := range in.Events {
		if e.Kind != ninalog.SolveFailed && e.Kind != ninalog.PHD2Error {
			continue
		}
		failures++
		causes[e.Cause]++
		if in.HasOnset && !e.At.Before(in.Onset) {
			afterOnset++
		}
	}

	var v strings.Builder
	var stats []stat
	if in.HasOnset {
		fmt.Fprintf(&v, "Cloud arrived at %s — detected by the all-sky camera, independent of N.I.N.A. ", in.Onset.Format("15:04"))
		fmt.Fprintf(&v, "Before it the sky ran at index %.0f (IQR %.0f–%.0f); after it %.0f (IQR %.0f–%.0f). ",
			b.Median, b.Q1, b.Q3, a.Median, a.Q1, a.Q3)
		fmt.Fprintf(&v, "%d of %d light frames landed before the cloud. ", lightBefore, light)
		if failures > 0 {
			fmt.Fprintf(&v, "%d of %d failures are attributed to cloud", causes[analysis.CauseCloud], failures)
			if causes[analysis.CausePostSlew] > 0 {
				fmt.Fprintf(&v, ", %d to post-slew guide-star acquisition", causes[analysis.CausePostSlew])
			}
			if causes[analysis.CauseUnexplained] > 0 {
				fmt.Fprintf(&v, "; %d remain unexplained and deserve a look", causes[analysis.CauseUnexplained])
			}
			v.WriteString(". ")
		}
		stats = append(stats,
			stat{K: "Index before " + in.Onset.Format("15:04"), V: fmt.Sprintf("%.0f", b.Median), Small: fmt.Sprintf("IQR %.0f–%.0f", b.Q1, b.Q3)},
			stat{K: "Index after " + in.Onset.Format("15:04"), V: fmt.Sprintf("%.0f", a.Median), Small: fmt.Sprintf("IQR %.0f–%.0f", a.Q1, a.Q3)},
			stat{K: "Cloud onset (all-sky)", V: in.Onset.Format("15:04"), Small: "independent"},
			stat{K: "Errors after onset", V: fmt.Sprintf("%d of %d", afterOnset, failures), Small: "solve + PHD2"},
		)
	} else {
		fmt.Fprintf(&v, "No cloud onset detected. The transparency index held at %.0f (IQR %.0f–%.0f) across %d light frames. ",
			b.Median, b.Q1, b.Q3, light)
		stats = append(stats,
			stat{K: "Transparency index", V: fmt.Sprintf("%.0f", b.Median), Small: fmt.Sprintf("IQR %.0f–%.0f", b.Q1, b.Q3)},
			stat{K: "Light frames", V: fmt.Sprintf("%d", light), Small: ""},
			stat{K: "Autofocus runs", V: fmt.Sprintf("%d", len(in.AFRuns)), Small: ""},
			stat{K: "Failures", V: fmt.Sprintf("%d", failures), Small: "solve + PHD2"},
		)
	}
	for _, r := range in.Untrustworthy {
		fmt.Fprintf(&v, "The %s autofocus run fitted its curve on almost no stars and should not be trusted. ", r.Start.Format("15:04"))
	}
	if s := flatsSentence(in); s != "" {
		v.WriteString(s)
	}

	note := "All observatory events with their attributed causes."
	if causes[analysis.CausePostSlew] == 1 && in.HasOnset {
		note = "Only the post-slew event predates the cloud — PHD2 failing to settle on a new guide star after a slew, not a sky problem."
	}
	return v.String(), stats, note
}

// flatsSentence summarises the morning sky-flats session for the verdict.
// Flats never touch the index; this is a checklist item, so you find out a
// session quietly failed before stacking, not after.
func flatsSentence(in Input) string {
	if len(in.Flats) == 0 {
		return ""
	}
	byFilter := map[string]int{}
	var order []string
	for _, fl := range in.Flats {
		if byFilter[fl.Filter] == 0 {
			order = append(order, fl.Filter)
		}
		byFilter[fl.Filter]++
	}
	var parts []string
	for _, f := range order {
		parts = append(parts, fmt.Sprintf("%s %d", f, byFilter[f]))
	}
	var s strings.Builder
	fmt.Fprintf(&s, "Morning sky flats: %d frames (%s), %s–%s",
		len(in.Flats), strings.Join(parts, ", "),
		in.Flats[0].At.Format("15:04"), in.Flats[len(in.Flats)-1].At.Format("15:04"))
	if len(in.FlatExposures) > 0 {
		lo, hi := in.FlatExposures[0], in.FlatExposures[0]
		for _, e := range in.FlatExposures {
			lo, hi = math.Min(lo, e), math.Max(hi, e)
		}
		fmt.Fprintf(&s, ", auto-exposure %.1f–%.1f s", lo, hi)
	}
	s.WriteString(". ")
	if in.FlatsUnstableSky {
		s.WriteString("The all-sky camera saw unstable sky during the flats — check them for gradients before use. ")
	}
	return s.String()
}

func baselineNote(mode string) string {
	if mode == "historical" {
		return "rolling median over recent clear nights"
	}
	return "this night's own pre-cloud median"
}

func baselineSource(src string) string {
	switch src {
	case analysis.SourceHistorical:
		return "earlier clear nights"
	case analysis.SourceWholeNight:
		return "whole night, cloud included"
	}
	return "tonight, clear frames"
}

// baselineFallbackNote explains any divisor that is not a clear-sky median.
func baselineFallbackNote(base map[analysis.BaselineKey]analysis.Baseline) string {
	var relative []string
	seen := map[string]bool{}
	for k, v := range base {
		if v.Source == analysis.SourceWholeNight && !seen[k.Target] {
			seen[k.Target] = true
			relative = append(relative, k.Target)
		}
	}
	if len(relative) == 0 {
		return ""
	}
	sort.Strings(relative)
	return fmt.Sprintf("%s had no clear frames tonight and no clear history, so its divisor is its own whole-night median. "+
		"Its index shows how the sky changed during that target, not how it compared with a clear sky.",
		strings.Join(relative, " and "))
}

func baselineRows(base map[analysis.BaselineKey]analysis.Baseline) []baselineRow {
	var rows []baselineRow
	for k, v := range base {
		rows = append(rows, baselineRow{Target: k.Target, Filter: k.Filter,
			Median: fmt.Sprintf("%.0f", v.MedianStars), N: v.NFrames, Source: baselineSource(v.Source)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Target != rows[j].Target {
			return rows[i].Target < rows[j].Target
		}
		return rows[i].Filter < rows[j].Filter
	})
	return rows
}

func eventRows(events []analysis.AttributedEvent) []eventRow {
	kinds := map[ninalog.EventKind]string{
		ninalog.SolveFailed:       "solve failed",
		ninalog.PHD2Error:         "PHD2",
		ninalog.AutofocusRejected: "AF rejected",
		ninalog.AutofocusFailed:   "AF failed",
		ninalog.DomeRefused:       "dome refused",
		ninalog.FlatsFailed:       "flats failed",
	}
	var rows []eventRow
	for _, e := range events {
		rows = append(rows, eventRow{
			Time: e.At.Format("15:04:05"), Kind: kinds[e.Kind],
			Detail: strings.TrimSpace(e.Detail), Cause: string(e.Cause),
		})
	}
	return rows
}

func frameRows(in Input) []frameRow {
	var rows []frameRow
	for _, f := range in.Frames {
		idx := "—"
		if f.Class() == ninalog.ClassLight {
			if v, ok := analysis.Index(f, in.Baselines); ok {
				idx = fmt.Sprintf("%.0f%%", v)
			}
		}
		rows = append(rows, frameRow{
			Time: f.At.Format("15:04:05"), Class: string(f.Class()), Target: f.Target,
			Filter: f.Filter, Exp: fmt.Sprintf("%.0fs", f.ExposureSec),
			Stars: fmt.Sprintf("%d", f.DetectedStars), HFR: fmt.Sprintf("%.2f", f.HFR), Index: idx,
		})
	}
	return rows
}

// chart config consumed by the hover script.
type chartJSON struct {
	Svg    string       `json:"svg"`
	Box    string       `json:"box"`
	Tip    string       `json:"tip"`
	H      int          `json:"h"`
	Rail   bool         `json:"rail"`
	YMax   float64      `json:"ymax"`
	Span   float64      `json:"span"`
	Origin float64      `json:"o"` // minutes since midnight of the origin day
	Fmt    string       `json:"fmt"`
	Series []seriesJSON `json:"series"`
}
type seriesJSON struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Pts   []Pt   `json:"pts"`
}

func chartsJSON(origin time.Time, span float64, index, lum []Series) template.JS {
	o := float64(origin.Hour()*60 + origin.Minute())
	mk := func(ss []Series) []seriesJSON {
		out := make([]seriesJSON, 0, len(ss))
		for _, s := range ss {
			out = append(out, seriesJSON{Name: s.Name, Color: s.Color, Pts: s.Pts})
		}
		return out
	}
	lumYM := 40.0
	for _, s := range lum {
		for _, p := range s.Pts {
			lumYM = math.Max(lumYM, math.Ceil(p.V/20)*20)
		}
	}
	charts := []chartJSON{
		{Svg: "c1", Box: "b1", Tip: "t1", H: 320, Rail: true, YMax: 200, Span: span, Origin: o, Fmt: "pct", Series: mk(index)},
		{Svg: "c2", Box: "b2", Tip: "t2", H: 210, Rail: false, YMax: lumYM, Span: span, Origin: o, Fmt: "lum", Series: mk(lum)},
	}
	j, err := json.Marshal(charts)
	if err != nil {
		return "[]"
	}
	return template.JS(j)
}
