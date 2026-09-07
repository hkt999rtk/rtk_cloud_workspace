package recovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configureArchiveFixture(t *testing.T, ctx context.Context, docker, container, directory string, c WALConfig, query func(string, string) string) {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("RTK_PITR_CLI_BINARY"))
	if err != nil {
		t.Fatal("Linux recovery CLI required", err)
	}
	tool := filepath.Join(directory, "archive-cli")
	if err = os.WriteFile(tool, raw, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "archive-wal.json")
	if err = WriteJSON(config, c); err != nil {
		t.Fatal(err)
	}
	if err = PrivateDirectory(c.Directory); err != nil {
		t.Fatal(err)
	}
	fragment, err := RenderWALArchiveConfig(c, config, tool, 60)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "archive.conf"), []byte(fragment), 0600); err != nil {
		t.Fatal(err)
	}
	if err = exec.CommandContext(ctx, docker, "exec", container, "chown", "-R", "postgres:postgres", directory).Run(); err != nil {
		t.Fatal(err)
	}
	// archive_mode and archive_timeout were set at disposable server startup;
	// install the generated command via ALTER SYSTEM and reload this server only.
	command := walArchiveCommand(c, config, tool)
	query("5432", "ALTER SYSTEM SET archive_command = '"+strings.ReplaceAll(command, "'", "''")+"'")
	query("5432", "SELECT pg_reload_conf()")
}
func checkArchiveFixture(t *testing.T, ctx context.Context, directory string, c WALConfig, key string, query func(string, string) string) {
	t.Helper()
	// Verify a segment archived after the backup history, proving it did not
	// stall the native archiver queue.
	query("5432", "INSERT INTO physical_fixture VALUES(4,'archiver-check')")
	walFile := query("5432", "SELECT pg_walfile_name(pg_current_wal_lsn())")
	query("5432", "SELECT pg_switch_wal()")
	id, _ := walObjectID(walFile)
	root := filepath.Join(directory, "objects", c.Remote.Prefix, "wal-v1", c.SystemIdentifier, c.Stack)
	for {
		if _, err := os.Stat(filepath.Join(root, id+".complete.json")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("native archive completion unavailable", ctx.Err())
		}
		time.Sleep(100 * time.Millisecond)
	}
	entries, err := os.ReadDir(c.Directory)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "backup-history-") {
			continue
		}
		f, err := os.Open(filepath.Join(c.Directory, entry.Name(), "receipt.json"))
		if err != nil {
			t.Fatal(err)
		}
		var receipt walReceipt
		err = Decode(f, &receipt)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !walBackupHistoryName.MatchString(receipt.Name) {
			t.Fatal("unexpected native history name")
		}
		if _, err = os.Stat(filepath.Join(root, entry.Name()+".complete.json")); err != nil {
			t.Fatal("native backup history completion missing", err)
		}
		encrypted := filepath.Join(c.Directory, entry.Name(), entry.Name()+".age")
		destination := filepath.Join(directory, "native-"+receipt.Name)
		if err = decryptWAL(ctx, c, receipt.Name, encrypted, destination, key); err != nil {
			t.Fatal("real backup-history restore", err)
		}
		found = true
	}
	if !found {
		t.Fatal("pg_basebackup did not produce archived backup history")
	}
	if got := query("5432", "SELECT failed_count FROM pg_stat_archiver"); got != "0" {
		t.Fatal("native archive failures", got)
	}
	// PostgreSQL can update its stats just after completion publication.
	for {
		got := query("5432", "SELECT last_archived_wal FROM pg_stat_archiver")
		if got == walFile {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("native archiver did not acknowledge completed segment", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("native archive_command uploaded WAL and generated backup history with zero failures, then archived the next segment")
}
