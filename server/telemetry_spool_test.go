package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTelemetrySpoolPersistsMode0600AndAcknowledgesDurably(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "telemetry.spool")
	now := time.Unix(1_700_000_000, 0)
	spool, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(1, []byte("frame-1"), now); err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(2, []byte("frame-2"), now); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spool mode = %v, err=%v", info.Mode().Perm(), err)
	}
	if err := spool.Ack(1); err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending := reopened.Pending()
	if len(pending) != 1 || pending[0].Sequence != 2 || string(pending[0].Payload) != "frame-2" || reopened.NextSequence() != 3 {
		t.Fatalf("reloaded pending = %#v, next=%d", pending, reopened.NextSequence())
	}
	if err := reopened.Ack(99); err == nil {
		t.Fatal("future acknowledgement was accepted")
	}
}

func TestTelemetrySpoolQuarantinesCorruptionAndDropsStaleRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "telemetry.spool")
	now := time.Unix(1_700_000_000, 0)
	spool, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(1, []byte("stale"), now.Add(-spoolMaximumAge-time.Second)); err != nil {
		t.Fatal(err)
	}
	_ = spool.Close()
	reopened, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Pending()) != 0 {
		t.Fatal("stale record was replayed")
	}
	_ = reopened.Close()
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer clean.Close()
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 || len(clean.Pending()) != 0 {
		t.Fatalf("quarantine=%#v pending=%#v", matches, clean.Pending())
	}
}

func TestTelemetrySpoolReturnsOwnedSortedPendingFrames(t *testing.T) {
	spool, err := openTelemetrySpool(filepath.Join(t.TempDir(), "spool"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	_ = spool.Add(1, []byte("one"), time.Now())
	_ = spool.Add(2, []byte("two"), time.Now())
	first := spool.Pending()
	first[0].Payload[0] = 'X'
	second := spool.Pending()
	if !reflect.DeepEqual([]string{string(second[0].Payload), string(second[1].Payload)}, []string{"one", "two"}) {
		t.Fatalf("pending aliases internal storage: %#v", second)
	}
	if err := spool.Add(3, make([]byte, (64<<10)+1), time.Now()); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
