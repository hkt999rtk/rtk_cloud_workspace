package recovery

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var walBackupHistoryName = regexp.MustCompile(`^[0-9A-F]{24}\.[0-9A-F]{8}\.backup$`)
var walPartialName = regexp.MustCompile(`^[0-9A-F]{24}\.partial$`)
var backupLocation = regexp.MustCompile(`^([0-9A-F]{1,8}/[0-9A-F]{1,8}) \(file ([0-9A-F]{24})\)$`)

func walSegmentName(name string) (string, bool) {
	if walName.MatchString(name) {
		return name, true
	}
	if walPartialName.MatchString(name) {
		return name[:24], true
	}
	return "", false
}
func walNameAtLSN(timeline uint64, lsn uint64, size int64) string {
	seg := lsn / uint64(size)
	perLog := (uint64(1) << 32) / uint64(size)
	return fmt.Sprintf("%08X%08X%08X", timeline, seg/perLog, seg%perLog)
}

// PostgreSQL 16 writes informational backup history on a primary. It contains
// no system identifier; provenance is the configured cluster and age envelope.
func validateBackupHistory(name string, raw []byte, c WALConfig) error {
	invalid := errors.New("invalid PostgreSQL backup history identity/range")
	if !walBackupHistoryName.MatchString(name) || len(raw) == 0 || len(raw) > maxWALHistoryBytes || bytes.ContainsAny(raw, "\x00\r") || raw[len(raw)-1] != '\n' {
		return invalid
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(lines) < 10 || lines[3] != "BACKUP METHOD: streamed" || lines[4] != "BACKUP FROM: primary" || !strings.HasPrefix(lines[5], "START TIME: ") || len(lines[5]) <= len("START TIME: ") || !strings.HasPrefix(lines[6], "LABEL: ") {
		return invalid
	}
	// Labels are opaque and may include newlines. The final three lines are
	// emitted after the entire label, so injected label fields cannot set scope.
	tail := len(lines) - 3
	if !strings.HasPrefix(lines[tail], "START TIMELINE: ") || !strings.HasPrefix(lines[tail+1], "STOP TIME: ") || len(lines[tail+1]) <= len("STOP TIME: ") || !strings.HasPrefix(lines[tail+2], "STOP TIMELINE: ") {
		return invalid
	}
	label := strings.Join(lines[6:tail], "\n")
	if len(label) > len("LABEL: ")+1024 {
		return invalid
	}
	startTLI, err1 := strconv.ParseUint(strings.TrimPrefix(lines[tail], "START TIMELINE: "), 10, 32)
	stopTLI, err2 := strconv.ParseUint(strings.TrimPrefix(lines[tail+2], "STOP TIMELINE: "), 10, 32)
	if err1 != nil || err2 != nil || startTLI == 0 || stopTLI != startTLI {
		return invalid
	}
	if !strings.HasPrefix(lines[0], "START WAL LOCATION: ") || !strings.HasPrefix(lines[1], "STOP WAL LOCATION: ") || !strings.HasPrefix(lines[2], "CHECKPOINT LOCATION: ") {
		return invalid
	}
	start := backupLocation.FindStringSubmatch(strings.TrimPrefix(lines[0], "START WAL LOCATION: "))
	stop := backupLocation.FindStringSubmatch(strings.TrimPrefix(lines[1], "STOP WAL LOCATION: "))
	if len(start) != 3 || len(stop) != 3 {
		return invalid
	}
	startLSN, ok1 := baseLSN(start[1])
	stopLSN, ok2 := baseLSN(stop[1])
	checkpoint, ok3 := baseLSN(strings.TrimPrefix(lines[2], "CHECKPOINT LOCATION: "))
	if !ok1 || !ok2 || !ok3 || startLSN == 0 || stopLSN <= startLSN || checkpoint < startLSN || checkpoint >= stopLSN {
		return invalid
	}
	if start[2] != walNameAtLSN(startTLI, startLSN, c.SegmentBytes) || stop[2] != walNameAtLSN(stopTLI, stopLSN, c.SegmentBytes) {
		return invalid
	}
	expected := fmt.Sprintf("%s.%08X.backup", start[2], startLSN%uint64(c.SegmentBytes))
	if name != expected {
		return invalid
	}
	return nil
}
