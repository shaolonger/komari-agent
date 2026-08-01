package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This fixture represents a disconnected Agent long enough to exceed the
// frame-count budget. Runtime and disk use must stay flat after the boundary.
func TestDisconnectedSpoolStaysBoundedAcrossLongLogicalOutage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.spool")
	spool, err := openTelemetrySpool(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 128)
	total := uint64(spoolMaximumFrames + 257)
	for sequence := uint64(1); sequence <= total; sequence++ {
		payload[0] = byte(sequence)
		if err := spool.Add(sequence, payload, time.Now()); err != nil {
			t.Fatalf("add sequence %d: %v", sequence, err)
		}
	}
	pending := spool.Pending()
	if len(pending) != spoolMaximumFrames {
		t.Fatalf("pending frames = %d, want %d", len(pending), spoolMaximumFrames)
	}
	if pending[0].Sequence != total-spoolMaximumFrames+1 || pending[len(pending)-1].Sequence != total {
		t.Fatalf("retained sequence range = %d..%d", pending[0].Sequence, pending[len(pending)-1].Sequence)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > int64(spoolMaximumBytes*2) {
		t.Fatalf("physical spool = %d bytes, hard envelope = %d", info.Size(), spoolMaximumBytes*2)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
}
