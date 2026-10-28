package cpuwork

import "testing"

func TestBurnIsDeterministic(t *testing.T) {
	if Burn(3) != Burn(3) {
		t.Fatal("Burn must be deterministic")
	}
	if Burn(1) == Burn(2) {
		t.Fatal("Burn must depend on the iteration count")
	}
	if got := len(Burn(0)); got != 16 {
		t.Fatalf("digest length = %d, want 16", got)
	}
}

func BenchmarkBurn5000(b *testing.B) {
	for b.Loop() {
		Burn(5000)
	}
}
