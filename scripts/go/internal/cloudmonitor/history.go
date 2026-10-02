package cloudmonitor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxHistoryRecord = 16 << 20

var environmentPath = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func historyDirectory(outDir, env string) (string, error) {
	if !environmentPath.MatchString(env) || env == "." || env == ".." {
		return "", fmt.Errorf("invalid history environment %q", env)
	}
	return filepath.Join(outDir, "history", env), nil
}

// ensurePrivateDirectory refuses symlinks in the directories the monitor owns.
func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("monitor directory is not a regular directory")
	}
	return os.Chmod(path, 0700)
}

func atomicPrivateWrite(path string, contents []byte) error {
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("refusing nonregular output")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cloud-monitor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(contents)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// StoreSnapshot appends a complete safe snapshot and atomically replaces latest.
// Callers must hold the environment writer lock when multiple processes exist.
func StoreSnapshot(outDir string, s Snapshot) error {
	dir, err := historyDirectory(outDir, s.Environment)
	if err != nil {
		return err
	}
	if s.StartedAt.IsZero() {
		return errors.New("snapshot has no collection time")
	}
	if s.SchemaVersion != SchemaVersion {
		return errors.New("snapshot schema is unsupported")
	}
	if err = ensurePrivateDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	if err = ensurePrivateDirectory(dir); err != nil {
		return err
	}
	s = safeSnapshot(s)
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(encoded) > maxHistoryRecord {
		return errors.New("snapshot exceeds history record limit")
	}
	path := filepath.Join(dir, s.StartedAt.UTC().Format("2006-01-02")+".jsonl")
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing nonregular history file")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err == nil {
		err = repairHistoryTail(f, s)
		if err == nil {
			_, err = f.Write(append(encoded, '\n'))
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return atomicPrivateWrite(filepath.Join(dir, "latest.json"), append(encoded, '\n'))
}

// Recover an interrupted final write before resuming the append stream. The
// incomplete bytes are replaced by an explicit UNKNOWN marker, never discarded
// as though the interrupted observation had been successful.
func repairHistoryTail(f *os.File, s Snapshot) error {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := []byte{0}
	if _, err = f.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	length := info.Size()
	if length > maxHistoryRecord+1 {
		length = maxHistoryRecord + 1
	}
	tail := make([]byte, length)
	if _, err = f.ReadAt(tail, info.Size()-length); err != nil {
		return err
	}
	cut := bytes.LastIndexByte(tail, '\n')
	if cut < 0 && info.Size() > length {
		return errors.New("crash tail exceeds history record limit")
	}
	offset := info.Size() - length + int64(cut+1)
	if err = f.Truncate(offset); err != nil {
		return err
	}
	at := s.StartedAt
	marker := Snapshot{SchemaVersion: SchemaVersion, Environment: s.Environment, StartedAt: at, FinishedAt: at, Overall: Unknown, Results: []Result{{Service: "monitor", CheckID: "history.truncated", Layer: "observation", Required: true, Status: Unknown, ObservedAt: at, Reason: "先前歷史寫入中斷；未完整資料已標為缺口。"}}}
	marker.Overall, marker.Coverage = Summarize(marker.Results)
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// LoadHistory streams bounded records. A partial final line from an interrupted
// write becomes an explicit UNKNOWN gap, never a synthetic successful sample.
func LoadHistory(outDir, env string, from, to time.Time) ([]Snapshot, error) {
	dir, err := historyDirectory(outDir, env)
	if err != nil {
		return nil, err
	}
	if !to.IsZero() && !from.IsZero() && !to.After(from) {
		return nil, errors.New("history end must follow start")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshots []Snapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		day, e := time.Parse("2006-01-02.jsonl", entry.Name())
		if e != nil {
			continue
		}
		if (!from.IsZero() && !day.Add(24*time.Hour).After(from)) || (!to.IsZero() && !day.Before(to)) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("refusing nonregular history file")
		}
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		loaded, e := readHistoryFile(f, env, day, from, to)
		closeErr := f.Close()
		if e != nil {
			return nil, fmt.Errorf("history %s: %w", entry.Name(), e)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		snapshots = append(snapshots, loaded...)
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].StartedAt.Before(snapshots[j].StartedAt) })
	return snapshots, nil
}

func readHistoryFile(f io.Reader, env string, day, from, to time.Time) ([]Snapshot, error) {
	reader := bufio.NewReaderSize(f, 64<<10)
	var records []Snapshot
	for {
		line, err := readBoundedLine(reader, maxHistoryRecord)
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == io.EOF && len(bytes.TrimSpace(line)) > 0 {
			at := day
			if len(records) > 0 {
				at = records[len(records)-1].StartedAt.Add(time.Nanosecond)
			}
			if !from.IsZero() && at.Before(from) {
				at = from
			}
			if to.IsZero() || at.Before(to) {
				records = append(records, Snapshot{SchemaVersion: SchemaVersion, Environment: env, StartedAt: at, FinishedAt: at, Overall: Unknown, Results: []Result{{Service: "monitor", CheckID: "history.truncated", Layer: "observation", Required: true, Status: Unknown, ObservedAt: at, Reason: "歷史檔案最後一筆未完整寫入；此時段資料有缺口。"}}})
			}
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var s Snapshot
		if e := json.Unmarshal(line, &s); e != nil {
			return nil, errors.New("malformed complete history record")
		}
		if s.Environment != env || s.StartedAt.IsZero() || s.SchemaVersion != SchemaVersion {
			return nil, errors.New("history identity or schema mismatch")
		}
		if (!from.IsZero() && s.StartedAt.Before(from)) || (!to.IsZero() && !s.StartedAt.Before(to)) {
			continue
		}
		records = append(records, safeSnapshot(s))
	}
	return records, nil
}

func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > limit {
			return nil, errors.New("history record exceeds limit")
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

// RetainHistory deletes only date-named records belonging to one environment.
func RetainHistory(outDir, env string, days int, now time.Time) error {
	if days < 1 {
		return errors.New("history retention must be positive")
	}
	dir, err := historyDirectory(outDir, env)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days+1)
	for _, e := range entries {
		day, err := time.Parse("2006-01-02.jsonl", e.Name())
		if err != nil || !day.Before(cutoff) || !e.Type().IsRegular() {
			continue
		}
		if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func sampleKey(s Sample) string { return s.Service + "\x00" + s.Name + "\x00" + s.Identity }

// EnrichHistory derives counter rates from actual observations. Reset counters
// and changed identities produce a gap. Forecasts require >=72h and >=90%
// coverage at the 30-second resource collection cadence.
func EnrichHistory(s Snapshot, prior []Snapshot) Snapshot {
	s.Results = append([]Result(nil), s.Results...)
	s.Samples = append([]Sample(nil), s.Samples...)
	last := map[string]Sample{}
	for _, p := range prior {
		for _, v := range p.Samples {
			if v.ObservedAt.Before(s.StartedAt) {
				key := sampleKey(v)
				if old, ok := last[key]; !ok || v.ObservedAt.After(old.ObservedAt) {
					last[key] = v
				}
			}
		}
	}
	original := append([]Sample(nil), s.Samples...)
	for _, v := range original {
		if v.Kind != "counter" {
			continue
		}
		old, ok := last[sampleKey(v)]
		dt := v.ObservedAt.Sub(old.ObservedAt).Seconds()
		if !ok || dt <= 0 || dt > 90 || v.Value < old.Value || math.IsNaN(v.Value) || math.IsInf(v.Value, 0) {
			continue
		}
		s.Samples = append(s.Samples, Sample{Service: v.Service, Name: v.Name + "_per_second", Value: (v.Value - old.Value) / dt, Unit: v.Unit + "/s", Kind: "gauge", ObservedAt: v.ObservedAt, Identity: v.Identity})
		if v.Value > old.Value && redisWriteRejectionCounter(v.Name) {
			delta := v.Value - old.Value
			s.Results = append(s.Results, Result{Service: v.Service, CheckID: "redis/rejected-writes/" + v.Name, Layer: "performance", Required: requiredRedisService(s, v.Service), Status: Fail, ObservedAt: v.ObservedAt, Value: &delta, Unit: "errors", Threshold: "new write rejection count > 0", Source: "consecutive Redis INFO observations", Reason: "連續觀測間出現新的 Redis 拒絕寫入；既有累積錯誤與重新啟動不計入。", Recommendation: "檢查記憶體、持久化、replica 角色與寫入 ACL。"})
		}
	}
	for _, v := range original {
		if v.Name != "redis_memory_used_bytes" && v.Name != "redis_container_used_bytes" {
			continue
		}
		base := strings.TrimSuffix(v.Name, "_used_bytes")
		limit := 0.0
		for _, n := range original {
			if n.Service == v.Service && n.Identity == v.Identity && n.Name == base+"_limit_bytes" {
				limit = n.Value
				break
			}
		}
		if limit <= 0 || v.Value/limit < .85 {
			continue
		}
		if sustainedRedisMemory(v, limit, prior) {
			check := "redis/memory"
			if v.Name == "redis_container_used_bytes" {
				check = "redis/container-memory"
			}
			found := false
			for i := range s.Results {
				r := &s.Results[i]
				if r.Service == v.Service && r.CheckID == check {
					r.Status = Fail
					r.Reason = "Redis 記憶體使用率已連續五分鐘達 85% 以上。"
					r.Threshold = "70% warning / 85% for 5m critical"
					r.Recommendation = "確認寫入成長並安排擴容，避免拒絕寫入。"
					found = true
					break
				}
			}
			if !found {
				pct := v.Value / limit * 100
				s.Results = append(s.Results, Result{Service: v.Service, CheckID: check, Layer: "capacity", Required: requiredRedisService(s, v.Service), Status: Fail, ObservedAt: v.ObservedAt, Value: &pct, Unit: "percent", Threshold: "85% for 5m critical", Source: "saved Redis history", Reason: "Redis 記憶體使用率已連續五分鐘達 85% 以上。"})
			}
		}
	}
	for _, v := range original {
		if v.Kind != "gauge" || !strings.HasSuffix(v.Name, "_used_bytes") {
			continue
		}
		base := strings.TrimSuffix(v.Name, "_used_bytes")
		capacity := 0.0
		for _, n := range original {
			if n.Service == v.Service && n.Identity == v.Identity && (n.Name == base+"_capacity_bytes" || n.Name == base+"_limit_bytes") {
				capacity = n.Value
				break
			}
		}
		if capacity <= 0 {
			continue
		}
		result := Result{Service: v.Service, CheckID: base + ".days_to_warning", Layer: "capacity", Status: Unknown, ObservedAt: v.ObservedAt, Unit: "days", Threshold: "70% capacity; ≥72h history and ≥90% coverage", Source: "saved history", Reason: "歷史資料不足，尚不能預估到達容量警戒線的時間。"}
		points := []Sample{v}
		start := v.ObservedAt.Add(-7 * 24 * time.Hour)
		capacityChanged := false
		for _, p := range prior {
			for _, n := range p.Samples {
				if sampleKey(n) == sampleKey(v) && !n.ObservedAt.Before(start) && n.ObservedAt.Before(v.ObservedAt) {
					points = append(points, n)
				}
				if n.Service == v.Service && n.Identity == v.Identity && (n.Name == base+"_capacity_bytes" || n.Name == base+"_limit_bytes") && !n.ObservedAt.Before(start) && n.ObservedAt.Before(v.ObservedAt) && n.Value != capacity {
					capacityChanged = true
				}
			}
		}
		sort.Slice(points, func(i, j int) bool { return points[i].ObservedAt.Before(points[j].ObservedAt) })
		if capacityChanged {
			result.Reason = "觀測期間容量限制已變更；重新累積足夠資料後再預估。"
			s.Results = append(s.Results, result)
			continue
		}
		if len(points) > 1 {
			span := v.ObservedAt.Sub(points[0].ObservedAt)
			covered := time.Duration(0)
			for i := 1; i < len(points); i++ {
				dt := points[i].ObservedAt.Sub(points[i-1].ObservedAt)
				if dt > 0 {
					if dt > 30*time.Second {
						dt = 30 * time.Second
					}
					covered += dt
				}
			}
			if span >= 72*time.Hour && float64(covered)/float64(span) >= .9 {
				growth := medianDailyGrowth(points)
				if growth > 0 {
					days := math.Max(0, (capacity*.7-v.Value)/growth)
					result.Value = &days
					result.Status = Pass
					result.Reason = "依最近最多七天的每日增長中位數估算；工作負載改變可能影響結果。"
					if days <= 7 {
						result.Status = Warn
						result.Recommendation = "確認成長原因並安排容量擴充。"
					}
					s.Samples = append(s.Samples, Sample{Service: v.Service, Name: base + "_days_to_warning", Value: days, Unit: "days", Kind: "gauge", ObservedAt: v.ObservedAt, Identity: v.Identity})
				} else {
					result.Reason = "觀測期間未呈現穩定正成長，不提供耗盡日期。"
				}
			}
		}
		s.Results = append(s.Results, result)
	}
	s.Overall, s.Coverage = Summarize(s.Results)
	return s
}

func requiredRedisService(s Snapshot, service string) bool {
	for _, r := range s.Results {
		if r.Service == service && strings.HasPrefix(r.CheckID, "redis/") && r.Required {
			return true
		}
	}
	return false
}

func redisWriteRejectionCounter(name string) bool {
	if name == "redis_error_oom" || name == "redis_error_readonly" || name == "redis_error_misconf" {
		return true
	}
	if !strings.HasPrefix(name, "redis_command_") || !strings.HasSuffix(name, "_rejected_calls") {
		return false
	}
	command := strings.TrimSuffix(strings.TrimPrefix(name, "redis_command_"), "_rejected_calls")
	write := map[string]bool{"set": true, "setex": true, "psetex": true, "setnx": true, "mset": true, "msetnx": true, "append": true, "incr": true, "incrby": true, "incrbyfloat": true, "decr": true, "decrby": true, "del": true, "unlink": true, "expire": true, "pexpire": true, "hset": true, "hsetnx": true, "hmset": true, "hdel": true, "hincrby": true, "hincrbyfloat": true, "sadd": true, "srem": true, "lpush": true, "rpush": true, "lpop": true, "rpop": true, "lset": true, "ltrim": true, "zadd": true, "zrem": true, "zincrby": true, "xadd": true, "xdel": true, "xtrim": true, "xack": true, "eval": true, "evalsha": true, "fcall": true}
	return write[command]
}

func sustainedRedisMemory(current Sample, limit float64, prior []Snapshot) bool {
	start := current.ObservedAt.Add(-5 * time.Minute)
	var points []Sample
	for _, s := range prior {
		for _, v := range s.Samples {
			if sampleKey(v) == sampleKey(current) && !v.ObservedAt.Before(start.Add(-90*time.Second)) && !v.ObservedAt.After(current.ObservedAt) {
				points = append(points, v)
			}
			if v.Service == current.Service && v.Identity == current.Identity && v.Name == strings.TrimSuffix(current.Name, "_used_bytes")+"_limit_bytes" && !v.ObservedAt.Before(start) && v.Value != limit {
				return false
			}
		}
	}
	points = append(points, current)
	sort.Slice(points, func(i, j int) bool { return points[i].ObservedAt.Before(points[j].ObservedAt) })
	// The first observation at/before the window begins must itself meet the
	// threshold, and every following observation needs a <=90 second gap.
	anchor := -1
	for i, v := range points {
		if !v.ObservedAt.After(start) {
			anchor = i
		}
	}
	if anchor < 0 {
		return false
	}
	last := points[anchor].ObservedAt
	for _, v := range points[anchor:] {
		if v.Value/limit < .85 || v.ObservedAt.Sub(last) > 90*time.Second {
			return false
		}
		last = v.ObservedAt
	}
	return !last.Before(current.ObservedAt)
}

func medianDailyGrowth(points []Sample) float64 {
	var growth []float64
	for i := 0; i < len(points); {
		j := i + 1
		for j < len(points) && points[j].ObservedAt.Sub(points[i].ObservedAt) < 24*time.Hour {
			j++
		}
		if j >= len(points) {
			break
		}
		days := points[j].ObservedAt.Sub(points[i].ObservedAt).Hours() / 24
		growth = append(growth, (points[j].Value-points[i].Value)/days)
		i = j
	}
	if len(growth) < 3 {
		return 0
	}
	sort.Float64s(growth)
	if len(growth)%2 == 1 {
		return growth[len(growth)/2]
	}
	return (growth[len(growth)/2-1] + growth[len(growth)/2]) / 2
}
