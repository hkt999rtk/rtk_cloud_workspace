package postgresbackup

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeWALCommandVerifiesActualClusterBeforeArchive(t *testing.T) {
	config, _, _ := workerFixture(t)
	pgdata := t.TempDir()
	workerWrite(t, filepath.Join(pgdata, "PG_VERSION"), []byte("16\n"))
	name := "000000010000000000000001"
	identifier := config.Source.SystemIdentifier
	engine := Engine{Exec: func(_ context.Context, argv, env []string, out io.Writer) error {
		if len(argv) != 2 || argv[0] != "pg_controldata" || argv[1] != pgdata {
			t.Fatal("unexpected source verification command")
		}
		_, err := io.WriteString(out, "Database system identifier: "+identifier+"\n")
		return err
	}}
	ctx := context.Background()
	if err := engine.VerifyWALSource(ctx, config, pgdata, name, "pg_wal/"+name); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"../" + name, "pg_wal/other", filepath.Join(t.TempDir(), name)} {
		if engine.VerifyWALSource(ctx, config, pgdata, name, source) == nil {
			t.Fatal("foreign segment accepted")
		}
	}
	identifier = "999999"
	if engine.VerifyWALSource(ctx, config, pgdata, name, "pg_wal/"+name) == nil {
		t.Fatal("configured identifier substituted for actual cluster")
	}
}

func TestWALArchiveRestoreRetryAndSystemIdentity(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			config, _, identity := workerFixture(t)
			config.Environment = environment
			config.Stack = "video-cloud-" + environment
			config.Remote.Prefix = environment + "/postgres-physical"
			remote := newMemoryRepository()
			engine := Engine{remote: remote}
			ctx := context.Background()
			for _, name := range []string{"000000010000000000000001", "00000002.history", "000000010000000000000001.00000001.backup"} {
				source := filepath.Join(filepath.Dir(identity), name)
				content := []byte("durable authenticated WAL " + name)
				workerWrite(t, source, content)
				first, err := engine.ArchiveWAL(ctx, config, name, source)
				if err != nil {
					t.Fatal(err)
				}
				second, err := engine.ArchiveWAL(ctx, config, name, source)
				if err != nil || first != second {
					t.Fatal("retry did not reuse exact archived WAL", err)
				}
				for _, object := range remote.objects {
					if bytes.Contains(object, content) {
						t.Fatal("plaintext WAL was published")
					}
				}
				target := source + "-restored"
				if err := engine.RestoreWAL(ctx, config, name, identity, target); err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(target)
				if !bytes.Equal(got, content) {
					t.Fatal("WAL restore differs")
				}
				if engine.RestoreWAL(ctx, config, name, identity, target) == nil {
					t.Fatal("restore overwrote existing WAL")
				}
				workerWrite(t, source, []byte("different WAL"))
				if _, err := engine.ArchiveWAL(ctx, config, name, source); err == nil {
					t.Fatal("same WAL name overwritten")
				}
				foreign := config
				foreign.Source.SystemIdentifier = "99999"
				if engine.RestoreWAL(ctx, foreign, name, identity, target+"-foreign") == nil {
					t.Fatal("foreign system WAL accepted")
				}
			}
		})
	}
}

func TestWALCorruptionAndInvalidNameNeverAcknowledge(t *testing.T) {
	config, _, identity := workerFixture(t)
	ctx := context.Background()
	name := "000000010000000000000001"
	source := filepath.Join(filepath.Dir(identity), name)
	workerWrite(t, source, []byte("WAL"))
	remote := newMemoryRepository()
	remote.corrupt = true
	engine := Engine{remote: remote}
	if _, err := engine.ArchiveWAL(ctx, config, name, source); err == nil {
		t.Fatal("corrupt remote acknowledged")
	}
	if _, ok := remote.objects[walKey(config, name)+".json"]; ok {
		t.Fatal("failed read-back published completion")
	}
	remote.corrupt = false
	if _, err := engine.ArchiveWAL(ctx, config, name, source); err != nil {
		t.Fatal("retry after failed upload", err)
	}
	for _, bad := range []string{"../wal", name + ".partial", "../../../key", strings.ToLower("ABCDEF010000000000000001")} {
		if _, err := engine.ArchiveWAL(ctx, config, bad, source); err == nil {
			t.Fatal("invalid segment accepted")
		}
	}
	remote.corrupt = true
	target := source + "-restored"
	if engine.RestoreWAL(ctx, config, name, identity, target) == nil {
		t.Fatal("corrupt archive restored")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("failed restore left unverified WAL")
	}
}
