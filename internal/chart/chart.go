// Package chart renders the e2e time series (replicas, request rate, queue
// lag) as a dependency-free SVG line chart with an optional second y axis.
package chart

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"strconv"
	"strings"
)

// Series is one line on the chart.
type Series struct {
	Name   string
	Values []float64 // NaN leaves a gap
	Color  string
	// Right plots the series against the right-hand axis.
	Right bool
	// Step draws a step line, which suits integer counts such as replicas.
	Step   bool
	Dashed bool
}

// Marker is a labelled vertical line, for example "pod deleted".
type Marker struct {
	X     float64
	Label string
}

// Chart is a time-series chart with a shared x axis.
type Chart struct {
	Title      string
	XLabel     string
	LeftLabel  string
	RightLabel string
	X          []float64
	Series     []Series
	Markers    []Marker
	Width      int
	Height     int
}

// Palette is used for series without an explicit color (colorblind-friendly).
var Palette = []string{"#2563eb", "#16a34a", "#ea580c", "#9333ea", "#0891b2", "#dc2626"}

const (
	marginLeft   = 64.0
	marginRight  = 72.0
	marginTop    = 64.0
	marginBottom = 48.0
)

// SVG renders the chart.
func (c Chart) SVG() ([]byte, error) {
	if len(c.X) < 2 {
		return nil, errors.New("chart needs at least two x values")
	}
	for _, s := range c.Series {
		if len(s.Values) != len(c.X) {
			return nil, fmt.Errorf("series %q has %d values, x has %d", s.Name, len(s.Values), len(c.X))
		}
	}
	if c.Width == 0 {
		c.Width = 880
	}
	if c.Height == 0 {
		c.Height = 360
	}
	w, h := float64(c.Width), float64(c.Height)
	plotW, plotH := w-marginLeft-marginRight, h-marginTop-marginBottom

	xMin, xMax := minMax(c.X)
	if xMax == xMin {
		xMax = xMin + 1
	}
	leftMax, rightMax, hasRight := 0.0, 0.0, false
	for _, s := range c.Series {
		_, m := minMax(s.Values)
		if s.Right {
			hasRight = true
			rightMax = math.Max(rightMax, m)
		} else {
			leftMax = math.Max(leftMax, m)
		}
	}
	leftTicks := niceTicks(leftMax)
	rightTicks := niceTicks(rightMax)
	leftTop, rightTop := leftTicks[len(leftTicks)-1], rightTicks[len(rightTicks)-1]

	px := func(x float64) float64 { return marginLeft + (x-xMin)/(xMax-xMin)*plotW }
	py := func(v, top float64) float64 { return marginTop + plotH - v/top*plotH }

	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="-apple-system,Segoe UI,Helvetica,Arial,sans-serif" font-size="12">`+"\n", c.Width, c.Height, c.Width, c.Height)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="#ffffff"/>`+"\n")
	fmt.Fprintf(&b, `<text x="%g" y="22" font-size="15" font-weight="600" fill="#111827">%s</text>`+"\n", marginLeft, html.EscapeString(c.Title))

	// Legend.
	lx := marginLeft
	for i, s := range c.Series {
		color := seriesColor(s, i)
		dash := ""
		if s.Dashed {
			dash = ` stroke-dasharray="5 3"`
		}
		fmt.Fprintf(&b, `<line x1="%.1f" y1="40" x2="%.1f" y2="40" stroke="%s" stroke-width="2.5"%s/>`, lx, lx+18, color, dash)
		fmt.Fprintf(&b, `<text x="%.1f" y="44" fill="#374151">%s</text>`+"\n", lx+24, html.EscapeString(s.Name))
		lx += 24 + float64(len(s.Name))*6.6 + 22
	}

	// Grid and left axis.
	for _, t := range leftTicks {
		y := py(t, leftTop)
		fmt.Fprintf(&b, `<line x1="%g" y1="%.1f" x2="%g" y2="%.1f" stroke="#e5e7eb"/>`, marginLeft, y, marginLeft+plotW, y)
		fmt.Fprintf(&b, `<text x="%g" y="%.1f" text-anchor="end" fill="#6b7280">%s</text>`+"\n", marginLeft-8, y+4, formatTick(t))
	}
	if hasRight {
		for _, t := range rightTicks {
			y := py(t, rightTop)
			fmt.Fprintf(&b, `<text x="%g" y="%.1f" fill="#6b7280">%s</text>`+"\n", marginLeft+plotW+8, y+4, formatTick(t))
		}
		fmt.Fprintf(&b, `<text transform="translate(%g %g) rotate(90)" text-anchor="middle" fill="#374151">%s</text>`+"\n",
			w-14, marginTop+plotH/2, html.EscapeString(c.RightLabel))
	}
	fmt.Fprintf(&b, `<text transform="translate(16 %g) rotate(-90)" text-anchor="middle" fill="#374151">%s</text>`+"\n",
		marginTop+plotH/2, html.EscapeString(c.LeftLabel))

	// X axis.
	for _, t := range niceTicks(xMax - xMin) {
		x := px(xMin + t)
		if x > marginLeft+plotW+0.5 {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%g" x2="%.1f" y2="%g" stroke="#9ca3af"/>`, x, marginTop+plotH, x, marginTop+plotH+4)
		fmt.Fprintf(&b, `<text x="%.1f" y="%g" text-anchor="middle" fill="#6b7280">%s</text>`+"\n", x, marginTop+plotH+18, formatTick(t))
	}
	fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" stroke="#9ca3af"/>`+"\n", marginLeft, marginTop+plotH, marginLeft+plotW, marginTop+plotH)
	fmt.Fprintf(&b, `<text x="%g" y="%g" text-anchor="middle" fill="#374151">%s</text>`+"\n", marginLeft+plotW/2, h-8, html.EscapeString(c.XLabel))

	// Markers.
	for _, m := range c.Markers {
		if m.X < xMin || m.X > xMax {
			continue
		}
		x := px(m.X)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%g" x2="%.1f" y2="%g" stroke="#6b7280" stroke-dasharray="3 3"/>`, x, marginTop, x, marginTop+plotH)
		fmt.Fprintf(&b, `<text x="%.1f" y="%g" fill="#4b5563" font-size="11">%s</text>`+"\n", x+4, marginTop+12, html.EscapeString(m.Label))
	}

	// Series.
	for i, s := range c.Series {
		top := leftTop
		if s.Right {
			top = rightTop
		}
		for _, seg := range segments(c.X, s.Values) {
			var pts []string
			for j, p := range seg {
				x, y := px(p[0]), py(p[1], top)
				if s.Step && j > 0 {
					pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, py(seg[j-1][1], top)))
				}
				pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
			}
			dash := ""
			if s.Dashed {
				dash = ` stroke-dasharray="5 3"`
			}
			fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2.2" stroke-linejoin="round"%s points="%s"/>`+"\n",
				seriesColor(s, i), dash, strings.Join(pts, " "))
		}
	}
	b.WriteString("</svg>\n")
	return b.Bytes(), nil
}

func seriesColor(s Series, i int) string {
	if s.Color != "" {
		return s.Color
	}
	return Palette[i%len(Palette)]
}

// segments splits a series into runs of non-NaN points.
func segments(xs, ys []float64) [][][2]float64 {
	var out [][][2]float64
	var cur [][2]float64
	for i := range xs {
		if math.IsNaN(ys[i]) {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, [2]float64{xs[i], ys[i]})
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func minMax(vs []float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, v := range vs {
		if math.IsNaN(v) {
			continue
		}
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if math.IsInf(lo, 1) {
		return 0, 0
	}
	return lo, hi
}

// niceTicks returns 0 and round tick values covering max.
func niceTicks(maxV float64) []float64 {
	if maxV <= 0 {
		return []float64{0, 1}
	}
	raw := maxV / 5
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	var ticks []float64
	for t := 0.0; t < maxV+step-1e-9; t += step {
		ticks = append(ticks, t)
	}
	return ticks
}

func formatTick(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// ReadCSV reads a CSV with a header row into columns. Empty or unparsable
// cells become NaN.
func ReadCSV(r io.Reader) (map[string][]float64, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	if len(rows) < 2 {
		return nil, errors.New("csv needs a header and at least one row")
	}
	cols := make(map[string][]float64, len(rows[0]))
	for _, row := range rows[1:] {
		for i, name := range rows[0] {
			v := math.NaN()
			if i < len(row) {
				if f, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64); err == nil {
					v = f
				}
			}
			cols[name] = append(cols[name], v)
		}
	}
	return cols, nil
}
