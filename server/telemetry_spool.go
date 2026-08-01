package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	spoolRecordMagic   = uint32(0x3150534b) // KSP1
	spoolRecordHeader  = 32
	spoolRecordAdd     = byte(1)
	spoolRecordAck     = byte(2)
	spoolMaximumFrames = 4096
	spoolMaximumBytes  = 4 << 20
	spoolMaximumAge    = 24 * time.Hour
)

type spooledFrame struct {
	Sequence  uint64
	CreatedAt time.Time
	Payload   []byte
}

type telemetrySpool struct {
	mu           sync.Mutex
	path         string
	file         *os.File
	pending      map[uint64]spooledFrame
	maximumSeen  uint64
	payloadBytes int
	ackRecords   int
	now          func() time.Time
}

func openTelemetrySpool(path string, now func() time.Time) (*telemetrySpool, error) {
	if path == "" {
		return nil, errors.New("telemetry spool path is empty")
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("telemetry spool must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	spool := &telemetrySpool{path: path, pending: make(map[uint64]spooledFrame), now: now}
	if err := spool.openAndLoad(); err != nil {
		quarantine := fmt.Sprintf("%s.corrupt-%d", path, now().Unix())
		_ = os.Rename(path, quarantine)
		spool.pending = make(map[uint64]spooledFrame)
		spool.maximumSeen = 0
		spool.payloadBytes = 0
		if reopenErr := spool.openEmpty(); reopenErr != nil {
			return nil, errors.Join(err, reopenErr)
		}
	}
	return spool, nil
}

func (spool *telemetrySpool) openAndLoad() error {
	file, err := os.OpenFile(spool.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := spool.load(file); err != nil {
		_ = file.Close()
		return err
	}
	spool.file = file
	return nil
}

func (spool *telemetrySpool) openEmpty() error {
	file, err := os.OpenFile(spool.path, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	spool.file = file
	return nil
}

func (spool *telemetrySpool) load(file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	cutoff := spool.now().Add(-spoolMaximumAge)
	for {
		header := make([]byte, spoolRecordHeader)
		_, err := io.ReadFull(file, header)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("truncated telemetry spool record")
		}
		if binary.LittleEndian.Uint32(header[:4]) != spoolRecordMagic {
			return errors.New("invalid telemetry spool magic")
		}
		kind := header[4]
		sequence := binary.LittleEndian.Uint64(header[8:16])
		createdAt := time.UnixMilli(int64(binary.LittleEndian.Uint64(header[16:24])))
		length := int(binary.LittleEndian.Uint32(header[24:28]))
		checksum := binary.LittleEndian.Uint32(header[28:32])
		if sequence == 0 || length < 0 || length > telemetryMaximumFrameSize(kind) {
			return errors.New("invalid telemetry spool record bounds")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(file, payload); err != nil || crc32.ChecksumIEEE(payload) != checksum {
			return errors.New("invalid telemetry spool checksum")
		}
		spool.maximumSeen = max(spool.maximumSeen, sequence)
		switch kind {
		case spoolRecordAdd:
			if createdAt.Before(cutoff) {
				continue
			}
			if previous, exists := spool.pending[sequence]; exists {
				spool.payloadBytes -= len(previous.Payload)
			}
			spool.pending[sequence] = spooledFrame{Sequence: sequence, CreatedAt: createdAt, Payload: payload}
			spool.payloadBytes += len(payload)
			if len(spool.pending) > spoolMaximumFrames || spool.payloadBytes > spoolMaximumBytes {
				return errors.New("telemetry spool exceeds configured bounds")
			}
		case spoolRecordAck:
			for pendingSequence, frame := range spool.pending {
				if pendingSequence <= sequence {
					spool.payloadBytes -= len(frame.Payload)
					delete(spool.pending, pendingSequence)
				}
			}
		default:
			return errors.New("invalid telemetry spool record type")
		}
	}
	_, err := file.Seek(0, io.SeekEnd)
	return err
}

func telemetryMaximumFrameSize(kind byte) int {
	if kind == spoolRecordAck {
		return 0
	}
	return 64 << 10
}

func (spool *telemetrySpool) Add(sequence uint64, payload []byte, createdAt time.Time) error {
	if sequence == 0 || len(payload) == 0 || len(payload) > telemetryMaximumFrameSize(spoolRecordAdd) {
		return errors.New("invalid telemetry spool frame")
	}
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if sequence <= spool.maximumSeen {
		return errors.New("telemetry sequence is not monotonic")
	}
	for len(spool.pending) >= spoolMaximumFrames || spool.payloadBytes+len(payload) > spoolMaximumBytes {
		if err := spool.dropOldestLocked(); err != nil {
			return err
		}
	}
	if err := spool.appendRecordLocked(spoolRecordAdd, sequence, createdAt, payload); err != nil {
		return err
	}
	owned := append([]byte(nil), payload...)
	spool.pending[sequence] = spooledFrame{Sequence: sequence, CreatedAt: createdAt, Payload: owned}
	spool.payloadBytes += len(owned)
	spool.maximumSeen = sequence
	return spool.maybeCompactLocked()
}

func (spool *telemetrySpool) Ack(through uint64) error {
	if through == 0 {
		return errors.New("ack sequence must be positive")
	}
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if through > spool.maximumSeen {
		return errors.New("ack sequence exceeds the highest emitted sequence")
	}
	if err := spool.appendRecordLocked(spoolRecordAck, through, spool.now(), nil); err != nil {
		return err
	}
	for sequence, frame := range spool.pending {
		if sequence <= through {
			spool.payloadBytes -= len(frame.Payload)
			delete(spool.pending, sequence)
		}
	}
	spool.ackRecords++
	return spool.maybeCompactLocked()
}

func (spool *telemetrySpool) Pending() []spooledFrame {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	result := make([]spooledFrame, 0, len(spool.pending))
	for _, frame := range spool.pending {
		frame.Payload = append([]byte(nil), frame.Payload...)
		result = append(result, frame)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Sequence < result[right].Sequence })
	return result
}

func (spool *telemetrySpool) NextSequence() uint64 {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	return spool.maximumSeen + 1
}

func (spool *telemetrySpool) Close() error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.file == nil {
		return nil
	}
	err := spool.file.Close()
	spool.file = nil
	return err
}

func (spool *telemetrySpool) appendRecordLocked(kind byte, sequence uint64, createdAt time.Time, payload []byte) error {
	header := make([]byte, spoolRecordHeader)
	binary.LittleEndian.PutUint32(header[:4], spoolRecordMagic)
	header[4] = kind
	binary.LittleEndian.PutUint64(header[8:16], sequence)
	binary.LittleEndian.PutUint64(header[16:24], uint64(createdAt.UnixMilli()))
	binary.LittleEndian.PutUint32(header[24:28], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[28:32], crc32.ChecksumIEEE(payload))
	if _, err := spool.file.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := spool.file.Write(payload); err != nil {
			return err
		}
	}
	return spool.file.Sync()
}

func (spool *telemetrySpool) dropOldestLocked() error {
	oldest := uint64(mathMaxUint64)
	for sequence := range spool.pending {
		oldest = min(oldest, sequence)
	}
	if frame, exists := spool.pending[oldest]; exists {
		spool.payloadBytes -= len(frame.Payload)
		delete(spool.pending, oldest)
		if err := spool.appendRecordLocked(spoolRecordAck, oldest, spool.now(), nil); err != nil {
			return err
		}
		spool.ackRecords++
	}
	return spool.maybeCompactLocked()
}

const mathMaxUint64 = ^uint64(0)

func (spool *telemetrySpool) maybeCompactLocked() error {
	info, _ := spool.file.Stat()
	physicalLimit := int64(max(spoolMaximumBytes*2, spool.payloadBytes*2+spoolRecordHeader))
	if spool.ackRecords >= 64 || info != nil && info.Size() > physicalLimit {
		return spool.compactLocked()
	}
	return nil
}

func (spool *telemetrySpool) compactLocked() error {
	temporary := spool.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	pending := make([]spooledFrame, 0, len(spool.pending))
	for _, frame := range spool.pending {
		pending = append(pending, frame)
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].Sequence < pending[right].Sequence })
	writeErr := error(nil)
	for _, frame := range pending {
		header := make([]byte, spoolRecordHeader)
		binary.LittleEndian.PutUint32(header[:4], spoolRecordMagic)
		header[4] = spoolRecordAdd
		binary.LittleEndian.PutUint64(header[8:16], frame.Sequence)
		binary.LittleEndian.PutUint64(header[16:24], uint64(frame.CreatedAt.UnixMilli()))
		binary.LittleEndian.PutUint32(header[24:28], uint32(len(frame.Payload)))
		binary.LittleEndian.PutUint32(header[28:32], crc32.ChecksumIEEE(frame.Payload))
		if _, writeErr = file.Write(header); writeErr == nil {
			_, writeErr = file.Write(frame.Payload)
		}
		if writeErr != nil {
			break
		}
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(writeErr, closeErr)
	}
	if err := spool.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, spool.path); err != nil {
		return err
	}
	spool.ackRecords = 0
	return spool.openAndLoadAfterCompact()
}

func (spool *telemetrySpool) openAndLoadAfterCompact() error {
	file, err := os.OpenFile(spool.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	spool.file = file
	return nil
}
