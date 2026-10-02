package cloudmonitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFilesystemCapacityThresholdsAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		used int
		want Status
	}{{60, Pass}, {70, Warn}, {80, Warn}, {90, Fail}, {95, Fail}} {
		f := &collectorRunner{fn: func(args []string) ([]byte, int, error) {
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/test 100 " + strconvInt(tc.used) + " " + strconvInt(100-tc.used) + " 80% /data\n"), 0, nil
		}}
		c := collectVolume(context.Background(), Config{}, Runtime{}, f, "pg", "postgres", VolumeCheck{Namespace: "env-app", Pod: "p", Mount: "/data"}, true, time.Now())
		if c.Results[0].Status != tc.want {
			t.Fatalf("%d status %s", tc.used, c.Results[0].Status)
		}
		args := strings.Join(f.calls[0], " ")
		if !strings.Contains(args, "df -Pk -- /data") {
			t.Fatal("must use explicit readonly df")
		}
		if c.Samples[0].Value != float64(tc.used)*1024 {
			t.Fatal("bytes conversion")
		}
	}
	f := &collectorRunner{fn: func([]string) ([]byte, int, error) { return nil, 1, errors.New("password") }}
	if collectVolume(context.Background(), Config{}, Runtime{}, f, "pg", "postgres", VolumeCheck{}, true, time.Now()).Results[0].Status != Unknown {
		t.Fatal("missing disk source unknown")
	}
}
func TestRedisPersistenceMemoryAndMissingLimit(t *testing.T) {
	info := parseRedisInfo("used_memory:90\nmaxmemory:100\nused_memory_rss:120\naof_last_write_status:ok\naof_last_bgrewrite_status:ok\nrun_id:abc\ntotal_commands_processed:500\nerrorstat_OOM:count=2\ncmdstat_get:calls=100,usec=700,rejected_calls=0,failed_calls=0\n")
	conf := map[string]string{"appendonly": "yes", "appendfsync": "everysec", "maxmemory-policy": "noeviction"}
	cfg := RedisCheck{Name: "shadow", Required: true, Role: "shadow", ContainerLimitBytes: 200}
	c := evaluateRedisInfo(cfg, info, conf, true, time.Now())
	if findCollectorResult(t, c, "redis/memory").Status != Warn || findCollectorResult(t, c, "redis/persistence").Status != Pass {
		t.Fatal("capacity warn and persistence pass expected")
	}
	if findCollectorResult(t, c, "redis/container-memory").Status != Unknown {
		t.Fatal("RSS alone not enough to prove total container memory")
	}
	conf["maxmemory-policy"] = "allkeys-lru"
	c = evaluateRedisInfo(cfg, info, conf, true, time.Now())
	if findCollectorResult(t, c, "redis/persistence").Status != Fail {
		t.Fatal("persistent eviction must fail")
	}
	info["maxmemory"] = "0"
	c = evaluateRedisInfo(cfg, info, conf, true, time.Now())
	if findCollectorResult(t, c, "redis/memory").Status == Pass {
		t.Fatal("maxmemory0 cannot mean unlimited good capacity")
	}
}
func TestRedisCacheEvictionNotAutomaticFailure(t *testing.T) {
	c := evaluateRedisInfo(RedisCheck{Name: "cache", Role: "cache", Required: true}, parseRedisInfo("used_memory:10\nmaxmemory:100\nevicted_keys:500\n"), nil, false, time.Now())
	if findCollectorResult(t, c, "redis/persistence").Status != NotApplicable {
		t.Fatal("cache persistence policy not applicable")
	}
}
