// Package server is the Phase 2 HTMX live page: one read-only view of the
// live engine, refreshed every 30 s. No forms, no buttons, no POST
// handlers — config and CLI are the only control surface, and nothing on a
// LAN page may gate or un-gate N.I.N.A. (SPEC §8).
package server

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/live"
	"github.com/exploded/skyq/internal/report"
)

//go:embed live.tmpl
var liveTmpl string

//go:embed static/htmx.min.js
var htmxJS []byte

var tmpl = template.Must(template.New("live").Parse(liveTmpl))

type Server struct {
	engine *live.Engine
	loc    *time.Location
}

func New(engine *live.Engine, loc *time.Location) *Server {
	return &Server{engine: engine, loc: loc}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page)
	mux.HandleFunc("GET /partial", s.partial)
	mux.HandleFunc("GET /still/latest", s.still)
	mux.HandleFunc("GET /static/js/htmx.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(htmxJS)
	})
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "page")
}

func (s *Server) partial(w http.ResponseWriter, r *http.Request) {
	s.render(w, "live")
}

// still serves the newest cached all-sky image. no-store: it changes every
// minute and the page cache-busts with a timestamp anyway.
func (s *Server) still(w http.ResponseWriter, r *http.Request) {
	snap := s.engine.Snapshot()
	if snap.LastStill == "" {
		http.Error(w, "no still yet", http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(snap.LastStill)
	if err != nil {
		http.Error(w, "still unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

type eventRow struct {
	Time, Kind, Detail, Cause string
}

type viewData struct {
	CSS        template.CSS
	StateLabel string
	StateClass string // ok | bad | unknown
	Reason     string
	Dark       string
	Vol        string
	Threshold  string
	Lum        string
	Index      string
	StillAge   string
	StillURL   string
	HasStill   bool
	Chart      template.HTML
	HasChart   bool
	Events     []eventRow
	FrameCount int
	Generated  string
}

func (s *Server) render(w http.ResponseWriter, name string) {
	snap := s.engine.Snapshot()
	d := viewData{
		CSS:       template.CSS(report.CSS()),
		Reason:    snap.Reason,
		Threshold: fmt.Sprintf("%.1f", snap.Threshold),
		Generated: snap.At.In(s.loc).Format("15:04:05"),
	}
	switch snap.Cloud {
	case live.StateClear:
		d.StateLabel, d.StateClass = "CLEAR", "ok"
	case live.StateCloudy:
		d.StateLabel, d.StateClass = "CLOUD", "bad"
	default:
		d.StateLabel, d.StateClass = "UNKNOWN", "unknown"
	}
	if snap.Dark {
		d.Dark = fmt.Sprintf("dark until %s", snap.DarkTo.Format("15:04"))
	} else if !snap.DarkFrom.IsZero() {
		d.Dark = fmt.Sprintf("dark %s–%s", snap.DarkFrom.Format("15:04"), snap.DarkTo.Format("15:04"))
	}
	if !math.IsNaN(snap.Volatility) {
		d.Vol = fmt.Sprintf("%.1f", snap.Volatility)
	} else {
		d.Vol = "—"
	}
	if !math.IsNaN(snap.Luminance) {
		d.Lum = fmt.Sprintf("%.1f", snap.Luminance)
	} else {
		d.Lum = "—"
	}
	if snap.IndexOK {
		d.Index = fmt.Sprintf("%.0f", snap.Index)
	} else {
		d.Index = "—"
	}
	if !snap.LastStillAt.IsZero() {
		d.StillAge = fmt.Sprintf("%.0fs ago", time.Since(snap.LastStillAt).Seconds())
		d.HasStill = snap.LastStill != ""
		d.StillURL = fmt.Sprintf("/still/latest?t=%d", snap.LastStillAt.Unix())
	}
	d.FrameCount = len(snap.Frames)
	for i := len(snap.Events) - 1; i >= 0 && len(d.Events) < 12; i-- {
		e := snap.Events[i]
		d.Events = append(d.Events, eventRow{
			Time: e.At.Format("15:04:05"), Kind: string(e.Kind),
			Detail: e.Detail, Cause: string(e.Cause),
		})
	}
	if len(snap.Samples) >= 2 {
		d.Chart = template.HTML(lumChart(snap.Samples))
		d.HasChart = true
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, d); err != nil {
		log.Printf("live page: template %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// lumChart renders tonight's luminance with the report's chart machinery,
// so live and morning views are visually the same system.
func lumChart(samples []analysis.LumSample) string {
	origin := samples[0].At.Truncate(15 * time.Minute)
	span := math.Max(60, math.Ceil(samples[len(samples)-1].At.Sub(origin).Minutes()/15)*15)
	toMin := func(t time.Time) float64 { return t.Sub(origin).Minutes() }

	var pts []report.Pt
	max := 40.0
	for _, s := range samples {
		pts = append(pts, report.Pt{M: toMin(s.At), V: s.Luminance})
		max = math.Max(max, math.Ceil(s.Luminance/20)*20)
	}
	firstHour := origin.Truncate(time.Hour).Add(time.Hour)
	return report.RenderChart(
		[]report.Series{{Name: "Sky brightness", Label: "all-sky", Color: "var(--series-3)", Pts: pts}},
		report.ChartOpts{
			H: 210, YMax: max, YTicks: 4, GapMin: 6,
			YLabel: "Mean sky luminance (0–255)", SpanMin: span,
			TickStart: firstHour.Sub(origin).Minutes(),
			TickLabel: func(m float64) string { return origin.Add(time.Duration(m * float64(time.Minute))).Format("15:04") },
			YFmt:      func(v float64) string { return fmt.Sprintf("%.0f", v) },
		})
}
