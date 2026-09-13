# Chart construction rules

Everything needed to reproduce the report's charts in Go, without redesigning them.
Read this before writing any SVG. The companion files are `report.css` (all the tokens and
classes referenced here) and `reference-report.html` (a rendered, validated example — open it
in a browser and compare).

Charts are **inline SVG generated in Go**. No JS charting library, no external stylesheet, no
CDN. Each report is one self-contained file.

---

## The rules that are not negotiable

These come from a design-system validator, not from taste. Breaking one produces a chart that
is measurably harder to read.

1. **One y-axis per chart. Never a dual-axis chart.** Two measures on different scales get two
   stacked charts sharing an x-axis. The transparency index and the all-sky luminance are two
   charts for exactly this reason.
2. **Series colours are assigned in fixed slot order and never cycled.** H α = `--series-1`,
   O III = `--series-2`, S II = `--series-3`, L = `--series-4`, R = `--series-5`,
   G = `--series-6`, B = `--series-7`. Any filter outside that list folds into one "other"
   series on `--series-8` — never a generated hue. The slots are the first eight of the
   dataviz reference palette; the order passed colour-blind separation and contrast checks
   for adjacent pairs in both light and dark, and the LRGB four pass all-pairs. Do not
   substitute, reorder, or add a ninth hue. Slots 3–5 sit under 3:1 contrast on the light
   surface, which is why every line is direct-labelled and the frames table exists.
3. **Colour follows the entity, never its rank.** If a filter is missing from a night, the
   remaining series keep their own colours — do not repack them onto slots 1 and 2.
4. **Status colours are reserved.** `--critical` and `--warning` mark failures and are never
   reused as a series. They always ship with a shape (▲ / ◆) and a legend label, never colour
   alone.
5. **Legend always present for 2+ series**, and with ≤ 4 series also direct-label each line at
   its last point. Identity must never depend on colour alone.
6. **Text wears text tokens, not series colours** — axis ticks, captions and values stay in
   `--muted` / `--text-secondary`. The only coloured text is a direct series label.
7. **Never define a colour only inside a media query.** Light on bare `:root`, dark redefined
   under both `prefers-color-scheme` and `[data-theme="dark"]`.
8. **A dot on every point is only acceptable at low density.** Draw point markers when the
   series has ≤ 150 points; above that draw the line alone.

---

## Geometry

Both charts use the same viewBox width and margins so their x-axes align visually when
stacked.

```
viewBox   0 0 940 H          H = 320 (index chart), 210 (luminance chart)
left      L = 54             room for y-axis tick labels
right     R = 62             room for direct series labels outside the plot
top       T = 30             room for the axis label above the top gridline
bottom    B = 44 with an event rail, 30 without
plotW     940 - L - R
plotH     H - T - B
```

Scales:

```go
x := func(min float64) float64 { return L + (min/spanMinutes)*plotW }
y := func(v float64) float64   { return T + plotH - (math.Min(v, yMax)/yMax)*plotH }
```

`spanMinutes` is measured from a fixed chart origin (the prototype used 20:45 local) so both
charts and the event rail share one coordinate system. Clamp values at `yMax` rather than
letting a spike escape the plot.

---

## Draw order

Back to front. Getting this wrong buries the data under the chrome.

1. **Autofocus bands** — `<rect>` filled `var(--band)`, full plot height, one per AF window.
2. **Gridlines** — horizontal, `class="gl"`, at each y tick.
3. **Y tick labels** — `class="tick"`, `text-anchor="end"`, at `x = L-9`, `y = Y(v)+4`.
4. **X ticks** — a 4px stub below the baseline every 60 minutes, label `class="tick"`,
   `text-anchor="middle"` at `y = T+plotH+16`.
5. **Baseline** — horizontal rule at `y = T+plotH`, `class="ax"`.
6. **Axis label** — `class="alab"`, `text-anchor="start"`, at `x = L-9`, `y = T-11`. Above the
   top gridline, not beside it, or it collides with the topmost tick.
7. **Reference rules** — `class="refline"` (dashed). The index chart has a horizontal rule at
   100 labelled *clear-sky baseline* (right-aligned, 6px above the rule) and a vertical rule
   at each target change labelled with the new target name (5px right of the rule, near the
   top).
8. **Series** — line first, then markers on top. `stroke-width: 2`, round joins and caps.
   Markers `r=2.8`, filled with the series colour, with a `1.5px` stroke in `var(--surface-1)`
   so overlapping points stay separable.
9. **Direct labels** — `class="dl"`, `fill` = series colour, at the last point, `x+9`, `y+4`,
   clamped to stay inside the viewBox.
10. **Event rail** — see below.
11. **Crosshair and hover halo** — added last, `opacity: 0` until hover.

### Gap handling

A line must break rather than draw a straight run across a period with no data. Start a new
`M` subpath whenever the gap between consecutive points exceeds a threshold:

- index chart: **75 minutes** (a filter can legitimately go that long between subs in an
  H/O/S rotation)
- luminance chart: **6 minutes**

### Event rail

Failures sit in a strip below the plot at `y = T + plotH + 24`, so they never overlap the
data. Each event draws:

- a marker — triangle for a plate-solve failure (`--critical`), diamond for a PHD2 error
  (`--warning`)
- a faint dashed vertical line up through the plot: `stroke-width: 1`,
  `stroke-dasharray: "2 3"`, `opacity: .45`

```
triangle  M x,y-5  L x+5,y+4  L x-5,y+4  Z
diamond   M x,y-5  L x+5,y    L x,y+5    L x-5,y  Z
```

---

## Hover layer

An HTML/SVG chart is interactive by default — ship the hover. Attach `pointermove` to the
`.chartbox` div, not the SVG, so the hit area covers the padding.

On move: convert clientX to chart minutes, find the nearest point across **all** series,
ignore it if more than ~14 minutes away, then show the crosshair, a halo ring on the matched
point in that series' colour, and the `.tip` div. The tooltip carries timestamp and target on
the first line, then a colour swatch, the series name, the value, and the raw star count in
secondary ink.

Clamp the tooltip inside the container so it never causes horizontal overflow. Hide everything
on `pointerleave`.

---

## Accessibility checklist

Run through this before shipping a report:

- [ ] Legend present for every multi-series chart, plus direct labels
- [ ] Every failure marker has a distinct shape as well as a colour
- [ ] A table view exists for the underlying numbers (the collapsed `<details>` frame table)
- [ ] Both themes render correctly — check `data-theme="dark"` and OS dark mode separately
- [ ] Wide content scrolls inside `.chartbox`, never the page body
- [ ] No colour is defined only inside a media query

---

## Verifying the result

Do not ship a chart you have not looked at. The validator checks colour, not layout — label
collisions, overflow and geometry errors only show up on screen.

```bash
# render the generated report headlessly and eyeball both themes
chromium --headless --screenshot=light.png --window-size=1080,2400 report.html
```

Compare against `reference-report.html`, which is the validated original: same tokens, same
geometry, same draw order, real data from 2026-09-02.
