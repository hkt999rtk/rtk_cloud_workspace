package cloudmonitor

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pgFixture struct {
	version int
	fail    string
	queries []string
	begins  []driver.TxOptions
	restart time.Time
}
type pgFixtureDriver struct{ state *pgFixture }
type pgFixtureConn struct{ state *pgFixture }
type pgFixtureTx struct{}
type pgFixtureRows struct {
	values []driver.Value
	done   bool
}

var pgFixtureSequence atomic.Int32

func (d pgFixtureDriver) Open(string) (driver.Conn, error) { return &pgFixtureConn{d.state}, nil }
func (c *pgFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements not supported by statistics fixture")
}
func (c *pgFixtureConn) Close() error              { return nil }
func (c *pgFixtureConn) Begin() (driver.Tx, error) { return nil, errors.New("BeginTx required") }
func (c *pgFixtureConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.state.begins = append(c.state.begins, opts)
	if c.state.fail == "begin" {
		return nil, errors.New("password-CANNOT-APPEAR")
	}
	if !opts.ReadOnly {
		return nil, errors.New("statistics access must be read-only")
	}
	return pgFixtureTx{}, nil
}
func (pgFixtureTx) Commit() error   { return nil }
func (pgFixtureTx) Rollback() error { return nil }
func (c *pgFixtureConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.queries = append(c.state.queries, q)
	if !strings.HasPrefix(q, "SET LOCAL statement_timeout") {
		return nil, errors.New("unexpected operation")
	}
	if c.state.fail == "timeout" {
		return nil, errors.New("password-CANNOT-APPEAR")
	}
	return driver.RowsAffected(0), nil
}
func (c *pgFixtureConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	var v []driver.Value
	phase := ""
	switch {
	case q == "SELECT 1":
		phase = "probe"
		v = []driver.Value{int64(1)}
	case strings.Contains(q, "pg_stat_activity"):
		phase = "activity"
		v = []driver.Value{float64(85), float64(100), float64(3), float64(1), float64(1), float64(1)}
	case strings.Contains(q, "pg_stat_database"):
		phase = "counters"
		v = []driver.Value{c.state.restart, float64(1000), float64(5), float64(5000), float64(40), float64(20), float64(10), float64(1), float64(1024), float64(10), float64(100), float64(100000)}
	case strings.Contains(q, "pg_indexes_size"):
		phase = "indexes"
		v = []driver.Value{float64(1000)}
	case strings.Contains(q, "pg_ls_waldir"):
		phase = "wal"
		v = []driver.Value{float64(40000)}
	case strings.Contains(q, "pg_stat_replication") || strings.Contains(q, "pg_replication_slots"):
		v = []driver.Value{nil}
	case strings.Contains(q, "server_version_num"):
		phase = "version"
		v = []driver.Value{int64(c.state.version)}
	case strings.Contains(q, "pg_stat_checkpointer"):
		phase = "checkpoints"
		v = []driver.Value{[]byte(`{"num_timed":5,"num_requested":1,"write_time":50,"sync_time":10,"buffers_written":600,"stats_reset":"2026-01-01"}`)}
	case strings.Contains(q, "pg_stat_bgwriter"):
		phase = "checkpoints"
		v = []driver.Value{[]byte(`{"checkpoints_timed":5,"checkpoints_req":1,"checkpoint_write_time":50,"checkpoint_sync_time":10,"buffers_checkpoint":600,"stats_reset":"2026-01-01"}`)}
	default:
		return nil, errors.New("unrecognized readonly statistics query")
	}
	if c.state.fail == phase && phase != "" {
		return nil, errors.New("permission denied DSNpassword-CANNOT-APPEAR")
	}
	if c.state.fail == "badcheckpoint" && phase == "checkpoints" {
		v = []driver.Value{[]byte(`{"missing":true}`)}
	}
	return &pgFixtureRows{values: v}, nil
}
func (r *pgFixtureRows) Columns() []string {
	a := make([]string, len(r.values))
	for i := range a {
		a[i] = fmt.Sprint("column", i)
	}
	return a
}
func (r *pgFixtureRows) Close() error { return nil }
func (r *pgFixtureRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}
func fixturePostgresDB(t *testing.T, s *pgFixture) *sql.DB {
	t.Helper()
	if s.version == 0 {
		s.version = 170001
	}
	if s.restart.IsZero() {
		s.restart = time.Now().Add(-24 * time.Hour).UTC()
	}
	name := fmt.Sprintf("cloudmonitor-fixture-%d", pgFixtureSequence.Add(1))
	sql.Register(name, pgFixtureDriver{s})
	db, err := sql.Open(name, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPostgresStatisticsAreReadonlyAndPermissionGapsExplicit(t *testing.T) {
	for _, version := range []int{160009, 170001} {
		state := &pgFixture{version: version, fail: "wal"}
		db := fixturePostgresDB(t, state)
		now := time.Now()
		c := collectPostgresWithDB(context.Background(), Config{}, db, PostgresCheck{Name: "pg", Required: true}, now)
		if findCollectorResult(t, c, "postgres/connectivity").Status != Pass {
			t.Fatal("readonly query probe")
		}
		if findCollectorResult(t, c, "postgres/statistics").Status != Warn {
			t.Fatal("connections and lock wait warning")
		}
		if findCollectorResult(t, c, "postgres/wal_bytes").Status != Unknown {
			t.Fatal("WAL permission must be unknown")
		}
		if findCollectorResult(t, c, "postgres/checkpoints").Status != Pass {
			t.Fatal("WAL permission error must not abort checkpoint telemetry")
		}
		if findCollectorResult(t, c, "postgres/replication_lag_seconds").Status != NotApplicable {
			t.Fatal("no replication not applicable")
		}
		for _, opts := range state.begins {
			if !opts.ReadOnly {
				t.Fatal("transaction allows writes")
			}
		}
		if len(state.begins) < 5 {
			t.Fatal("optional telemetry isolated transactions")
		}
		found := false
		for _, s := range c.Samples {
			if s.Name == "postgres_transactions_committed" {
				found = true
				if s.Kind != "counter" || !strings.Contains(s.Identity, state.restart.Format(time.RFC3339Nano)) {
					t.Fatal("counter identity lacks instance restart")
				}
			}
		}
		if !found {
			t.Fatal("no transaction counter")
		}
		b, _ := json.Marshal(c)
		if strings.Contains(string(b), "CANNOT-APPEAR") {
			t.Fatal("SQL diagnostics leak")
		}
	}
}
func TestPostgresStatisticsFailurePaths(t *testing.T) {
	for _, phase := range []string{"begin", "timeout", "probe", "activity", "counters", "version", "checkpoints", "badcheckpoint"} {
		t.Run(phase, func(t *testing.T) {
			state := &pgFixture{fail: phase}
			c := collectPostgresWithDB(context.Background(), Config{}, fixturePostgresDB(t, state), PostgresCheck{Name: "pg", Required: true}, time.Now())
			b, _ := json.Marshal(c)
			if strings.Contains(string(b), "CANNOT-APPEAR") {
				t.Fatal("diagnostic leak")
			}
			if phase == "begin" || phase == "probe" {
				if c.Results[0].Status != Fail {
					t.Fatal("connectivity failed")
				}
			}
			if phase == "activity" {
				if findCollectorResult(t, c, "postgres/statistics").Status != Unknown {
					t.Fatal("missing stats unknown")
				}
			}
			if phase == "counters" {
				if findCollectorResult(t, c, "postgres/counters").Status != Unknown {
					t.Fatal("missing counters unknown")
				}
			}
			if phase == "version" || phase == "checkpoints" || phase == "badcheckpoint" {
				if findCollectorResult(t, c, "postgres/checkpoints").Status != Unknown {
					t.Fatal("missing checkpoint data unknown")
				}
			}
		})
	}
}

// Minimal RESP fixture exercises the real go-redis client and permission failures.
type redisFixture struct {
	listener   net.Listener
	mu         sync.Mutex
	commands   [][]string
	denyConfig bool
}

func newRedisFixture(t *testing.T, deny bool) *redisFixture {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &redisFixture{listener: l, denyConfig: deny}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					cmd, err := readFixtureRESP(r)
					if err != nil {
						return
					}
					f.mu.Lock()
					f.commands = append(f.commands, cmd)
					f.mu.Unlock()
					name := strings.ToUpper(cmd[0])
					switch name {
					case "HELLO":
						io.WriteString(conn, "-ERR unknown command 'HELLO'\r\n")
					case "CLIENT":
						io.WriteString(conn, "-ERR unsupported client metadata\r\n")
					case "PING":
						io.WriteString(conn, "+PONG\r\n")
					case "INFO":
						info := "used_memory:10\r\nmaxmemory:100\r\nused_memory_rss:15\r\nrun_id:fixture\r\naof_last_write_status:ok\r\naof_last_bgrewrite_status:ok\r\n"
						fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(info), info)
					case "CONFIG":
						if f.denyConfig {
							io.WriteString(conn, "-NOPERM secret-password-CANNOT-APPEAR\r\n")
							continue
						}
						key := cmd[2]
						value := map[string]string{"appendonly": "yes", "appendfsync": "everysec", "maxmemory-policy": "noeviction"}[key]
						fmt.Fprintf(conn, "*2\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(key), key, len(value), value)
					default:
						io.WriteString(conn, "-ERR unsupported operation\r\n")
					}
				}
			}()
		}
	}()
	return f
}
func readFixtureRESP(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[0] != '*' {
		return nil, errors.New("invalid RESP array")
	}
	count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || count < 1 || count > 64 {
		return nil, errors.New("invalid RESP count")
	}
	var out []string
	for i := 0; i < count; i++ {
		line, err = r.ReadString('\n')
		if err != nil || len(line) < 3 || line[0] != '$' {
			return nil, errors.New("invalid RESP string")
		}
		n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil || n < 0 || n > 1<<20 {
			return nil, errors.New("invalid RESP length")
		}
		b := make([]byte, n+2)
		if _, err = io.ReadFull(r, b); err != nil {
			return nil, err
		}
		out = append(out, string(b[:n]))
	}
	return out, nil
}
func TestRedisClientUsesReadOnlyProtocolAndHandlesConfigPermissions(t *testing.T) {
	for _, deny := range []bool{false, true} {
		f := newRedisFixture(t, deny)
		cfg := RedisCheck{Name: "shadow", Address: f.listener.Addr().String(), Role: "shadow", Required: true}
		c := collectRedis(context.Background(), Config{}, Runtime{}, cfg, time.Now())
		if findCollectorResult(t, c, "redis/connectivity").Status != Pass {
			t.Fatal("real client PING must pass")
		}
		expected := Pass
		if deny {
			expected = Unknown
		}
		if findCollectorResult(t, c, "redis/persistence").Status != expected {
			t.Fatal("CONFIG permissions must determine coverage")
		}
		f.mu.Lock()
		commands := append([][]string{}, f.commands...)
		f.mu.Unlock()
		configCount := 0
		for _, cmd := range commands {
			switch strings.ToUpper(cmd[0]) {
			case "HELLO", "CLIENT", "PING", "INFO":
			case "CONFIG":
				configCount++
				if len(cmd) != 3 || strings.ToUpper(cmd[1]) != "GET" {
					t.Fatal("configuration mutation")
				}
			default:
				t.Fatalf("unexpected Redis operation %s", cmd[0])
			}
		}
		if configCount != 3 {
			t.Fatalf("expected3narrowCONFIGGETs,got%d", configCount)
		}
		b, _ := json.Marshal(c)
		if strings.Contains(string(b), "CANNOT-APPEAR") {
			t.Fatal("Redis diagnostics leak")
		}
	}
}
func TestDatabaseMissingCredentialsAndRedisConnectionFailure(t *testing.T) {
	rt := Runtime{ConfigRoot: t.TempDir()}
	c := collectPostgres(context.Background(), Config{}, rt, PostgresCheck{Name: "pg", DSNFile: "missing", Required: true}, time.Now())
	if c.Results[0].Status != Unknown {
		t.Fatal("missing DSN unknown")
	}
	os.WriteFile(filepath.Join(rt.ConfigRoot, "bad-dsn"), []byte("host=127.0.0.1 port=1 connect_timeout=1 password=DO-NOT-EXPOSE"), 0600)
	c = collectPostgres(context.Background(), Config{TimeoutSeconds: 1}, rt, PostgresCheck{Name: "pg", DSNFile: "bad-dsn", Required: true}, time.Now())
	if c.Results[0].Status != Fail {
		t.Fatal("unreachable DB fail")
	}
	c = collectRedis(context.Background(), Config{TimeoutSeconds: 1}, rt, RedisCheck{Name: "redis", Address: "127.0.0.1:1", Role: "cache", Required: true}, time.Now())
	if c.Results[0].Status != Fail {
		t.Fatal("unreachable Redis fail")
	}
	c = collectRedis(context.Background(), Config{}, rt, RedisCheck{Name: "redis", Address: "x", PasswordFile: "missing"}, time.Now())
	if c.Results[0].Status != Unknown {
		t.Fatal("credential unavailable unknown")
	}
	c = collectRedis(context.Background(), Config{}, rt, RedisCheck{Name: "redis", Address: "x", CAFile: "missing"}, time.Now())
	if c.Results[0].Status != Unknown {
		t.Fatal("trust unavailable unknown")
	}
}
