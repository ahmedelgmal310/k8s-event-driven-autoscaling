// Command chartgen turns an e2e timeline CSV into an SVG chart.
//
//	chartgen -in timeline.csv -out api.svg -title "API scale-out" \
//	  -left "api_desired=Desired replicas,api_ready=Ready replicas" -right "rps=Requests/s" \
//	  -markers markers.csv
//
// Left series are drawn as step lines (replica counts); right series as lines.
// A series name ending in "~" is drawn dashed. markers.csv has "t,label" rows.
package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/chart"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "chartgen:", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("chartgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "input CSV with a header row")
	out := fs.String("out", "", "output SVG")
	title := fs.String("title", "", "chart title")
	xcol := fs.String("x", "t", "x column")
	xlabel := fs.String("xlabel", "seconds since scenario start", "x axis label")
	left := fs.String("left", "", "left-axis series: col=Label,...")
	right := fs.String("right", "", "right-axis series: col=Label,...")
	leftLabel := fs.String("left-label", "replicas", "left axis label")
	rightLabel := fs.String("right-label", "", "right axis label")
	markers := fs.String("markers", "", "optional CSV of t,label markers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" || *left == "" {
		return errors.New("-in, -out and -left are required")
	}

	f, err := os.Open(*in) //nolint:gosec // reading the file named on the command line is the point
	if err != nil {
		return err
	}
	cols, err := chart.ReadCSV(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	xs, ok := cols[*xcol]
	if !ok {
		return fmt.Errorf("x column %q not in %s", *xcol, *in)
	}
	c := chart.Chart{Title: *title, XLabel: *xlabel, LeftLabel: *leftLabel, RightLabel: *rightLabel, X: xs}
	for _, side := range []struct {
		spec  string
		right bool
	}{{*left, false}, {*right, true}} {
		series, err := parseSeries(side.spec, cols, side.right)
		if err != nil {
			return err
		}
		c.Series = append(c.Series, series...)
	}
	if *markers != "" {
		if c.Markers, err = readMarkers(*markers); err != nil {
			return err
		}
	}
	svg, err := c.SVG()
	if err != nil {
		return err
	}
	return os.WriteFile(*out, svg, 0o600)
}

func parseSeries(spec string, cols map[string][]float64, right bool) ([]chart.Series, error) {
	var out []chart.Series
	if spec == "" {
		return out, nil
	}
	for _, item := range strings.Split(spec, ",") {
		col, label, found := strings.Cut(item, "=")
		if !found {
			label = col
		}
		dashed := strings.HasSuffix(label, "~")
		label = strings.TrimSuffix(label, "~")
		vals, ok := cols[col]
		if !ok {
			return nil, fmt.Errorf("column %q not found", col)
		}
		out = append(out, chart.Series{Name: label, Values: vals, Right: right, Step: !right, Dashed: dashed})
	}
	return out, nil
}

func readMarkers(path string) ([]chart.Marker, error) {
	f, err := os.Open(path) //nolint:gosec // reading the file named on the command line is the point
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("markers: %w", err)
	}
	var out []chart.Marker
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		x, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			continue // header or malformed row
		}
		out = append(out, chart.Marker{X: x, Label: row[1]})
	}
	return out, nil
}
