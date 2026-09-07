package recovery

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const maxWALHistoryBytes = 64 << 10
const maxWALMetadataBytes = 128 << 10

var walHistoryName = regexp.MustCompile(`^[0-9A-F]{8}\.history$`)
var historyTimeline = regexp.MustCompile(`^[0-9]+$`)
var historyLSN = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)

type walAncestor struct {
	timeline   uint32
	begin, end uint64
}

// History files carry no cluster identifier. Their provenance is the explicitly
// configured archive source and the subsequently authenticated envelope.
func parseWALHistory(name string, raw []byte) ([]walAncestor, error) {
	invalid := errors.New("invalid PostgreSQL timeline history")
	if !walHistoryName.MatchString(name) || len(raw) == 0 || len(raw) > maxWALHistoryBytes || bytes.IndexByte(raw, 0) >= 0 {
		return nil, invalid
	}
	target, _ := strconv.ParseUint(name[:8], 16, 32)
	if target <= 1 {
		return nil, invalid
	}
	var ancestors []walAncestor
	var last uint64
	var end uint64
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// PostgreSQL reads history lines into MAXPGPATH (1024 bytes).
	scanner.Buffer(make([]byte, 1024), 1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !historyTimeline.MatchString(fields[0]) || !historyLSN.MatchString(fields[1]) {
			return nil, invalid
		}
		tli, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil || tli <= last || tli >= target {
			return nil, invalid
		}
		parts := strings.Split(fields[1], "/")
		high, _ := strconv.ParseUint(parts[0], 16, 32)
		low, _ := strconv.ParseUint(parts[1], 16, 32)
		next := (high << 32) | low
		if next == 0 || next < end {
			return nil, invalid
		}
		ancestors = append(ancestors, walAncestor{uint32(tli), end, next})
		last, end = tli, next
	}
	if scanner.Err() != nil || len(ancestors) == 0 {
		return nil, invalid
	}
	return ancestors, nil
}

func walObjectID(name string) (string, error) {
	if walName.MatchString(name) {
		return "wal-" + strings.ToLower(name), nil
	}
	if walHistoryName.MatchString(name) {
		tli, _ := strconv.ParseUint(name[:8], 16, 32)
		if tli > 1 {
			return "history-" + strings.ToLower(name[:8]), nil
		}
	}
	return "", errors.New("complete WAL segment or timeline history name required")
}
func validWALSize(c WALConfig, name string, size int64) bool {
	if walHistoryName.MatchString(name) {
		return size > 0 && size <= maxWALHistoryBytes
	}
	return walName.MatchString(name) && size == c.SegmentBytes
}

// Read exactly the content which is validated, so hashing/encryption can replay
// these bytes even if the source is modified after this read.
func readWALPrefix(r io.Reader, name string, c WALConfig, history []byte) ([]byte, error) {
	if walHistoryName.MatchString(name) {
		if len(history) != 0 {
			return nil, errors.New("history cannot contain a nested history envelope")
		}
		raw, err := io.ReadAll(io.LimitReader(r, maxWALHistoryBytes+1))
		if err != nil {
			return nil, err
		}
		_, err = parseWALHistory(name, raw)
		return raw, err
	}
	h := make([]byte, 40)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return h, validateWALHeader(h, name, c)
	}
	if !walName.MatchString(name) {
		return nil, errors.New("invalid WAL segment name")
	}
	ancestors, err := parseWALHistory(name[:8]+".history", history)
	if err != nil {
		return nil, err
	}
	target, _ := strconv.ParseUint(name[:8], 16, 32)
	original := binary.LittleEndian.Uint32(h[4:])
	copyHeader := append([]byte(nil), h...)
	binary.LittleEndian.PutUint32(copyHeader[4:], uint32(target))
	if err = validateWALHeader(copyHeader, name, c); err != nil {
		return nil, err
	}
	address := binary.LittleEndian.Uint64(h[8:])
	// The child segment must contain its fork point; archived segments wholly
	// before that point belong to their ancestor's filename, not this child.
	if original == uint32(target) || address > ^uint64(0)-uint64(c.SegmentBytes) || address+uint64(c.SegmentBytes) <= ancestors[len(ancestors)-1].end {
		return nil, errors.New("unnecessary or unrelated WAL ancestor history")
	}
	for _, ancestor := range ancestors {
		if ancestor.timeline == original && address >= ancestor.begin && address < ancestor.end {
			return h, nil
		}
	}
	return nil, errors.New("WAL page timeline does not match history at segment start")
}

func readWALHistoryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxWALHistoryBytes {
		return nil, errors.New("regular bounded timeline history required beside WAL source")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("timeline history changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxWALHistoryBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("timeline history changed size")
	}
	return raw, nil
}
