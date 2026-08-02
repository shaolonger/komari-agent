package monitoring

import (
	"fmt"
	"strings"
	"testing"
)

const procNetHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func TestCountProcNetTable(t *testing.T) {
	fixture := procNetHeader +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 1\n" +
		"\n" +
		"   1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 2\n"
	got, err := countProcNetTable(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("countProcNetTable failed: %v", err)
	}
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestCountProcNetTableLargeFixture(t *testing.T) {
	const rows = 25_000
	var fixture strings.Builder
	fixture.Grow(len(procNetHeader) + rows*96)
	fixture.WriteString(procNetHeader)
	for index := range rows {
		fmt.Fprintf(&fixture, "%5d: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 %d\n", index, index)
	}
	got, err := countProcNetTable(strings.NewReader(fixture.String()))
	if err != nil || got != rows {
		t.Fatalf("count = %d, err = %v, want %d", got, err, rows)
	}
}

func TestCountProcNetTableBoundsLineLength(t *testing.T) {
	fixture := procNetHeader + strings.Repeat("x", 70*1024) + "\n"
	if _, err := countProcNetTable(strings.NewReader(fixture)); err == nil {
		t.Fatal("oversized proc row was accepted")
	}
}
