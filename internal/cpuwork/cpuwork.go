// Package cpuwork burns a predictable amount of CPU, so the per-request and
// per-job cost of the demo services is a tunable, measurable number.
package cpuwork

import (
	"crypto/sha256"
	"encoding/hex"
)

// Burn runs iterations chained SHA-256 rounds and returns a short digest.
func Burn(iterations int) string {
	sum := sha256.Sum256([]byte("k8s-event-driven-autoscaling"))
	for range iterations {
		sum = sha256.Sum256(sum[:])
	}
	return hex.EncodeToString(sum[:8])
}
