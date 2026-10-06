package postgresbackup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func pitrFixtureEngine(t *testing.T, c Config) Engine {
	t.Helper()
	base := workerExec(t, c, "")
	return Engine{remote: newMemoryRepository(), Exec: func(ctx context.Context, args, env []string, out io.Writer) error {
		if args[0] == "pg_controldata" {
			_, err := io.WriteString(out, "Database system identifier: "+c.Source.SystemIdentifier+"\nBytes per WAL segment: 16777216\nmax_connections setting: 800\nmax_worker_processes setting: 8\nmax_wal_senders setting: 10\nmax_prepared_xacts setting: 0\nmax_locks_per_xact setting: 64\n")
			return err
		}
		if err := base(ctx, args, env, out); err != nil {
			return err
		}
		if args[0] == "pg_basebackup" {
			for _, arg := range args {
				if strings.HasPrefix(arg, "--pgdata=") {
					root := strings.TrimPrefix(arg, "--pgdata=")
					workerWrite(t, filepath.Join(root, "backup_manifest"), []byte(`{"WAL-Ranges":[{"Timeline":1,"Start-LSN":"0/1000000","End-LSN":"0/2000000"}]}`))
					workerWrite(t, filepath.Join(root, "postgresql.auto.conf"), []byte("# original private settings\n"))
				}
			}
		}
		return nil
	}}
}

func TestPITRPreparationAuthenticatesWALPreservesExistingTargetsAndNeverClaimsSuccess(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			ctx := context.Background()
			c, _, identity := workerFixture(t)
			c.Environment, c.Stack = environment, "video-cloud-"+environment
			c.Remote.Prefix = environment + "/postgres-physical"
			engine := pitrFixtureEngine(t, c)
			capture, err := engine.Capture(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			root := filepath.Join(filepath.Dir(c.Directory), "restore with ' quote")
			if _, err := engine.PreparePITR(ctx, c, capture.File, identity, root, PITRTarget{at, 1, 1024}); err == nil {
				t.Fatal("missing continuous WAL accepted")
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatal("failed preparation left an unverified target")
			}
			name := "000000010000000000000002"
			source := filepath.Join(filepath.Dir(identity), name)
			workerWrite(t, source, []byte("authenticated fixture WAL"))
			if _, err := engine.ArchiveWAL(ctx, c, name, source); err != nil {
				t.Fatal(err)
			}
			prepared, err := engine.PreparePITR(ctx, c, capture.File, identity, root, PITRTarget{at, 1, 1024})
			if err != nil || len(prepared.WAL) != 1 {
				t.Fatal("authenticated preparation failed", err)
			}
			raw, err := os.ReadFile(filepath.Join(root, "pgdata", "postgresql.conf"))
			if err != nil || !strings.Contains(string(raw), "recovery_target_action = 'pause'") || !strings.Contains(string(raw), "primary_conninfo = ''") || !strings.Contains(string(raw), "''\"''\"''") {
				t.Fatal("recovery fence or quoted target path missing", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "pgdata", "recovery.signal")); err != nil {
				t.Fatal("recovery startup signal absent")
			}
			if _, err := engine.PreparePITR(ctx, c, capture.File, identity, root, PITRTarget{at, 1, 1024}); err == nil {
				t.Fatal("existing recovery target reused")
			}
			after, _ := os.ReadFile(filepath.Join(root, "pgdata", "postgresql.conf"))
			if string(raw) != string(after) {
				t.Fatal("failed retry overwrote recovery state")
			}
			tooSmall := filepath.Join(filepath.Dir(c.Directory), "insufficient-wal-budget")
			if _, err := engine.PreparePITR(ctx, c, capture.File, identity, tooSmall, PITRTarget{at, 1, 1}); err == nil {
				t.Fatal("declared WAL cache capacity ignored")
			}
			if entries, err := os.ReadDir(tooSmall); err != nil || len(entries) != 0 {
				t.Fatal("failed bounded restore retained partial data")
			}
		})
	}
}

type repeatedWALPage struct{ *memoryRepository }

func (p repeatedWALPage) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	page, err := p.memoryRepository.ListObjectsV2(ctx, in, opts...)
	page.IsTruncated, page.NextContinuationToken = aws.Bool(true), aws.String("repeat")
	return page, err
}
func TestPITRWALInventoryRejectsUnboundedPaginationAndForeignTimeline(t *testing.T) {
	c, _, _ := workerFixture(t)
	remote := newMemoryRepository()
	prefix := remotePrefix(c) + "wal/"
	for _, name := range []string{"000000010000000000000001", "000000010000000000000002", "000000020000000000000003", "00000002.history"} {
		remote.objects[prefix+name+".json"] = []byte("fixture")
	}
	names, err := listPITRWAL(context.Background(), remote, c, 1, 1, "0000000000000002")
	if err != nil || len(names) != 1 || names[0] != "000000010000000000000002" {
		t.Fatal("starting range or selected timeline ignored", err)
	}
	if _, err := listPITRWAL(context.Background(), repeatedWALPage{remote}, c, 1, 2, "0000000000000001"); err == nil {
		t.Fatal("repeated archive pagination accepted")
	}
	remote.objects[prefix+"../unowned.json"] = []byte("fixture")
	if _, err := listPITRWAL(context.Background(), remote, c, 1, 2, "0000000000000001"); err == nil {
		t.Fatal("foreign WAL path accepted")
	}
}
