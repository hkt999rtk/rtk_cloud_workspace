package loadtest

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/pion/rtp"
)

const (
	defaultMediaSchedulerTick             = time.Millisecond
	defaultMediaSchedulerWheelSize        = 4096
	defaultMediaSchedulerQueueCapacity    = 8192
	defaultMediaSchedulerMaxPacketsPerJob = 32
)

type mediaPacedSendRequest struct {
	Label            string
	Frames           [][]*rtp.Packet
	Duration         time.Duration
	WriteRTP         func(*rtp.Packet) error
	WriteRTPBytes    func(*rtp.Packet) (int, error)
	MaxPacketsPerJob int
}

type mediaPacedSendStats struct {
	PacketsSent        int
	BytesSent          int
	DroppedJobs        int
	DroppedPackets     int
	QueueFullDrops     int
	ZeroByteWrites     int
	WriteAttempts      int
	WriteReturns       int
	WriteErrors        int
	StartedAt          time.Time
	FirstWriteCallAt   time.Time
	FirstWriteReturnAt time.Time
	FirstWriteAt       time.Time
	EndedAt            time.Time
	MaxWriteLatency    time.Duration
}

type mediaPacerConfig struct {
	Tick             time.Duration
	WheelSize        int
	WorkerCount      int
	QueueCapacity    int
	MaxPacketsPerJob int
	Logf             func(string, ...any)
}

type mediaPacer struct {
	cfg      mediaPacerConfig
	submitCh chan *mediaPacedTask
	workCh   chan mediaPacedWork
	resultCh chan mediaPacedResult
	stopCh   chan struct{}
	doneCh   chan struct{}
}

type mediaPacedTask struct {
	req         mediaPacedSendRequest
	ctx         context.Context
	done        chan mediaPacedResult
	stats       mediaPacedSendStats
	frameIndex  int
	packetIndex int
	nextDue     time.Time
	interval    time.Duration
}

type mediaPacedWork struct {
	task       *mediaPacedTask
	packets    []*rtp.Packet
	byteCount  int
	startIndex int
	endIndex   int
}

type mediaPacedResult struct {
	task       *mediaPacedTask
	stats      mediaPacedSendStats
	firstWrite time.Time
	endIndex   int
	nextIndex  int
	retry      bool
	err        error
}

var sharedMediaPacer = newMediaPacer(mediaPacerConfig{
	Tick:             envDuration("VIDEO_CLOUD_LOAD_MEDIA_SCHEDULER_TICK", defaultMediaSchedulerTick),
	WheelSize:        envInt("VIDEO_CLOUD_LOAD_MEDIA_SCHEDULER_WHEEL_SIZE", defaultMediaSchedulerWheelSize),
	WorkerCount:      envInt("VIDEO_CLOUD_LOAD_MEDIA_SCHEDULER_WORKERS", runtime.GOMAXPROCS(0)),
	QueueCapacity:    envInt("VIDEO_CLOUD_LOAD_MEDIA_SCHEDULER_QUEUE", defaultMediaSchedulerQueueCapacity),
	MaxPacketsPerJob: envInt("VIDEO_CLOUD_LOAD_MEDIA_SCHEDULER_MAX_PACKETS_PER_JOB", defaultMediaSchedulerMaxPacketsPerJob),
	Logf:             log.Printf,
})

func newMediaPacer(cfg mediaPacerConfig) *mediaPacer {
	if cfg.Tick <= 0 {
		cfg.Tick = defaultMediaSchedulerTick
	}
	if cfg.WheelSize <= 0 {
		cfg.WheelSize = defaultMediaSchedulerWheelSize
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 1
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = cfg.WorkerCount * 4
		if cfg.QueueCapacity < 16 {
			cfg.QueueCapacity = 16
		}
	}
	if cfg.MaxPacketsPerJob <= 0 {
		cfg.MaxPacketsPerJob = defaultMediaSchedulerMaxPacketsPerJob
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	p := &mediaPacer{
		cfg:      cfg,
		submitCh: make(chan *mediaPacedTask),
		workCh:   make(chan mediaPacedWork, cfg.QueueCapacity),
		resultCh: make(chan mediaPacedResult, cfg.QueueCapacity),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	for i := 0; i < cfg.WorkerCount; i++ {
		go p.worker()
	}
	go p.loop()
	return p
}

func (p *mediaPacer) Stop() {
	select {
	case <-p.doneCh:
		return
	default:
	}
	close(p.stopCh)
	<-p.doneCh
}

func (p *mediaPacer) Send(ctx context.Context, req mediaPacedSendRequest) (mediaPacedSendStats, error) {
	if req.WriteRTP == nil && req.WriteRTPBytes == nil {
		return mediaPacedSendStats{}, fmt.Errorf("media scheduler missing RTP writer")
	}
	if len(req.Frames) == 0 {
		return mediaPacedSendStats{}, nil
	}
	if req.MaxPacketsPerJob <= 0 {
		req.MaxPacketsPerJob = p.cfg.MaxPacketsPerJob
	}
	task := &mediaPacedTask{
		req:   req,
		ctx:   ctx,
		done:  make(chan mediaPacedResult, 1),
		stats: mediaPacedSendStats{StartedAt: time.Now()},
	}
	if req.Duration > 0 && len(req.Frames) > 1 {
		task.interval = req.Duration / time.Duration(len(req.Frames))
	}
	select {
	case p.submitCh <- task:
	case <-ctx.Done():
		return mediaPacedSendStats{}, ctx.Err()
	case <-p.doneCh:
		return mediaPacedSendStats{}, fmt.Errorf("media scheduler stopped")
	}
	select {
	case result := <-task.done:
		return result.stats, result.err
	case <-ctx.Done():
		return mediaPacedSendStats{}, ctx.Err()
	case <-p.doneCh:
		return mediaPacedSendStats{}, fmt.Errorf("media scheduler stopped")
	}
}

func (p *mediaPacer) loop() {
	defer close(p.doneCh)
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()
	wheel := make([][]*mediaPacedTask, p.cfg.WheelSize)
	cursor := 0
	schedule := func(task *mediaPacedTask, now time.Time) {
		if err := task.ctx.Err(); err != nil {
			task.stats.EndedAt = now
			task.done <- mediaPacedResult{task: task, stats: task.stats, err: err}
			return
		}
		delay := task.nextDue.Sub(now)
		ticks := 0
		if delay > 0 {
			ticks = int((delay + p.cfg.Tick - 1) / p.cfg.Tick)
		}
		if ticks == 0 {
			ticks = 1
		}
		if ticks >= len(wheel) {
			ticks = len(wheel) - 1
		}
		slot := (cursor + ticks) % len(wheel)
		wheel[slot] = append(wheel[slot], task)
	}
	for {
		select {
		case <-p.stopCh:
			return
		case task := <-p.submitCh:
			task.nextDue = time.Now()
			schedule(task, task.nextDue)
		case result := <-p.resultCh:
			task := result.task
			task.stats.PacketsSent += result.stats.PacketsSent
			task.stats.BytesSent += result.stats.BytesSent
			task.stats.ZeroByteWrites += result.stats.ZeroByteWrites
			task.stats.WriteAttempts += result.stats.WriteAttempts
			task.stats.WriteReturns += result.stats.WriteReturns
			task.stats.WriteErrors += result.stats.WriteErrors
			if task.stats.FirstWriteCallAt.IsZero() && !result.stats.FirstWriteCallAt.IsZero() {
				task.stats.FirstWriteCallAt = result.stats.FirstWriteCallAt
			}
			if task.stats.FirstWriteReturnAt.IsZero() && !result.stats.FirstWriteReturnAt.IsZero() {
				task.stats.FirstWriteReturnAt = result.stats.FirstWriteReturnAt
			}
			if result.stats.MaxWriteLatency > task.stats.MaxWriteLatency {
				task.stats.MaxWriteLatency = result.stats.MaxWriteLatency
			}
			if task.stats.FirstWriteAt.IsZero() && !result.firstWrite.IsZero() {
				task.stats.FirstWriteAt = result.firstWrite
			}
			if result.err != nil {
				task.stats.EndedAt = time.Now()
				task.done <- mediaPacedResult{task: task, stats: task.stats, err: result.err}
				continue
			}
			if result.retry {
				if result.nextIndex > task.packetIndex {
					task.packetIndex = result.nextIndex
				}
				task.nextDue = time.Now().Add(p.cfg.Tick)
				schedule(task, time.Now())
				continue
			}
			if result.endIndex > task.packetIndex {
				task.packetIndex = result.endIndex
			}
			if task.packetIndex >= len(task.req.Frames[task.frameIndex]) {
				task.frameIndex++
				task.packetIndex = 0
				if task.frameIndex >= len(task.req.Frames) {
					task.stats.EndedAt = time.Now()
					task.done <- mediaPacedResult{task: task, stats: task.stats}
					continue
				}
				if task.interval > 0 {
					task.nextDue = time.Now().Add(task.interval)
				} else {
					task.nextDue = time.Now()
				}
			} else {
				task.nextDue = time.Now().Add(p.cfg.Tick)
			}
			schedule(task, time.Now())
		case now := <-ticker.C:
			cursor = (cursor + 1) % len(wheel)
			due := wheel[cursor]
			wheel[cursor] = nil
			for _, task := range due {
				if task.nextDue.After(now) {
					schedule(task, now)
					continue
				}
				p.enqueueDueTask(task, now, schedule)
			}
		}
	}
}

func (p *mediaPacer) enqueueDueTask(task *mediaPacedTask, now time.Time, schedule func(*mediaPacedTask, time.Time)) {
	if err := task.ctx.Err(); err != nil {
		task.stats.EndedAt = now
		task.done <- mediaPacedResult{task: task, stats: task.stats, err: err}
		return
	}
	if task.frameIndex >= len(task.req.Frames) {
		task.stats.EndedAt = now
		task.done <- mediaPacedResult{task: task, stats: task.stats}
		return
	}
	frame := task.req.Frames[task.frameIndex]
	if task.packetIndex >= len(frame) {
		task.frameIndex++
		task.packetIndex = 0
		task.nextDue = now
		schedule(task, now)
		return
	}
	end := task.packetIndex + task.req.MaxPacketsPerJob
	if end > len(frame) {
		end = len(frame)
	}
	packets := frame[task.packetIndex:end]
	byteCount := rtpPayloadBytes(packets)
	work := mediaPacedWork{task: task, packets: packets, byteCount: byteCount, startIndex: task.packetIndex, endIndex: end}
	select {
	case p.workCh <- work:
	default:
		task.stats.DroppedJobs++
		task.stats.DroppedPackets += len(packets)
		task.stats.QueueFullDrops++
		p.cfg.Logf("media_scheduler_queue_full label=%s dropped_packets=%d queue_depth=%d queue_capacity=%d", task.req.Label, len(packets), len(p.workCh), cap(p.workCh))
		task.packetIndex = end
		task.nextDue = now.Add(p.cfg.Tick)
		schedule(task, now)
	}
}

func (p *mediaPacer) worker() {
	for {
		select {
		case <-p.stopCh:
			return
		case work := <-p.workCh:
			result := mediaPacedResult{task: work.task, endIndex: work.endIndex, nextIndex: work.startIndex}
			for _, packet := range work.packets {
				callAt := time.Now()
				if result.stats.FirstWriteCallAt.IsZero() {
					result.stats.FirstWriteCallAt = callAt
				}
				result.stats.WriteAttempts++
				n, err := work.task.writeRTP(packet)
				returnAt := time.Now()
				result.stats.WriteReturns++
				if result.stats.FirstWriteReturnAt.IsZero() {
					result.stats.FirstWriteReturnAt = returnAt
				}
				if latency := returnAt.Sub(callAt); latency > result.stats.MaxWriteLatency {
					result.stats.MaxWriteLatency = latency
				}
				if err != nil {
					result.stats.WriteErrors++
					result.err = err
					break
				}
				if n <= 0 {
					result.retry = true
					result.stats.ZeroByteWrites++
					break
				}
				if result.firstWrite.IsZero() {
					result.firstWrite = time.Now()
				}
				result.stats.PacketsSent++
				result.stats.BytesSent += n
				result.nextIndex++
			}
			select {
			case p.resultCh <- result:
			case <-p.stopCh:
				return
			}
		}
	}
}

func (t *mediaPacedTask) writeRTP(packet *rtp.Packet) (int, error) {
	if t.req.WriteRTPBytes != nil {
		return t.req.WriteRTPBytes(packet)
	}
	if err := t.req.WriteRTP(packet); err != nil {
		return 0, err
	}
	return len(packet.Payload), nil
}

func mediaSchedulerLogLine(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func mediaFramesFromPackets(packets []*rtp.Packet) [][]*rtp.Packet {
	frames := make([][]*rtp.Packet, 0, len(packets))
	for _, packet := range packets {
		frames = append(frames, []*rtp.Packet{packet})
	}
	return frames
}

func mediaFramesFromH264(frames []H264RTPFrame) [][]*rtp.Packet {
	mediaFrames := make([][]*rtp.Packet, 0, len(frames))
	for _, frame := range frames {
		mediaFrames = append(mediaFrames, frame.Packets)
	}
	return mediaFrames
}

func rtpPayloadBytes(packets []*rtp.Packet) int {
	total := 0
	for _, packet := range packets {
		total += len(packet.Payload)
	}
	return total
}
