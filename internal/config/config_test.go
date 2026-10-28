package config

import (
	"testing"
	"time"
)

func env(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestString(t *testing.T) {
	e := env(map[string]string{"A": "x"})
	if got := String(e, "A", "d"); got != "x" {
		t.Fatalf("String(A) = %q", got)
	}
	if got := String(e, "B", "d"); got != "d" {
		t.Fatalf("String(B) = %q", got)
	}
}

func TestInt(t *testing.T) {
	e := env(map[string]string{"N": "42", "BAD": "x"})
	if n, err := Int(e, "N", 1); err != nil || n != 42 {
		t.Fatalf("Int(N) = %d, %v", n, err)
	}
	if n, err := Int(e, "MISSING", 7); err != nil || n != 7 {
		t.Fatalf("Int(MISSING) = %d, %v", n, err)
	}
	if _, err := Int(e, "BAD", 1); err == nil {
		t.Fatal("Int(BAD) should fail")
	}
}

func TestDuration(t *testing.T) {
	e := env(map[string]string{"D": "250ms", "BAD": "soon"})
	if d, err := Duration(e, "D", time.Second); err != nil || d != 250*time.Millisecond {
		t.Fatalf("Duration(D) = %v, %v", d, err)
	}
	if d, err := Duration(e, "MISSING", time.Second); err != nil || d != time.Second {
		t.Fatalf("Duration(MISSING) = %v, %v", d, err)
	}
	if _, err := Duration(e, "BAD", time.Second); err == nil {
		t.Fatal("Duration(BAD) should fail")
	}
}
