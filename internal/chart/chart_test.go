package chart

import (
	"encoding/xml"
	"math"
	"strings"
	"testing"
)

func TestReadCSV(t *testing.T) {
	cols, err := ReadCSV(strings.NewReader("t,a,b\n0,1,\n1,2,x\n2,3,4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cols["a"]; len(got) != 3 || got[2] != 3 {
		t.Fatalf("a = %v", got)
	}
	if !math.IsNaN(cols["b"][0]) || !math.IsNaN(cols["b"][1]) || cols["b"][2] != 4 {
		t.Fatalf("b = %v", cols["b"])
	}
	if _, err := ReadCSV(strings.NewReader("t\n")); err == nil {
		t.Fatal("header-only csv should fail")
	}
	if _, err := ReadCSV(strings.NewReader("a,b\n\"x\n")); err == nil {
		t.Fatal("malformed csv should fail")
	}
}

func TestSVGIsWellFormed(t *testing.T) {
	c := Chart{
		Title:      "API <replicas> & rps",
		XLabel:     "seconds",
		LeftLabel:  "replicas",
		RightLabel: "req/s",
		X:          []float64{0, 2, 4, 6, 8},
		Series: []Series{
			{Name: "desired", Values: []float64{2, 2, 4, 8, 8}, Step: true},
			{Name: "ready", Values: []float64{2, math.NaN(), 3, 6, 8}, Step: true, Dashed: true},
			{Name: "rps", Values: []float64{20, 100, 250, 300, 300}, Right: true, Color: "#000000"},
		},
		Markers: []Marker{{X: 4, Label: "pod deleted"}, {X: 99, Label: "out of range"}},
	}
	out, err := c.SVG()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"API &lt;replicas&gt; &amp; rps", "pod deleted", "req/s", `stroke="#000000"`, "stroke-dasharray"} {
		if !strings.Contains(s, want) {
			t.Errorf("svg missing %q", want)
		}
	}
	if strings.Contains(s, "out of range") {
		t.Error("marker outside the x range must be skipped")
	}
	// The NaN splits "ready" into two polylines: 3 series -> 4 polylines.
	if n := strings.Count(s, "<polyline"); n != 4 {
		t.Errorf("polylines = %d, want 4", n)
	}
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("svg is not well-formed XML: %v", err)
		}
	}
}

func TestSVGErrors(t *testing.T) {
	if _, err := (Chart{X: []float64{1}}).SVG(); err == nil {
		t.Fatal("one x value should fail")
	}
	if _, err := (Chart{X: []float64{1, 2}, Series: []Series{{Name: "s", Values: []float64{1}}}}).SVG(); err == nil {
		t.Fatal("length mismatch should fail")
	}
}

func TestFlatAndEmptySeries(t *testing.T) {
	c := Chart{X: []float64{5, 5}, Series: []Series{{Name: "zero", Values: []float64{math.NaN(), math.NaN()}}}}
	if _, err := c.SVG(); err != nil {
		t.Fatal(err)
	}
}

func TestNiceTicks(t *testing.T) {
	cases := map[float64][]float64{
		0:   {0, 1},
		10:  {0, 2, 4, 6, 8, 10},
		7:   {0, 2, 4, 6, 8},
		300: {0, 100, 200, 300},
		2.2: {0, 0.5, 1, 1.5, 2, 2.5},
	}
	for in, want := range cases {
		got := niceTicks(in)
		if len(got) != len(want) {
			t.Fatalf("niceTicks(%v) = %v, want %v", in, got, want)
		}
		for i := range got {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Fatalf("niceTicks(%v) = %v, want %v", in, got, want)
			}
		}
	}
	if formatTick(2.5) != "2.5" || formatTick(3) != "3" {
		t.Fatal("formatTick")
	}
}
