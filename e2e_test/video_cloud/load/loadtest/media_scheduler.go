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
	MaxPacketsPerJob int
}

type mediaPacedSendStats struct {
	PacketsSent    int
	BytesSent      int
	DroppedJobs    int
	DroppedPackets int
	QueueFullDrops int
	StartedAt      time.Time
	FirstWriteAt   time.Time
	EndedAt        time.Time
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
	task      *mediaPacedTask
	packets   []*rtp.Packet
	byteCount int
}

type mediaPacedResult struct {
	task       *mediaPacedTask
	stats      mediaPacedSendStats
	firstWrite time.Time
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
	if req.WriteRTP == nil {
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
			if task.stats.FirstWriteAt.IsZero() && !result.firstWrite.IsZero() {
				task.stats.FirstWriteAt = result.firstWrite
			}
			if result.err != nil {
				task.stats.EndedAt = time.Now()
				task.done <- mediaPacedResult{task: task, stats: task.stats, err: result.err}
				continue
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
	work := mediaPacedWork{task: task, packets: packets, byteCount: byteCount}
	select {
	case p.workCh <- work:
		task.packetIndex = end
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
			result := mediaPacedResult{task: work.task}
			for _, packet := range work.packets {
				if err := work.task.req.WriteRTP(packet); err != nil {
					result.err = err
					break
				}
				if result.firstWrite.IsZero() {
					result.firstWrite = time.Now()
				}
				result.stats.PacketsSent++
			}
			result.stats.BytesSent = work.byteCount
			select {
			case p.resultCh <- result:
			case <-p.stopCh:
				return
			}
		}
	}
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
