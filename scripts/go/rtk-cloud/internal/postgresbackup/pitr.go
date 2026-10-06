package postgresbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

type PITRTarget struct {
	At          time.Time `json:"at"`
	Timeline    uint32    `json:"timeline"`
	MaxWALBytes int64     `json:"max_wal_bytes"`
}
type PreparedPITR struct {
	Manifest            Manifest        `json:"base_backup"`
	Target              PITRTarget      `json:"target"`
	WAL                 []WALCompletion `json:"wal"`
	PreparedAt          time.Time       `json:"prepared_at"`
	ConfigurationSHA256 string          `json:"configuration_sha256"`
}

// PreparePITR decrypts only authenticated WAL for the original system and
// configures native PostgreSQL recovery. Preparation is not restore success:
// qualification must start a quarantined target, observe recovery pause and
// verify the intended data and independently retained security state.
func (e Engine) PreparePITR(ctx context.Context, c Config, backup, identity, directory string, target PITRTarget) (prepared PreparedPITR, resultErr error) {
	if target.At.IsZero() || target.Timeline == 0 || target.MaxWALBytes < 1 || target.MaxWALBytes > c.MaxPlaintextBytes {
		return prepared, errors.New("PITR requires an exact UTC target time, timeline and bounded WAL cache")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	m, err := e.Restore(ctx, c, backup, identity, directory)
	if err != nil {
		return prepared, err
	}
	// Every failure removes only generated data inside the empty private target
	// accepted by Restore. An occupied directory is rejected before cleanup.
	defer func() {
		if resultErr != nil {
			removeExtracted(directory)
			_ = os.RemoveAll(filepath.Join(directory, "wal-cache"))
		}
	}()
	if target.At.Before(m.FinishedAt) || target.At.After(time.Now().UTC()) {
		return prepared, errors.New("PITR target must follow the base backup and cannot be in the future")
	}
	pgdata := filepath.Join(directory, "pgdata")
	startTimeline, startSegment, err := e.pitrStart(ctx, pgdata)
	if err != nil || target.Timeline < startTimeline {
		return prepared, errors.New("PITR timeline is incompatible with the authenticated base backup")
	}
	client, err := e.repository(c)
	if err != nil {
		return prepared, err
	}
	cache := filepath.Join(directory, "wal-cache")
	if err = recovery.PrivateDirectory(cache); err != nil {
		return prepared, err
	}
	if err = scratchSpace(cache, target.MaxWALBytes); err != nil {
		return prepared, err
	}
	names, err := listPITRWAL(ctx, client, c, startTimeline, target.Timeline, startSegment)
	if err != nil {
		return prepared, err
	}
	var total int64
	for _, name := range names {
		receipt, err := readWALCompletion(ctx, client, c, name)
		if err != nil || receipt.Manifest.Size > target.MaxWALBytes-total {
			return prepared, errors.New("PITR WAL identity, integrity or declared cache capacity failed")
		}
		if err := e.RestoreWAL(ctx, c, name, identity, filepath.Join(cache, name)); err != nil {
			return prepared, err
		}
		total += receipt.Manifest.Size
		prepared.WAL = append(prepared.WAL, receipt)
	}
	if len(prepared.WAL) == 0 {
		return prepared, errors.New("continuous WAL evidence is missing; a base backup alone cannot qualify PITR")
	}
	// PostgreSQL performs the ancestry, completeness and recovery-target checks.
	// The pause fence leaves writes and normal traffic disabled after replay.
	shellQuote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	configQuote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	restoreCommand := "cp -- " + shellQuote(filepath.Join(cache, "%f")) + " " + shellQuote("%p")
	settings := "\n# Managed isolated PITR; remain paused until independent security qualification.\narchive_mode = off\nhot_standby = on\nprimary_conninfo = ''\nprimary_slot_name = ''\nrestore_command = " + configQuote(restoreCommand) + "\nrecovery_target = ''\nrecovery_target_name = ''\nrecovery_target_lsn = ''\nrecovery_target_xid = ''\nrecovery_target_time = " + configQuote(target.At.UTC().Format("2006-01-02 15:04:05.999999999-07:00")) + "\nrecovery_target_timeline = " + configQuote(strconv.FormatUint(uint64(target.Timeline), 10)) + "\nrecovery_target_action = 'pause'\nrecovery_target_inclusive = true\n"
	var control cappedBuffer
	if err := e.run(ctx, []string{"pg_controldata", pgdata}, safeEnvironment(), &control); err != nil {
		return prepared, err
	}
	prerequisites, err := NativeRecoverySettings(control.String())
	if err != nil {
		return prepared, err
	}
	// A dedicated Job owns its private /tmp and Unix socket. There is no TCP
	// listener, inherited preload library, replication worker or source shell hook.
	settings += "listen_addresses = ''\nport = 6543\nunix_socket_directories = '/tmp'\nshared_preload_libraries = ''\nmax_logical_replication_workers = 0\ndefault_transaction_read_only = on\nssl = off\nhba_file = " + configQuote(filepath.Join(pgdata, "pg_hba.conf")) + "\n" + prerequisites
	for name, value := range map[string]string{"postgresql.conf": settings, "postgresql.auto.conf": "", "pg_hba.conf": "local all postgres peer\n"} {
		path := filepath.Join(pgdata, name)
		if info, err := os.Lstat(path); err != nil && !os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
			return prepared, errors.New("invalid isolated PostgreSQL configuration owner")
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			return prepared, errors.New("cannot persist isolated PITR configuration")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return prepared, errors.New("cannot restrict isolated PITR configuration")
		}
	}
	if err = os.Remove(filepath.Join(pgdata, "standby.signal")); err != nil && !os.IsNotExist(err) {
		return prepared, errors.New("cannot remove inherited standby startup")
	}
	f, err := os.OpenFile(filepath.Join(pgdata, "recovery.signal"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return prepared, errors.New("PITR signal already exists or cannot be staged")
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return prepared, errors.New("cannot persist PITR startup fence")
	}
	prepared.ConfigurationSHA256, err = pitrConfigurationDigest(directory)
	if err != nil {
		return prepared, err
	}
	prepared.Manifest, prepared.Target, prepared.PreparedAt = m, target, time.Now().UTC()
	return prepared, nil
}

func pitrConfigurationDigest(directory string) (string, error) {
	h := sha256.New()
	for _, name := range []string{"postgresql.conf", "postgresql.auto.conf", "pg_hba.conf", "recovery.signal"} {
		path := filepath.Join(directory, "pgdata", name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
			return "", errors.New("isolated PITR configuration owner or size changed")
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("isolated PITR configuration unavailable")
		}
		h.Write([]byte(name + "\x00"))
		h.Write(value)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (e Engine) pitrStart(ctx context.Context, pgdata string) (uint32, string, error) {
	raw, err := os.ReadFile(filepath.Join(pgdata, "backup_manifest"))
	if err != nil || len(raw) > 16<<20 {
		return 0, "", errors.New("native WAL range unavailable")
	}
	var manifest struct {
		WALRanges []struct {
			Timeline uint32
			StartLSN string `json:"Start-LSN"`
		} `json:"WAL-Ranges"`
	}
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.WALRanges) != 1 || manifest.WALRanges[0].Timeline == 0 {
		return 0, "", errors.New("native base backup requires one reviewed starting timeline")
	}
	var out cappedBuffer
	if err := e.run(ctx, []string{"pg_controldata", pgdata}, safeEnvironment(), &out); err != nil {
		return 0, "", err
	}
	var segmentBytes uint64
	for _, line := range strings.Split(out.String(), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "Bytes per WAL segment" {
			segmentBytes, _ = strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		}
	}
	if segmentBytes < 1<<20 || segmentBytes > 1<<30 || segmentBytes&(segmentBytes-1) != 0 {
		return 0, "", errors.New("native WAL segment size unavailable")
	}
	high, low, ok := strings.Cut(manifest.WALRanges[0].StartLSN, "/")
	h, eh := strconv.ParseUint(high, 16, 32)
	l, el := strconv.ParseUint(low, 16, 32)
	if !ok || eh != nil || el != nil {
		return 0, "", errors.New("native starting LSN invalid")
	}
	segment := ((h << 32) | l) / segmentBytes
	perLog := uint64(1<<32) / segmentBytes
	return manifest.WALRanges[0].Timeline, fmt.Sprintf("%08X%08X", segment/perLog, segment%perLog), nil
}

func listPITRWAL(ctx context.Context, client objectStore, c Config, first, last uint32, start string) ([]string, error) {
	prefix := remotePrefix(c) + "wal/"
	seen, tokens := map[string]bool{}, map[string]bool{}
	var token *string
	for {
		page, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.Remote.Bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil || page == nil {
			return nil, errors.New("PITR WAL inventory unavailable")
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".json") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
			if !walName.MatchString(name) {
				return nil, errors.New("invalid WAL completion in the selected archive inventory")
			}
			timeline, err := strconv.ParseUint(name[:8], 16, 32)
			if err != nil || timeline < uint64(first) || timeline > uint64(last) {
				continue
			}
			if len(name) >= 24 && uint32(timeline) == first && name[8:24] < start {
				continue
			}
			seen[name] = true
			if len(seen) > 100000 {
				return nil, errors.New("PITR WAL inventory exceeds the reviewed bounded recovery scope")
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		next := aws.ToString(page.NextContinuationToken)
		if next == "" || tokens[next] {
			return nil, errors.New("invalid or repeated WAL archive pagination")
		}
		tokens[next], token = true, aws.String(next)
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
