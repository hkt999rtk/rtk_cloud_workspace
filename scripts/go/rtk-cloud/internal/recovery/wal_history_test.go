package recovery

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWALHistoryValidation(t *testing.T) {
	for _, raw := range []string{"1\t0/180000\tpromotion\n", " # comment\n\n1 0/180000 reason\n2 0/280000 reason\n", "1 0/180000\n2 0/180000\n"} {
		if _, err := parseWALHistory("00000003.history", []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", "# only comments\n", "0 0/100\n", "3 0/100\n", "2 0/100\n1 0/200\n", "1 0/200\n2 0/100\n", "1 0/0\n", "1 0/100000000\n", "1 0/xyz\n", "+1 0/100\n", "1 0/100\x00\n", "1 0/100 " + strings.Repeat("x", 1100), strings.Repeat("#\n", maxWALHistoryBytes)} {
		if _, err := parseWALHistory("00000003.history", []byte(raw)); err == nil {
			t.Fatalf("invalid history accepted: %.80q", raw)
		}
	}
	for _, name := range []string{"00000000.history", "00000001.history", "../00000002.history", "00000002.HISTORY", "000000010000000000000001.partial", "000000010000000000000001.00000028.backup"} {
		if _, err := walObjectID(name); err == nil {
			t.Fatal("unsupported object name", name)
		}
	}
}

func TestWALHistoryAndInheritedSegmentRemoteRoundTrip(t *testing.T) {
	c, _, source, identity := walFixture(t)
	directory := filepath.Dir(source)
	historyName := "00000002.history"
	history := []byte("1\t0/180000\tno recovery target specified\n")
	historyPath := filepath.Join(directory, historyName)
	if err := os.WriteFile(historyPath, history, 0600); err != nil {
		t.Fatal(err)
	}
	branchName := "000000020000000000000001"
	branchPath := filepath.Join(directory, branchName)
	raw, _ := os.ReadFile(source)
	if err := os.WriteFile(branchPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "key")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{objects: map[string][]byte{}}
	remote := c.Remote
	remote.Prefix += "/wal-v1/" + c.SystemIdentifier
	cfg := Config{Environment: c.Environment, Stack: c.Stack, Remote: remote, MaxArchiveBytes: c.SegmentBytes + (1 << 20)}
	for _, name := range []string{historyName, branchName} {
		path := filepath.Join(directory, name)
		encrypted, id, err := stageWAL(context.Background(), c, name, path)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(encrypted)
		if _, _, err = stageWAL(context.Background(), c, name, path); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(encrypted)
		if !bytes.Equal(before, after) {
			t.Fatal("retry changed ciphertext")
		}
		if err = upload(context.Background(), objects, cfg, id, encrypted); err != nil {
			t.Fatal(err)
		}
		// Branch restoration needs only authenticated envelope history.
		if name == branchName {
			if err = os.Remove(historyPath); err != nil {
				t.Fatal(err)
			}
		}
		dest := path + ".restored"
		if err = restoreWAL(context.Background(), objects, c, name, dest, key); err != nil {
			t.Fatal(err)
		}
		actual, _ := os.ReadFile(dest)
		expected, _ := os.ReadFile(path)
		if !bytes.Equal(actual, expected) {
			t.Fatal("round trip changed bytes")
		}
	}
}

func TestWALInheritedHeaderRejectsUnrelatedHistory(t *testing.T) {
	c, _, source, _ := walFixture(t)
	raw, _ := os.ReadFile(source)
	name := "000000030000000000000001"
	for _, history := range []string{"", "2 0/180000\n", "1 0/100000\n", "1 0/300000\n", "1 0/180000\n2 0/380000\n"} {
		if _, err := readWALPrefix(bytes.NewReader(raw), name, c, []byte(history)); err == nil {
			t.Fatal("unrelated history accepted", history)
		}
	}
	valid := []byte("1 0/180000\n2 0/190000\n")
	if _, err := readWALPrefix(bytes.NewReader(raw), name, c, valid); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 2, 4, 8, 24, 32, 36} {
		bad := append([]byte(nil), raw...)
		bad[offset] ^= 128
		if _, err := readWALPrefix(bytes.NewReader(bad), name, c, valid); err == nil {
			t.Fatal("invalid branch header accepted", offset)
		}
	}
	binary.LittleEndian.PutUint32(raw[4:], 3)
	if _, err := readWALPrefix(bytes.NewReader(raw), name, c, valid); err == nil {
		t.Fatal("redundant lineage accepted")
	}
}

func TestRealPostgres16TimelineFixture(t *testing.T) {
	directory := os.Getenv("RTK_TEST_WAL_TIMELINE_DIR")
	if directory == "" {
		t.Skip("real PostgreSQL promotion fixture not configured")
	}
	c, _, source, identity := walFixture(t)
	c.SystemIdentifier = os.Getenv("RTK_TEST_WAL_SYSTEM_ID")
	c.SegmentBytes = 16 << 20
	key := filepath.Join(filepath.Dir(source), "key")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	histories, inherited := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if _, err := walObjectID(name); err != nil {
			continue
		}
		path := filepath.Join(directory, name)
		encrypted, _, err := stageWAL(context.Background(), c, name, path)
		if err != nil {
			t.Fatal(name, err)
		}
		dest := filepath.Join(filepath.Dir(source), name+".restored")
		if err = decryptWAL(context.Background(), c, name, encrypted, dest, key); err != nil {
			t.Fatal(name, err)
		}
		original, _ := os.ReadFile(path)
		actual, _ := os.ReadFile(dest)
		if !bytes.Equal(original, actual) {
			t.Fatal("real promotion bytes changed", name)
		}
		if walHistoryName.MatchString(name) {
			histories++
		} else {
			target, _ := strconv.ParseUint(name[:8], 16, 32)
			if len(original) >= 40 && binary.LittleEndian.Uint32(original[4:]) != uint32(target) {
				inherited++
			}
		}
	}
	if histories == 0 || inherited == 0 {
		t.Fatal("history and inherited segment fixtures required")
	}
}

func TestWALHistorySourceAndRetryFailures(t *testing.T) {
	c, _, source, _ := walFixture(t)
	directory := filepath.Dir(source)
	name := "000000020000000000000001"
	branch := filepath.Join(directory, name)
	raw, _ := os.ReadFile(source)
	if err := os.WriteFile(branch, raw, 0600); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(directory, "00000002.history")
	if _, _, err := stageWAL(context.Background(), c, name, branch); err == nil {
		t.Fatal("missing lineage accepted")
	}
	if err := os.Symlink(source, historyPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageWAL(context.Background(), c, name, branch); err == nil {
		t.Fatal("symlink lineage accepted")
	}
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte("1 0/180000 promotion\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageWAL(context.Background(), c, name, branch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte("1 0/190000 different fork\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageWAL(context.Background(), c, name, branch); err == nil {
		t.Fatal("changed retry lineage accepted")
	}
	if _, _, err := stageWAL(context.Background(), c, "00000002.history", historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte("1 0/1A0000 changed history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageWAL(context.Background(), c, "00000002.history", historyPath); err == nil {
		t.Fatal("changed history object accepted")
	}
}
