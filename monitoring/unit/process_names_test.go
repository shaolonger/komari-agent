package monitoring

import (
	"fmt"
	"testing"
)

func TestIsDecimalPID(t *testing.T) {
	tests := map[string]bool{
		"":       false,
		"1":      true,
		"000042": true,
		"123456": true,
		"-1":     false,
		"12a":    false,
		"self":   false,
		"1/2":    false,
	}
	for value, want := range tests {
		if got := isDecimalPID(value); got != want {
			t.Errorf("isDecimalPID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestProcessCountIsNonNegative(t *testing.T) {
	if got := ProcessCount(); got < 0 {
		t.Fatalf("ProcessCount = %d", got)
	}
}

func TestCountDecimalPIDsLargeFixture(t *testing.T) {
	const processes = 50_000
	names := make([]string, 0, processes+4)
	for index := range processes {
		names = append(names, fmt.Sprintf("%d", index+1))
	}
	names = append(names, "self", "thread-self", "sys", "1.invalid")
	if got := countDecimalPIDs(names); got != processes {
		t.Fatalf("countDecimalPIDs = %d, want %d", got, processes)
	}
}
