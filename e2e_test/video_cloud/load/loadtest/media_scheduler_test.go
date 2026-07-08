package loadtest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestMediaSchedulerDropsWhenWorkerQueueFull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var logsMu sync.Mutex
	var logs []string
	pacer := newMediaPacer(mediaPacerConfig{
		Tick:             time.Millisecond,
		WheelSize:        16,
		WorkerCount:      1,
		QueueCapacity:    1,
		MaxPacketsPerJob: 1,
		Logf: func(format string, args ...any) {
			logsMu.Lock()
			defer logsMu.Unlock()
			logs = append(logs, mediaSchedulerLogLine(format, args...))
		},
	})
	defer pacer.Stop()

	blockWriter := make(chan struct{})
	write := func(*rtp.Packet) error {
		<-blockWriter
		return nil
	}
	frames := [][]*rtp.Packet{{{Header: rtp.Header{Version: 2}, Payload: []byte{1}}}}

	const senders = 8
	statsCh := make(chan mediaPacedSendStats, senders)
	for i := 0; i < senders; i++ {
		go func() {
			stats, err := pacer.Send(ctx, mediaPacedSendRequest{
				Label:    "test",
				Frames:   frames,
				Duration: time.Millisecond,
				WriteRTP: write,
			})
			if err != nil {
				t.Errorf("Send returned error: %v", err)
				return
			}
			statsCh <- stats
		}()
	}

	deadline := time.After(500 * time.Millisecond)
	var dropped mediaPacedSendStats
	for dropped.QueueFullDrops == 0 {
		select {
		case stats := <-statsCh:
			dropped.DroppedJobs += stats.DroppedJobs
			dropped.DroppedPackets += stats.DroppedPackets
			dropped.QueueFullDrops += stats.QueueFullDrops
		case <-deadline:
			t.Fatalf("scheduler did not report queue-full drops")
		}
	}
	close(blockWriter)

	for i := 0; i < senders-1; i++ {
		select {
		case stats := <-statsCh:
			dropped.DroppedJobs += stats.DroppedJobs
			dropped.DroppedPackets += stats.DroppedPackets
			dropped.QueueFullDrops += stats.QueueFullDrops
		case <-ctx.Done():
			t.Fatalf("timed out waiting for senders: %v", ctx.Err())
		}
	}
	if dropped.DroppedJobs == 0 || dropped.DroppedPackets == 0 {
		t.Fatalf("expected dropped jobs and packets, got %+v", dropped)
	}
	logsMu.Lock()
	defer logsMu.Unlock()
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "media_scheduler_queue_full") {
		t.Fatalf("expected queue-full log, got %q", joined)
	}
}

func TestMediaSchedulerPacesFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pacer := newMediaPacer(mediaPacerConfig{
		Tick:             time.Millisecond,
		WheelSize:        64,
		WorkerCount:      1,
		QueueCapacity:    8,
		MaxPacketsPerJob: 1,
	})
	defer pacer.Stop()

	var mu sync.Mutex
	var writes []time.Time
	frames := [][]*rtp.Packet{
		{{Header: rtp.Header{Version: 2}, Payload: []byte{1}}},
		{{Header: rtp.Header{Version: 2}, Payload: []byte{2}}},
		{{Header: rtp.Header{Version: 2}, Payload: []byte{3}}},
	}
	stats, err := pacer.Send(ctx, mediaPacedSendRequest{
		Label:    "paced",
		Frames:   frames,
		Duration: 30 * time.Millisecond,
		WriteRTP: func(*rtp.Packet) error {
			mu.Lock()
			writes = append(writes, time.Now())
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if stats.PacketsSent != len(frames) {
		t.Fatalf("PacketsSent = %d, want %d", stats.PacketsSent, len(frames))
	}
	if stats.QueueFullDrops != 0 {
		t.Fatalf("unexpected queue-full drops: %+v", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != len(frames) {
		t.Fatalf("writes = %d, want %d", len(writes), len(frames))
	}
	if gap := writes[1].Sub(writes[0]); gap < 5*time.Millisecond {
		t.Fatalf("frame gap too small: %s", gap)
	}
}

func TestMediaSchedulerDoesNotCountZeroByteWritesAsSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pacer := newMediaPacer(mediaPacerConfig{
		Tick:             time.Millisecond,
		WheelSize:        64,
		WorkerCount:      1,
		QueueCapacity:    8,
		MaxPacketsPerJob: 1,
	})
	defer pacer.Stop()

	writes := 0
	frames := [][]*rtp.Packet{
		{{Header: rtp.Header{Version: 2}, Payload: []byte{1, 2, 3}}},
	}
	stats, err := pacer.Send(ctx, mediaPacedSendRequest{
		Label:    "paced",
		Frames:   frames,
		Duration: 10 * time.Millisecond,
		WriteRTPBytes: func(*rtp.Packet) (int, error) {
			writes++
			if writes < 3 {
				return 0, nil
			}
			return len(frames[0][0].Payload), nil
		},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if writes != 3 {
		t.Fatalf("writes = %d, want 3", writes)
	}
	if stats.PacketsSent != 1 {
		t.Fatalf("PacketsSent = %d, want 1 confirmed packet", stats.PacketsSent)
	}
	if stats.BytesSent != len(frames[0][0].Payload) {
		t.Fatalf("BytesSent = %d, want %d", stats.BytesSent, len(frames[0][0].Payload))
	}
	if stats.FirstWriteAt.IsZero() {
		t.Fatalf("expected FirstWriteAt after confirmed byte write")
	}
}
