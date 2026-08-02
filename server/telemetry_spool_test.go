package server

import (
	"errors"
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
	if err := reopened.Ack(99); err != nil {
		t.Fatalf("authoritative server acknowledgement failed: %v", err)
	}
	if reopened.NextSequence() != 100 || len(reopened.Pending()) != 0 {
		t.Fatalf("server rebase next=%d pending=%#v", reopened.NextSequence(), reopened.Pending())
	}
}

func TestTelemetrySpoolQuarantinesCorruptionAndPreservesBoundedOfflineRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "telemetry.spool")
	now := time.Unix(1_700_000_000, 0)
	spool, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(1, []byte("offline"), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = spool.Close()
	reopened, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Pending()) != 1 || reopened.NextSequence() != 2 {
		t.Fatalf("bounded offline record was lost: pending=%#v next=%d", reopened.Pending(), reopened.NextSequence())
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

func TestTelemetrySpoolCompactionPersistsHighWaterAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.spool")
	now := time.Unix(1_700_000_000, 0)
	spool, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 64; sequence++ {
		if err := spool.Add(sequence, []byte("frame"), now); err != nil {
			t.Fatal(err)
		}
		if err := spool.Ack(sequence); err != nil {
			t.Fatal(err)
		}
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.NextSequence() != 65 || len(reopened.Pending()) != 0 {
		t.Fatalf("compacted high water was lost: next=%d pending=%#v", reopened.NextSequence(), reopened.Pending())
	}
}

func TestTelemetrySpoolRecoversValidPrefixAfterCrashTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.spool")
	now := time.Unix(1_700_000_000, 0)
	spool, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(1, []byte("durable-frame"), now); err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reopened, err := openTelemetrySpool(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending := reopened.Pending()
	if len(pending) != 1 || pending[0].Sequence != 1 || string(pending[0].Payload) != "durable-frame" || reopened.NextSequence() != 2 {
		t.Fatalf("recovered prefix = %#v next=%d", pending, reopened.NextSequence())
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 0 {
		t.Fatalf("recoverable crash tail was quarantined: %#v", matches)
	}
}

func TestTelemetrySpoolAppliesBackpressureWithoutCreatingSequenceGap(t *testing.T) {
	spool, err := openTelemetrySpool(filepath.Join(t.TempDir(), "spool"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	payload := make([]byte, 64<<10)
	for sequence := uint64(1); sequence <= 64; sequence++ {
		if err := spool.Add(sequence, payload, time.Now()); err != nil {
			t.Fatalf("fill sequence %d: %v", sequence, err)
		}
	}
	if err := spool.Add(65, payload, time.Now()); !errors.Is(err, ErrTelemetrySpoolFull) {
		t.Fatalf("overflow error = %v", err)
	}
	if pending := spool.Pending(); len(pending) != 64 || pending[0].Sequence != 1 || pending[len(pending)-1].Sequence != 64 {
		t.Fatalf("overflow changed contiguous pending prefix: %#v", pending)
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
