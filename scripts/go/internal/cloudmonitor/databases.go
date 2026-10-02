package cloudmonitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

func CollectDatabases(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	var tasks []func(context.Context) Collection
	for _, p := range cfg.Postgres {
		p := p
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.Postgres = []PostgresCheck{p}
			single.Redis = nil
			return collectDatabaseChecks(c, single, rt, runner, now)
		})
	}
	for _, r := range cfg.Redis {
		r := r
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.Redis = []RedisCheck{r}
			single.Postgres = nil
			return collectDatabaseChecks(c, single, rt, runner, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}
func collectDatabaseChecks(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	var out Collection
	for _, p := range cfg.Postgres {
		out = Merge(out, collectPostgres(ctx, cfg, rt, p, now))
		if p.Volume != nil {
			out = Merge(out, collectVolume(ctx, cfg, rt, runner, p.Name, "postgres", *p.Volume, p.Required, now))
		} else {
			out = Merge(out, sourceGap(p.Name, "postgres/volume", "缺少實際 filesystem/PVC 用量來源；database size 不代表磁碟使用率", p.Required, now))
		}
	}
	for _, r := range cfg.Redis {
		out = Merge(out, collectRedis(ctx, cfg, rt, r, now))
		if r.Volume != nil {
			out = Merge(out, collectVolume(ctx, cfg, rt, runner, r.Name, "redis", *r.Volume, r.Required, now))
		} else if r.Role != "cache" {
			out = Merge(out, sourceGap(r.Name, "redis/volume", "持久狀態 Redis 缺少 AOF filesystem/PVC 容量來源", r.Required, now))
		}
	}
	return out
}
func collectPostgres(ctx context.Context, cfg Config, rt Runtime, p PostgresCheck, now time.Time) Collection {
	r := result(p.Name, "postgres/connectivity", "readiness", p.Required, now)
	r.Source = "postgres"
	raw, err := ReadCredential(rt, p.DSNFile)
	if err != nil {
		r.Reason = "PostgreSQL 連線資料無法讀取"
		return Collection{Results: []Result{r}}
	}
	db, err := sql.Open("postgres", strings.TrimSpace(string(raw)))
	if err != nil {
		r.Reason = "PostgreSQL 連線設定無效"
		return Collection{Results: []Result{r}}
	}
	defer db.Close()
	return collectPostgresWithDB(ctx, cfg, db, p, now)
}

func collectPostgresWithDB(ctx context.Context, cfg Config, db *sql.DB, p PostgresCheck, now time.Time) Collection {
	r := result(p.Name, "postgres/connectivity", "readiness", p.Required, now)
	r.Source = "postgres"
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	c, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	start := time.Now()
	tx, err := db.BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		r.Status = Fail
		r.Reason = "PostgreSQL 連線或唯讀 transaction 失敗"
		return Collection{Results: []Result{r}}
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c, "SET LOCAL statement_timeout = '3000ms'"); err != nil {
		r.Reason = "無法配置唯讀統計查詢逾時"
		return Collection{Results: []Result{r}}
	}
	var one int
	if err = tx.QueryRowContext(c, "SELECT 1").Scan(&one); err != nil {
		r.Status = Fail
		r.Reason = "PostgreSQL 唯讀探測失敗"
	} else {
		r.Status = Pass
		r.Reason = "PostgreSQL 可處理唯讀查詢"
	}
	r.DurationMS = time.Since(start).Milliseconds()
	if r.Status == Pass && p.MaxProbeLatencyMS > 0 && float64(r.DurationMS) > p.MaxProbeLatencyMS {
		r.Status = Warn
		r.Reason = "PostgreSQL 探測延遲超過設定目標"
	}
	out := Collection{Results: []Result{r}, Samples: []Sample{{Service: p.Name, Name: "postgres_probe_latency_ms", Value: float64(r.DurationMS), Unit: "ms", Kind: "gauge", ObservedAt: now, Identity: p.Name}}}
	if r.Status == Fail {
		return out
	}
	stat := result(p.Name, "postgres/statistics", "capacity", p.Required, now)
	stat.Source = "postgres"
	var connections, maxConnections, active, idleTxn, lockWaits, longTxn float64
	err = tx.QueryRowContext(c, `SELECT count(*)::float8,current_setting('max_connections')::float8,count(*) FILTER (WHERE state='active')::float8,count(*) FILTER (WHERE state='idle in transaction')::float8,count(*) FILTER (WHERE wait_event_type='Lock')::float8,count(*) FILTER (WHERE xact_start < now()-interval '5 minutes')::float8 FROM pg_stat_activity`).Scan(&connections, &maxConnections, &active, &idleTxn, &lockWaits, &longTxn)
	if err != nil {
		stat.Reason = "PostgreSQL activity 統計不可取得；檢查統計權限"
		out.Results = append(out.Results, stat)
		return out
	}
	values := map[string]float64{"connections": connections, "max_connections": maxConnections, "active_connections": active, "idle_in_transaction": idleTxn, "lock_waits": lockWaits, "long_transactions": longTxn}
	for n, v := range values {
		out.Samples = append(out.Samples, Sample{Service: p.Name, Name: "postgres_" + n, Value: v, Unit: "count", Kind: "gauge", ObservedAt: now, Identity: p.Name})
	}
	stat.Status = Pass
	stat.Reason = "activity 統計已取得"
	pct := connections / maxConnections * 100
	limit := p.MaxConnectionsPercent
	if limit == 0 {
		limit = 80
	}
	if pct >= limit {
		stat.Status = Warn
		stat.Reason = "PostgreSQL 連線占用超過設定門檻"
	}
	if lockWaits > 0 || idleTxn > 0 || longTxn > 0 {
		stat.Status = Warn
		stat.Reason = "有鎖等待、長交易或 idle-in-transaction，需配合歷史判定"
	}
	stat.Value = numeric(pct)
	stat.Unit = "percent"
	out.Results = append(out.Results, stat)
	var restart time.Time
	var commits, rollbacks, read, inserted, updated, deleted, deadlocks, tempBytes, blocksRead, blocksHit, size float64
	err = tx.QueryRowContext(c, `SELECT pg_postmaster_start_time(),coalesce(sum(xact_commit),0)::float8,coalesce(sum(xact_rollback),0)::float8,coalesce(sum(tup_returned),0)::float8,coalesce(sum(tup_inserted),0)::float8,coalesce(sum(tup_updated),0)::float8,coalesce(sum(tup_deleted),0)::float8,coalesce(sum(deadlocks),0)::float8,coalesce(sum(temp_bytes),0)::float8,coalesce(sum(blks_read),0)::float8,coalesce(sum(blks_hit),0)::float8,pg_database_size(current_database())::float8 FROM pg_stat_database`).Scan(&restart, &commits, &rollbacks, &read, &inserted, &updated, &deleted, &deadlocks, &tempBytes, &blocksRead, &blocksHit, &size)
	cr := result(p.Name, "postgres/counters", "performance", p.Required, now)
	if err != nil {
		cr.Reason = "PostgreSQL counters 無法取得"
	} else {
		cr.Status = Pass
		cr.Reason = "交易、資料讀寫與錯誤累積量已取得；速率由連續樣本計算"
		counters := map[string]float64{"transactions_committed": commits, "transactions_rolled_back": rollbacks, "tuples_returned": read, "tuples_inserted": inserted, "tuples_updated": updated, "tuples_deleted": deleted, "deadlocks": deadlocks, "temp_bytes": tempBytes, "blocks_read": blocksRead, "blocks_hit": blocksHit}
		for n, v := range counters {
			out.Samples = append(out.Samples, Sample{Service: p.Name, Name: "postgres_" + n, Value: v, Unit: "count", Kind: "counter", ObservedAt: now, Identity: p.Name + "/" + restart.UTC().Format(time.RFC3339Nano)})
		}
		out.Samples = append(out.Samples, Sample{Service: p.Name, Name: "postgres_database_bytes", Value: size, Unit: "bytes", Kind: "gauge", ObservedAt: now, Identity: p.Name})
	}
	out.Results = append(out.Results, cr)
	tx.Rollback()
	optional := []struct {
		id, query, unit string
		required        bool
	}{
		{"index_bytes", `SELECT coalesce(sum(pg_indexes_size(oid)),0)::float8 FROM pg_class WHERE relkind='r'`, "bytes", false},
		{"wal_bytes", `SELECT coalesce(sum(size),0)::float8 FROM pg_ls_waldir()`, "bytes", p.Required},
		{"replication_lag_seconds", `SELECT max(extract(epoch FROM replay_lag))::float8 FROM pg_stat_replication`, "seconds", false},
		{"replication_slot_retained_bytes", `SELECT max(pg_wal_lsn_diff(pg_current_wal_lsn(),restart_lsn))::float8 FROM pg_replication_slots`, "bytes", false},
	}
	for _, q := range optional {
		or := result(p.Name, "postgres/"+q.id, "capacity", q.required, now)
		oc, ocancel := context.WithTimeout(ctx, timeout(cfg))
		ot, e := db.BeginTx(oc, &sql.TxOptions{ReadOnly: true})
		var v sql.NullFloat64
		if e == nil {
			_, e = ot.ExecContext(oc, "SET LOCAL statement_timeout = '3000ms'")
			if e == nil {
				e = ot.QueryRowContext(oc, q.query).Scan(&v)
			}
			ot.Rollback()
		}
		ocancel()
		if e != nil {
			or.Reason = "額外 PostgreSQL 統計無法取得；檢查版本與唯讀權限"
		} else if !v.Valid {
			or.Status = NotApplicable
			or.Reason = "沒有對應 replication 統計"
		} else {
			or.Status = Pass
			or.Reason = "PostgreSQL 統計已取得"
			or.Value = numeric(v.Float64)
			or.Unit = q.unit
			out.Samples = append(out.Samples, Sample{Service: p.Name, Name: "postgres_" + q.id, Value: v.Float64, Unit: q.unit, Kind: "gauge", ObservedAt: now, Identity: p.Name})
		}
		out.Results = append(out.Results, or)
	}
	out = Merge(out, collectPostgresCheckpoints(ctx, cfg, db, p, now))
	out = Merge(out, sourceGap(p.Name, "postgres/performance-target", "效能上限、p95/p99、I/O 與連線池等待需配置實際 metrics 及已驗證目標", p.Required && !hasPerformanceSources(cfg, p.Name), now))
	return out
}
func collectVolume(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, service, prefix string, v VolumeCheck, required bool, now time.Time) Collection {
	r := result(service, prefix+"/volume", "capacity", required, now)
	r.Source = "filesystem"
	args := []string{"-n", v.Namespace, "exec", v.Pod}
	if v.Container != "" {
		args = append(args, "-c", v.Container)
	}
	args = append(args, "--", "df", "-Pk", "--", v.Mount)
	c, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	raw, code, err := kube(c, rt, runner, args...)
	if err != nil || code != 0 {
		r.Reason = "無法讀取指定 mount 的 filesystem 用量"
		return Collection{Results: []Result{r}}
	}
	used, total, ok := parseDF(string(raw))
	if !ok {
		r.Reason = "filesystem 用量輸出無法解析"
		return Collection{Results: []Result{r}}
	}
	pct := used / total * 100
	r.Status = Pass
	r.Reason = "filesystem 容量充足"
	r.Value = numeric(pct)
	r.Unit = "percent"
	r.Threshold = "70% warning / 80% expansion / 90% urgent / 95% critical"
	if pct >= 70 {
		r.Status = Warn
		r.Reason = "filesystem 使用率超過 70%"
	}
	if pct >= 80 {
		r.Reason = "filesystem 使用率超過 80%，安排擴容"
	}
	if pct >= 90 {
		r.Status = Fail
		r.Reason = "filesystem 使用率超過 90%，緊急處理"
	}
	if pct >= 95 {
		r.Status = Fail
		r.Reason = "filesystem 使用率超過 95%，極高容量風險"
	}
	return Collection{Results: []Result{r}, Samples: []Sample{{Service: service, Name: prefix + "_volume_used_bytes", Value: used, Unit: "bytes", Kind: "gauge", ObservedAt: now, Identity: service}, {Service: service, Name: prefix + "_volume_capacity_bytes", Value: total, Unit: "bytes", Kind: "gauge", ObservedAt: now, Identity: service}}}
}
func parseDF(raw string) (float64, float64, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 2 {
		return 0, 0, false
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return 0, 0, false
	}
	total, e1 := strconv.ParseFloat(fields[len(fields)-5], 64)
	used, e2 := strconv.ParseFloat(fields[len(fields)-4], 64)
	if e1 != nil || e2 != nil || total <= 0 || used < 0 || used > total || math.IsNaN(total) || math.IsNaN(used) || math.IsInf(total, 0) || math.IsInf(used, 0) {
		return 0, 0, false
	}
	return used * 1024, total * 1024, true
}

func collectRedis(ctx context.Context, cfg Config, rt Runtime, r RedisCheck, now time.Time) Collection {
	res := result(r.Name, "redis/connectivity", "readiness", r.Required, now)
	options := &redis.Options{Addr: r.Address, Username: r.Username, DB: r.DB, DialTimeout: timeout(cfg), ReadTimeout: timeout(cfg), WriteTimeout: timeout(cfg), MaxRetries: -1}
	if r.PasswordFile != "" {
		raw, err := ReadCredential(rt, r.PasswordFile)
		if err != nil {
			res.Reason = "Redis credential 無法讀取"
			return Collection{Results: []Result{res}}
		}
		options.Password = string(raw)
	}
	if r.CAFile != "" {
		client, err := HTTPClient(r.CAFile, "", "", rt, timeout(cfg))
		if err != nil {
			res.Reason = "Redis trust 設定無效"
			return Collection{Results: []Result{res}}
		}
		options.TLSConfig = client.Transport.(*http.Transport).TLSClientConfig.Clone()
	}
	return collectRedisClient(ctx, cfg, r, redis.NewClient(options), now)
}
func collectRedisClient(ctx context.Context, cfg Config, r RedisCheck, client *redis.Client, now time.Time) Collection {
	defer client.Close()
	res := result(r.Name, "redis/connectivity", "readiness", r.Required, now)
	c, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	start := time.Now()
	_, err := client.Ping(c).Result()
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Status = Fail
		res.Reason = "Redis 連線、認證或 PING 失敗"
		return Collection{Results: []Result{res}}
	}
	res.Status = Pass
	res.Reason = "Redis 可接受 PING"
	if r.MaxProbeLatencyMS > 0 && float64(res.DurationMS) > r.MaxProbeLatencyMS {
		res.Status = Warn
		res.Reason = "Redis 探測延遲超過設定目標"
	}
	raw, err := client.Info(c, "all").Result()
	if err != nil {
		return Merge(Collection{Results: []Result{res}}, sourceGap(r.Name, "redis/info", "Redis INFO 不可取得；檢查 ACL", r.Required, now))
	}
	info := parseRedisInfo(raw)
	config := map[string]string{}
	var configErr error
	for _, key := range []string{"maxmemory-policy", "appendonly", "appendfsync"} {
		values, e := client.ConfigGet(c, key).Result()
		if e != nil {
			configErr = e
		}
		for k, v := range values {
			config[k] = v
		}
	}
	out := evaluateRedisInfo(r, info, config, configErr == nil, now)
	if hasPerformanceSources(cfg, r.Name) {
		for i := range out.Results {
			if out.Results[i].CheckID == "redis/performance-target" {
				out.Results[i].Required = false
			}
		}
	}
	out.Results = append([]Result{res}, out.Results...)
	out.Samples = append(out.Samples, Sample{Service: r.Name, Name: "redis_probe_latency_ms", Value: float64(res.DurationMS), Unit: "ms", Kind: "gauge", ObservedAt: now, Identity: r.Name})
	return out
}
func parseRedisInfo(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			out[key] = value
		}
	}
	return out
}
func evaluateRedisInfo(r RedisCheck, info, config map[string]string, hasConfig bool, now time.Time) Collection {
	var out Collection
	value := func(k string) (float64, bool) {
		v, ok := info[k]
		f, e := strconv.ParseFloat(v, 64)
		return f, ok && e == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	id := r.Name + "/" + info["run_id"]
	metrics := map[string]struct{ Name, Unit, Kind string }{"used_memory": {"redis_memory_used_bytes", "bytes", "gauge"}, "maxmemory": {"redis_memory_limit_bytes", "bytes", "gauge"}, "used_memory_rss": {"redis_memory_rss_bytes", "bytes", "gauge"}, "mem_fragmentation_ratio": {"redis_memory_fragmentation_ratio", "ratio", "gauge"}, "total_commands_processed": {"redis_commands_processed", "commands", "counter"}, "instantaneous_ops_per_sec": {"redis_commands_per_second", "commands/s", "gauge"}, "total_net_input_bytes": {"redis_network_input_bytes", "bytes", "counter"}, "total_net_output_bytes": {"redis_network_output_bytes", "bytes", "counter"}, "connected_clients": {"redis_connected_clients", "clients", "gauge"}, "blocked_clients": {"redis_blocked_clients", "clients", "gauge"}, "rejected_connections": {"redis_rejected_connections", "count", "counter"}, "total_error_replies": {"redis_error_replies", "count", "counter"}, "evicted_keys": {"redis_evicted_keys", "keys", "counter"}, "keyspace_hits": {"redis_keyspace_hits", "count", "counter"}, "keyspace_misses": {"redis_keyspace_misses", "count", "counter"}, "aof_current_size": {"redis_aof_bytes", "bytes", "gauge"}, "current_cow_peak": {"redis_cow_peak_bytes", "bytes", "gauge"}}
	for k, m := range metrics {
		if v, ok := value(k); ok {
			out.Samples = append(out.Samples, Sample{Service: r.Name, Name: m.Name, Value: v, Unit: m.Unit, Kind: m.Kind, ObservedAt: now, Identity: id})
		}
	}
	used, ok1 := value("used_memory")
	limit, ok2 := value("maxmemory")
	cap := result(r.Name, "redis/memory", "capacity", r.Required, now)
	cap.Source = "redis INFO"
	if !ok1 || !ok2 {
		cap.Reason = "Redis 記憶體或 maxmemory 資料缺少"
	} else if limit <= 0 {
		cap.Status = Warn
		cap.Reason = "maxmemory=0；缺少受控資料記憶體上限"
	} else {
		pct := used / limit * 100
		cap.Value = numeric(pct)
		cap.Unit = "percent"
		cap.Status = Pass
		cap.Reason = "Redis 資料記憶體有餘裕"
		if pct >= 70 {
			cap.Status = Warn
			cap.Reason = "Redis 資料記憶體超過 70%，需確認持續時間"
		}
		if pct >= 85 {
			cap.Status = Warn
			cap.Reason = "Redis 資料記憶體超過 85%，需緊急確認容量"
		}
	}
	out.Results = append(out.Results, cap)
	cr := result(r.Name, "redis/container-memory", "capacity", r.Required, now)
	rss, rssOK := value("used_memory_rss")
	if r.ContainerLimitBytes <= 0 || !rssOK {
		cr.Reason = "缺少 Redis container 總記憶體用量／限制；RSS 不能代表全部 container 用量"
	} else {
		pct := rss / r.ContainerLimitBytes * 100
		cr.Value = numeric(pct)
		cr.Unit = "percent"
		cr.Status = Unknown
		cr.Reason = "已取得 Redis RSS 與容器限制，需 container working-set 來源才可判定"
		out.Samples = append(out.Samples, Sample{Service: r.Name, Name: "redis_container_limit_bytes", Value: r.ContainerLimitBytes, Unit: "bytes", Kind: "gauge", ObservedAt: now, Identity: r.Name})
	}
	out.Results = append(out.Results, cr)
	persist := result(r.Name, "redis/persistence", "readiness", r.Required && r.Role != "cache", now)
	if r.Role == "cache" {
		persist.Status = NotApplicable
		persist.Reason = "此 instance 設定為可重建快取"
	} else if !hasConfig {
		persist.Reason = "無法讀取 Redis persistence 與 eviction 配置"
	} else if config["appendonly"] != "yes" || config["appendfsync"] != "everysec" || config["maxmemory-policy"] != "noeviction" {
		persist.Status = Fail
		persist.Reason = "持久狀態 Redis 必須使用 AOF/everysec/noeviction"
	} else if info["aof_last_write_status"] != "ok" || info["aof_last_bgrewrite_status"] != "ok" {
		persist.Status = Fail
		persist.Reason = "AOF 寫入或 rewrite 狀態失敗或缺少"
	} else {
		persist.Status = Pass
		persist.Reason = "持久狀態配置與最近 AOF 作業有效"
	}
	out.Results = append(out.Results, persist)
	for k, v := range info {
		if strings.HasPrefix(k, "errorstat_") {
			fields := parseKV(v)
			n, e := strconv.ParseFloat(fields["count"], 64)
			if e == nil {
				out.Samples = append(out.Samples, Sample{Service: r.Name, Name: "redis_error_" + strings.ToLower(strings.TrimPrefix(k, "errorstat_")), Value: n, Unit: "count", Kind: "counter", ObservedAt: now, Identity: id})
			}
		}
		if strings.HasPrefix(k, "cmdstat_") {
			fields := parseKV(v)
			for _, f := range []string{"calls", "usec", "rejected_calls", "failed_calls"} {
				n, e := strconv.ParseFloat(fields[f], 64)
				if e == nil {
					out.Samples = append(out.Samples, Sample{Service: r.Name, Name: "redis_command_" + strings.TrimPrefix(k, "cmdstat_") + "_" + f, Value: n, Unit: "count", Kind: "counter", ObservedAt: now, Identity: id})
				}
			}
		}
	}
	out = Merge(out, sourceGap(r.Name, "redis/performance-target", "p95/p99、積壓與已驗證 throughput 目標需配置实际 metrics", r.Required, now))
	return out
}
func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// A current throughput sample alone cannot establish a latency target or a safe load envelope.
func hasPerformanceSources(cfg Config, service string) bool {
	throughput, latency, baseline := false, false, false
	for _, m := range cfg.Metrics {
		if m.Service != service {
			continue
		}
		name := strings.ToLower(m.Name)
		unit := strings.ToLower(m.Unit)
		if strings.Contains(name, "throughput") || strings.Contains(name, "per_second") || strings.Contains(name, "tps") || strings.HasSuffix(unit, "/s") {
			throughput = true
			baseline = baseline || (m.Baseline > 0 && m.BaselineEvidence != "")
		}
		if (strings.Contains(name, "latency") || strings.Contains(name, "p95") || strings.Contains(name, "p99")) && (m.WarnAbove != nil || m.FailAbove != nil) {
			latency = true
		}
	}
	return throughput && latency && baseline
}

func collectPostgresCheckpoints(ctx context.Context, cfg Config, db *sql.DB, p PostgresCheck, now time.Time) Collection {
	r := result(p.Name, "postgres/checkpoints", "performance", p.Required, now)
	c, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	tx, err := db.BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		r.Reason = "checkpoint 統計唯讀連線失敗"
		return Collection{Results: []Result{r}}
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c, "SET LOCAL statement_timeout = '3000ms'"); err != nil {
		r.Reason = "checkpoint 統計查詢設定失敗"
		return Collection{Results: []Result{r}}
	}
	var version int
	err = tx.QueryRowContext(c, "SELECT current_setting('server_version_num')::int").Scan(&version)
	if err != nil {
		r.Reason = "無法取得 PostgreSQL 版本"
		return Collection{Results: []Result{r}}
	}
	query := "SELECT row_to_json(s) FROM pg_stat_bgwriter s"
	if version >= 170000 {
		query = "SELECT row_to_json(s) FROM pg_stat_checkpointer s"
	}
	var raw []byte
	if tx.QueryRowContext(c, query).Scan(&raw) != nil {
		r.Reason = "checkpoint 統計不可取得；檢查版本與唯讀統計權限"
		return Collection{Results: []Result{r}}
	}
	var stats map[string]json.RawMessage
	if json.Unmarshal(raw, &stats) != nil {
		r.Reason = "checkpoint 統計格式無效"
		return Collection{Results: []Result{r}}
	}
	fields := map[string]string{"checkpoints_timed": "checkpoints_timed", "checkpoints_req": "checkpoints_requested", "checkpoint_write_time": "checkpoint_write_ms", "checkpoint_sync_time": "checkpoint_sync_ms", "buffers_checkpoint": "checkpoint_buffers_written"}
	if version >= 170000 {
		fields = map[string]string{"num_timed": "checkpoints_timed", "num_requested": "checkpoints_requested", "write_time": "checkpoint_write_ms", "sync_time": "checkpoint_sync_ms", "buffers_written": "checkpoint_buffers_written"}
	}
	var out Collection
	for key, name := range fields {
		var v float64
		if json.Unmarshal(stats[key], &v) != nil {
			continue
		}
		unit := "count"
		if strings.HasSuffix(name, "_ms") {
			unit = "ms"
		}
		out.Samples = append(out.Samples, Sample{Service: p.Name, Name: "postgres_" + name, Value: v, Unit: unit, Kind: "counter", ObservedAt: now, Identity: p.Name + "/" + string(stats["stats_reset"])})
	}
	if len(out.Samples) != len(fields) {
		r.Reason = "checkpoint 統計缺少必要欄位"
	} else {
		r.Status = Pass
		r.Reason = "checkpoint 次數與寫入／同步累積耗時已取得"
	}
	out.Results = append(out.Results, r)
	return out
}
