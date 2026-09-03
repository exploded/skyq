package report

// Go port of the validated reference report's chart builder. The geometry,
// draw order and classes follow design/CHARTS.md exactly — change that file
// before changing this one. The rendered SVG must be indistinguishable from
// design/reference-report.html apart from the numbers.

import (
	"fmt"
	"math"
	"strings"
)

// Pt is one chart point: minutes from the chart origin, the y value, and
// (for the index chart) the raw star count and target for the tooltip.
type Pt struct {
	M      float64 `json:"m"`
	V      float64 `json:"v"`
	Stars  int     `json:"stars,omitempty"`
	Target string  `json:"target,omitempty"`
}

// Series is one line on a chart. Color is a CSS custom property reference,
// e.g. "var(--series-1)" — assigned by entity, never by rank (CHARTS.md
// rule 3).
type Series struct {
	Name  string
	Label string
	Color string
	Pts   []Pt
}

// EventMark is a failure tick on the event rail.
type EventMark struct {
	M    float64
	Kind string // "solve" → triangle/critical, anything else → diamond/warning
}

// Band is a shaded autofocus window.
type Band struct{ M0, M1 float64 }

// ChartOpts mirrors the reference builder's options object.
type ChartOpts struct {
	H            int     // viewBox height: 320 index, 210 luminance
	YMax         float64 // values clamp here rather than escaping the plot
	YTicks       int
	GapMin       float64 // start a new subpath when a gap exceeds this
	YLabel       string
	Rail         bool // event rail below the plot (B becomes 44)
	Ref          *float64
	RefLabel     string
	Divider      *float64
	DividerLabel string
	SpanMin      float64   // chart span in minutes
	TickStart    float64   // minutes offset of the first hour tick
	TickLabel    func(m float64) string
	Bands        []Band
	Events       []EventMark
	YFmt         func(v float64) string
}

const (
	chartW = 940
	padL   = 54
	padR   = 62
	padT   = 30
)

// RenderChart returns the inner SVG markup for one chart. The <svg> element
// itself lives in the page template so ids and classes stay in one place.
func RenderChart(series []Series, o ChartOpts) string {
	padB := 30
	if o.Rail {
		padB = 44
	}
	pw := float64(chartW - padL - padR)
	ph := float64(o.H - padT - padB)
	X := func(m float64) float64 { return padL + (m/o.SpanMin)*pw }
	Y := func(v float64) float64 { return padT + ph - (math.Min(v, o.YMax)/o.YMax)*ph }

	var b strings.Builder

	// 1. autofocus bands
	for _, band := range o.Bands {
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%.1f" fill="var(--band)"/>`,
			X(band.M0), padT, math.Max(2, X(band.M1)-X(band.M0)), ph)
	}
	// 2–3. gridlines + y tick labels
	for i := 0; i <= o.YTicks; i++ {
		v := o.YMax * float64(i) / float64(o.YTicks)
		fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="gl"/>`, padL, chartW-padR, Y(v), Y(v))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="tick" text-anchor="end">%s</text>`, padL-9, Y(v)+4, o.YFmt(v))
	}
	// 4. x ticks every 60 minutes
	for m := o.TickStart; m <= o.SpanMin; m += 60 {
		fmt.Fprintf(&b, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="ax"/>`, X(m), X(m), padT+ph, padT+ph+4)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="tick" text-anchor="middle">%s</text>`, X(m), padT+ph+16, o.TickLabel(m))
	}
	// 5. baseline
	fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="ax"/>`, padL, chartW-padR, padT+ph, padT+ph)
	// 6. axis label
	fmt.Fprintf(&b, `<text x="%d" y="%d" class="alab" text-anchor="start">%s</text>`, padL-9, padT-11, esc(o.YLabel))
	// 7. reference rules
	if o.Ref != nil {
		fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="refline"/>`, padL, chartW-padR, Y(*o.Ref), Y(*o.Ref))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="reftxt" text-anchor="end">%s</text>`, chartW-padR-2, Y(*o.Ref)-6, esc(o.RefLabel))
	}
	if o.Divider != nil {
		fmt.Fprintf(&b, `<line x1="%.1f" x2="%.1f" y1="%d" y2="%.1f" class="refline"/>`, X(*o.Divider), X(*o.Divider), padT, padT+ph)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="reftxt">%s</text>`, X(*o.Divider)+5, padT+11, esc(o.DividerLabel))
	}
	// 8–9. series: line, markers, direct label
	for _, s := range series {
		if len(s.Pts) == 0 {
			continue
		}
		var d strings.Builder
		prev := math.Inf(-1)
		for _, p := range s.Pts {
			cmd := " L"
			if math.IsInf(prev, -1) || p.M-prev > o.GapMin {
				cmd = " M"
			}
			fmt.Fprintf(&d, "%s%.1f %.1f", cmd, X(p.M), Y(p.V))
			prev = p.M
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`,
			strings.TrimSpace(d.String()), s.Color)
		if len(s.Pts) <= 150 {
			for _, p := range s.Pts {
				fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.8" fill="%s" stroke="var(--surface-1)" stroke-width="1.5"/>`,
					X(p.M), Y(p.V), s.Color)
			}
		}
		last := s.Pts[len(s.Pts)-1]
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="dl" fill="%s">%s</text>`,
			math.Min(chartW-3, X(last.M)+9), Y(last.V)+4, s.Color, esc(s.Label))
	}
	// 10. event rail
	if o.Rail {
		for _, e := range o.Events {
			y := padT + ph + 24
			color := "var(--warning)"
			if e.Kind == "solve" {
				color = "var(--critical)"
			}
			x := X(e.M)
			if e.Kind == "solve" {
				fmt.Fprintf(&b, `<path d="M%.1f %.1f L%.1f %.1f L%.1f %.1f Z" fill="%s"/>`,
					x, y-5, x+5, y+4, x-5, y+4, color)
			} else {
				fmt.Fprintf(&b, `<path d="M%.1f %.1f L%.1f %.1f L%.1f %.1f L%.1f %.1f Z" fill="%s"/>`,
					x, y-5, x+5, y, x, y+5, x-5, y, color)
			}
			fmt.Fprintf(&b, `<line x1="%.1f" x2="%.1f" y1="%d" y2="%.1f" stroke="%s" stroke-width="1" stroke-dasharray="2 3" opacity=".45"/>`,
				x, x, padT, padT+ph, color)
		}
	}
	// 11. crosshair + halo, hidden until hover; the hover script finds them
	// as the last line and circle in the SVG.
	fmt.Fprintf(&b, `<line x1="0" x2="0" y1="%d" y2="%.1f" class="ax cross" opacity="0"/>`, padT, padT+ph)
	b.WriteString(`<circle r="5.5" fill="none" stroke-width="2" class="halo" opacity="0"/>`)
	return b.String()
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
