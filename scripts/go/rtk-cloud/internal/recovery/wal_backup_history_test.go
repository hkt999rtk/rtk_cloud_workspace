package recovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const backupHistoryFixture = `START WAL LOCATION: 0/100028 (file 000000010000000000000001)
STOP WAL LOCATION: 0/180000 (file 000000010000000000000001)
CHECKPOINT LOCATION: 0/100060
BACKUP METHOD: streamed
BACKUP FROM: primary
START TIME: 2026-09-07 12:00:00 UTC
LABEL: test backup
START TIMELINE: 1
STOP TIME: 2026-09-07 12:00:02 UTC
STOP TIMELINE: 1
`
const backupHistoryFixtureName = "000000010000000000000001.00000028.backup"

func TestBackupHistoryIdentityAndRanges(t *testing.T) {
	c, _, _, _ := walFixture(t)
	if err := validateBackupHistory(backupHistoryFixtureName, []byte(backupHistoryFixture), c); err != nil {
		t.Fatal(err)
	}
	multiline := strings.Replace(backupHistoryFixture, "test backup", "test\nSTOP TIMELINE: 999\nbackup", 1)
	if err := validateBackupHistory(backupHistoryFixtureName, []byte(multiline), c); err != nil {
		t.Fatal("opaque multiline label rejected", err)
	}
	for _, replace := range [][2]string{{"0/100028", "0/100029"}, {"0/180000", "0/100000"}, {"0/100060", "0/900000"}, {"STOP TIMELINE: 1", "STOP TIMELINE: 2"}, {"START TIMELINE: 1", "START TIMELINE: 0"}, {"BACKUP FROM: primary", "BACKUP FROM: standby"}, {"BACKUP METHOD: streamed", "BACKUP METHOD: other"}, {"START TIME: 2026-09-07 12:00:00 UTC", "START TIME: "}, {"LABEL: test backup", "LABEL: " + strings.Repeat("x", 1025)}, {"CHECKPOINT LOCATION:", "INVALID:"}} {
		raw := strings.Replace(backupHistoryFixture, replace[0], replace[1], 1)
		if err := validateBackupHistory(backupHistoryFixtureName, []byte(raw), c); err == nil {
			t.Fatal("invalid backup history accepted", replace[0])
		}
	}
	for _, raw := range []string{backupHistoryFixture + "extra\n", backupHistoryFixture[:len(backupHistoryFixture)-1], backupHistoryFixture + "\x00", strings.Repeat("x", maxWALHistoryBytes+1)} {
		if err := validateBackupHistory(backupHistoryFixtureName, []byte(raw), c); err == nil {
			t.Fatal("malformed backup history accepted")
		}
	}
}
func TestBackupHistoryAndPartialRoundTrip(t *testing.T) {
	c, segment, source, identity := walFixture(t)
	dir := filepath.Dir(source)
	key := filepath.Join(dir, "identity")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{backupHistoryFixtureName, segment + ".partial"} {
		raw := []byte(backupHistoryFixture)
		if walPartialName.MatchString(name) {
			raw, _ = os.ReadFile(source)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		encrypted, id, err := stageWAL(context.Background(), c, name, path)
		if err != nil {
			t.Fatal(err)
		}
		if id == "wal-"+strings.ToLower(segment) {
			t.Fatal("partial/history collides with complete segment")
		}
		destination := path + ".restored"
		if err = decryptWAL(context.Background(), c, name, encrypted, destination, key); err != nil {
			t.Fatal(err)
		}
		if walPartialName.MatchString(name) {
			if err = decryptWAL(context.Background(), c, segment, encrypted, destination+".complete", key); err == nil {
				t.Fatal("partial substituted for complete WAL")
			}
		}
		got, _ := os.ReadFile(destination)
		if !bytes.Equal(got, raw) {
			t.Fatal("archive changed bytes")
		}
		if _, _, err = stageWAL(context.Background(), c, name, path); err != nil {
			t.Fatal("retry", err)
		}
		raw[len(raw)-1] ^= 1
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err = stageWAL(context.Background(), c, name, path); err == nil {
			t.Fatal("changed content reused archive")
		}
	}
	if validWALSize(c, segment+".partial", c.SegmentBytes-1) {
		t.Fatal("short partial accepted")
	}
}
func TestWALArchiveSettings(t *testing.T) {
	c, _, _, _ := walFixture(t)
	fragment, err := RenderWALArchiveConfig(c, "/private/quoted ' %f/config", "/private/rtk-cloud", 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"archive_mode = on", "archive_library = ''", "archive_timeout = '60s'", "--name ''%f'' --source ''%p''", "%%f"} {
		if !strings.Contains(fragment, want) {
			t.Fatal("missing archive setting", want)
		}
	}
	for _, seconds := range []int{0, 29, 301, 900} {
		if _, err = RenderWALArchiveConfig(c, "/private/config", "/private/tool", seconds); err == nil {
			t.Fatal("invalid interval accepted")
		}
	}
	if _, err = RenderWALArchiveConfig(c, "relative", "/private/tool", 60); err == nil {
		t.Fatal("relative config accepted")
	}
}
