package recovery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validateRaftComponent(c Component) error {
	peers := map[string]bool{}
	for _, p := range c.RaftPeers {
		if !Name.MatchString(p) || peers[p] {
			return errors.New("invalid or duplicate Raft peer pod")
		}
		peers[p] = true
	}
	if len(peers) < 3 || len(peers)%2 == 0 || !peers[c.Pod] || !Name.MatchString(c.Container) {
		return errors.New("Raft requires an odd inventory of at least three peer pods, a selected pod and container")
	}
	for _, p := range []string{c.TokenFile, c.CAFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\r\n") {
			return errors.New("Raft requires absolute mounted token and CA file paths")
		}
	}
	if c.TLSServerName == "" || len(c.TLSServerName) > 253 {
		return errors.New("Raft requires a TLS server name")
	}
	for _, label := range strings.Split(c.TLSServerName, ".") {
		if !Name.MatchString(label) || strings.HasSuffix(label, "-") {
			return errors.New("invalid Raft TLS server name")
		}
	}
	if c.PVC != "" || c.Purpose != "" {
		return errors.New("Raft snapshots cannot select physical PVC storage")
	}
	return nil
}

func logicalPod(c Component, pod string) bool {
	if c.Kind == "openbao-raft" {
		for _, peer := range c.RaftPeers {
			if peer == pod {
				return true
			}
		}
		return false
	}
	return (c.Kind == "postgres" || c.Kind == "redis") && c.Pod == pod
}

// Read the token inside the pod, never into argv/output. Clear inherited proxy,
// namespace, skip-verification and retry settings. Never force a restore.
func (e *Engine) raftSnapshot(ctx context.Context, c Component, operation string, in io.Reader, out io.Writer) error {
	if err := validateRaftComponent(c); err != nil {
		return err
	}
	path := "/dev/stdout"
	if operation == "restore" {
		path = "/dev/stdin"
	} else if operation != "save" {
		return errors.New("unsupported Raft operation")
	}
	const script = `set -eu
BAO_TOKEN=$(cat "$1")
test -n "$BAO_TOKEN"
export BAO_TOKEN
export BAO_ADDR=https://127.0.0.1:8200 BAO_CACERT="$2" BAO_TLS_SERVER_NAME="$3" BAO_SKIP_VERIFY=false BAO_MAX_RETRIES=0
exec bao operator raft snapshot "$4" "$5"`
	return e.pod(ctx, c, in, out, "env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "sh", "-ec", script, "rtk-raft", c.TokenFile, c.CAFile, c.TLSServerName, operation, path)
}

// Check native gzip/tar structure and plain digests before maintenance. OpenBao
// checks sealed digests against the original seal during the actual restore.
func validateRaftSnapshot(path string, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid Raft snapshot gzip")
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: limit + 1}
	tr := tar.NewReader(limited)
	sums := map[string]string{}
	seen := map[string]bool{}
	var meta, checks []byte
	var stateSize int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid Raft snapshot archive")
		}
		if seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > limit {
			return errors.New("invalid Raft snapshot member")
		}
		seen[h.Name] = true
		switch h.Name {
		case "meta.json", "state.bin":
			hash := sha256.New()
			var dst io.Writer = hash
			var b bytes.Buffer
			if h.Name == "meta.json" {
				if h.Size > 8192 {
					return errors.New("oversized Raft metadata")
				}
				dst = io.MultiWriter(hash, &b)
			} else {
				stateSize = h.Size
			}
			if _, err = io.Copy(dst, tr); err != nil {
				return err
			}
			sums[h.Name] = hex.EncodeToString(hash.Sum(nil))
			if h.Name == "meta.json" {
				meta = b.Bytes()
			}
		case "SHA256SUMS", "SHA256SUMS.sealed":
			if h.Size > 8192 {
				return errors.New("oversized Raft checksums")
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if h.Name == "SHA256SUMS" {
				checks = b
			}
		default:
			return errors.New("unexpected Raft snapshot member")
		}
	}
	// Consume gzip trailer/checksum; only native tar zero padding may remain.
	buf := make([]byte, 4096)
	for {
		n, err := limited.Read(buf)
		for _, v := range buf[:n] {
			if v != 0 {
				return errors.New("trailing Raft snapshot payload")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid Raft snapshot trailer")
		}
	}
	if limited.N <= 0 || len(seen) != 4 || !seen["SHA256SUMS.sealed"] {
		return errors.New("incomplete or oversized Raft snapshot")
	}
	var metadata struct {
		Size  int64
		Index uint64
		Term  uint64
	}
	if json.Unmarshal(meta, &metadata) != nil || metadata.Size != stateSize || metadata.Index == 0 || metadata.Term == 0 {
		return errors.New("invalid Raft snapshot metadata")
	}
	verified := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(checks)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || verified[fields[1]] || sums[fields[1]] == "" || sums[fields[1]] != fields[0] {
			return errors.New("Raft snapshot checksum mismatch")
		}
		verified[fields[1]] = true
	}
	if len(verified) != 2 {
		return errors.New("incomplete Raft snapshot checksums")
	}
	return nil
}
