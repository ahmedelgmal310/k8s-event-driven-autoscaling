package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunWritesSVG(t *testing.T) {
	dir := t.TempDir()
	in := write(t, dir, "timeline.csv", "t,api_desired,api_ready,rps\n0,2,2,20\n2,4,2,150\n4,8,6,300\n")
	markers := write(t, dir, "markers.csv", "t,label\n2,ramp\nbad,row\n3\n")
	out := filepath.Join(dir, "out.svg")
	err := run([]string{
		"-in", in, "-out", out, "-title", "API",
		"-left", "api_desired=Desired,api_ready=Ready~", "-right", "rps", "-right-label", "req/s",
		"-markers", markers,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	svg, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Desired", "Ready", "rps", "ramp", "stroke-dasharray"} {
		if !strings.Contains(string(svg), want) {
			t.Errorf("svg missing %q", want)
		}
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	in := write(t, dir, "timeline.csv", "t,a\n0,1\n1,2\n")
	out := filepath.Join(dir, "out.svg")
	cases := [][]string{
		{},
		{"-bogus"},
		{"-in", filepath.Join(dir, "missing.csv"), "-out", out, "-left", "a"},
		{"-in", write(t, dir, "empty.csv", "t\n"), "-out", out, "-left", "a"},
		{"-in", in, "-out", out, "-left", "a", "-x", "nope"},
		{"-in", in, "-out", out, "-left", "nope"},
		{"-in", in, "-out", out, "-left", "a", "-right", "nope"},
		{"-in", in, "-out", out, "-left", "a", "-markers", filepath.Join(dir, "missing.csv")},
		{"-in", in, "-out", out, "-left", "a", "-markers", write(t, dir, "bad.csv", "a,\"b\n")},
		{"-in", write(t, dir, "one.csv", "t,a\n0,1\n"), "-out", out, "-left", "a"},
	}
	for _, args := range cases {
		if err := run(args, io.Discard); err == nil {
			t.Errorf("run(%q) should fail", args)
		}
	}
}
