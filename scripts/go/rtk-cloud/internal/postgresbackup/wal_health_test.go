package postgresbackup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativePostgresMetadataCannotBypassOutputBound(t *testing.T) {
	var out cappedBuffer
	err := quietExec(context.Background(), []string{"sh", "-c", "printf 'private-diagnostic' >&2; head -c 1048577 /dev/zero"}, safeEnvironment(), &out)
	if err == nil || out.Len() > 1<<20 || strings.Contains(err.Error(), "private-diagnostic") {
		t.Fatal("native metadata command bypassed its bound or exposed diagnostics")
	}
}

func TestContinuousWALHealthRequiresLivePrimaryFreshnessAndExactRemoteReadback(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "foreign-system", "standby", "disabled", "long-timeout", "no-segment", "history-only", "no-time", "stale", "future", "old-observation", "clock-skew", "failed-archive", "missing-remote", "corrupt-remote", "stale-remote", "future-remote", "recovered-failure"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, _, identity := workerFixture(t)
				c.Environment = environment
				c.Stack = "video-cloud-" + environment
				c.Remote.Prefix = environment + "/postgres-physical"
				remote := newMemoryRepository()
				engine := Engine{remote: remote}
				ctx := context.Background()
				name := "000000010000000000000001"
				source := filepath.Join(filepath.Dir(identity), name)
				workerWrite(t, source, []byte("native-wal-content"))
				receipt, err := engine.ArchiveWAL(ctx, c, name, source)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				archived := now.Add(-time.Second)
				observed := WALObservation{SystemIdentifier: c.Source.SystemIdentifier, ArchiveMode: "on", ArchiveTimeoutSeconds: 60, LastArchivedWAL: name, LastArchivedAt: &archived, ObservedAt: now}
				switch mode {
				case "foreign-system":
					observed.SystemIdentifier = "999999"
				case "standby":
					observed.InRecovery = true
				case "disabled":
					observed.ArchiveMode = "off"
				case "long-timeout":
					observed.ArchiveTimeoutSeconds = 61
				case "no-segment":
					observed.LastArchivedWAL = ""
				case "history-only":
					observed.LastArchivedWAL = "00000002.history"
				case "no-time":
					observed.LastArchivedAt = nil
				case "stale":
					archived = now.Add(-15*time.Minute - time.Nanosecond)
				case "future":
					archived = now.Add(31 * time.Second)
				case "old-observation":
					observed.ObservedAt = now.Add(-31 * time.Second)
				case "clock-skew":
					observed.ObservedAt = now.Add(31 * time.Second)
				case "failed-archive":
					failed := now
					observed.LastFailedAt = &failed
				case "recovered-failure":
					failed := now.Add(-time.Minute)
					observed.LastFailedAt = &failed
				case "missing-remote":
					delete(remote.objects, walKey(c, name)+".json")
				case "corrupt-remote":
					remote.corrupt = true
				case "stale-remote", "future-remote":
					receipt.Manifest.ArchivedAt = now.Add(-16 * time.Minute)
					if mode == "future-remote" {
						receipt.Manifest.ArchivedAt = now.Add(time.Minute)
					}
					raw, _ := json.Marshal(receipt)
					remote.objects[walKey(c, name)+".json"] = raw
				}
				health, err := engine.VerifyContinuousWAL(ctx, c, observed, now)
				valid := mode == "valid" || mode == "recovered-failure"
				if (err == nil) != valid {
					t.Fatal("incorrect continuous protection decision", mode, err)
				}
				if valid && (health.Environment != environment || health.RemoteArtifactSHA256 != receipt.Artifact.SHA256 || health.ArchiveAgeMilliseconds != 1000) {
					t.Fatal("freshness evidence not bound to remote archive")
				}
			})
		}
	}
}
func TestContinuousWALObservationUsesReadOnlyNativeQueryAndPrivateCredentials(t *testing.T) {
	c, _, identity := workerFixture(t)
	ctx := context.Background()
	remote := newMemoryRepository()
	engine := Engine{remote: remote}
	name := "000000010000000000000001"
	source := filepath.Join(filepath.Dir(identity), name)
	workerWrite(t, source, []byte("WAL"))
	receipt, err := engine.ArchiveWAL(ctx, c, name, source)
	if err != nil {
		t.Fatal(err)
	}
	var passfile string
	engine.Exec = func(ctx context.Context, argv, env []string, out io.Writer) error {
		if argv[0] != "psql" || argv[len(argv)-1] != "--command="+walHealthSQL || !strings.Contains(strings.Join(argv, " "), "--no-password") {
			t.Fatal("WAL health can execute an arbitrary SQL mutation")
		}
		for _, setting := range env {
			if strings.HasPrefix(setting, "PGPASSFILE=") {
				passfile = strings.TrimPrefix(setting, "PGPASSFILE=")
			}
		}
		info, err := os.Stat(passfile)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("WAL credentials not private")
		}
		now := time.Now().UTC()
		observation := WALObservation{SystemIdentifier: c.Source.SystemIdentifier, ArchiveMode: "on", ArchiveTimeoutSeconds: 60, LastArchivedWAL: name, LastArchivedAt: &now, ObservedAt: now}
		return json.NewEncoder(out).Encode(observation)
	}
	health, err := engine.CheckContinuousWAL(ctx, c)
	if err != nil || health.RemoteArtifactSHA256 != receipt.Artifact.SHA256 {
		t.Fatal("native WAL check failed", err)
	}
	if _, err := os.Stat(passfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private WAL probe credentials survived")
	}
	engine.Exec = func(context.Context, []string, []string, io.Writer) error { return errors.New("password=never-public") }
	if _, err := engine.CheckContinuousWAL(ctx, c); err == nil || strings.Contains(err.Error(), "never-public") {
		t.Fatal("source access failure leaked or passed")
	}
}
