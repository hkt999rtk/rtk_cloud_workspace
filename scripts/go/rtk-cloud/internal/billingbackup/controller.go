// Package billingbackup implements the off-cluster Billing archive verifier.
// It deliberately does not share the core archive format or private identities
// with a Logger writer. Environment activation is an operator qualification gate.
package billingbackup

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type Remote struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	SigningRegion string `json:"signing_region"`
	Bucket        string `json:"bucket"`
}

type Config struct {
	Version                 int                 `json:"version"`
	Environment             string              `json:"environment"`
	Stack                   string              `json:"stack"`
	StoreID                 string              `json:"store_id"`
	Directory               string              `json:"directory"`
	LoggerURL               string              `json:"logger_url"`
	BillingURL              string              `json:"billing_url"`
	Remote                  Remote              `json:"remote"`
	VerifierKeyID           string              `json:"verifier_key_id"`
	VerifierKeys            map[string]string   `json:"verifier_keys"`
	EncryptionRecipients    map[string][]string `json:"encryption_recipients"`
	SourceEventEnvironments []string            `json:"source_event_environments,omitempty"`
	AutoRetirement          bool                `json:"auto_retirement"`
	AutoCompaction          bool                `json:"auto_compaction"`
	PolicyID                string              `json:"policy_id"`
	PolicyVersion           int                 `json:"policy_version"`
	ClearanceID             string              `json:"clearance_id"`
	HotRetentionDays        int                 `json:"hot_retention_days,omitempty"`
}

func (c Config) Validate() error {
	if c.Version != 1 || !billingarchive.SafeID(c.Environment) || !billingarchive.SafeID(c.Stack) || len(c.StoreID) != 32 || !filepath.IsAbs(c.Directory) || !billingarchive.SafeID(c.VerifierKeyID) {
		return errors.New("invalid Billing controller scope")
	}
	if decoded, err := hex.DecodeString(c.StoreID); err != nil || len(decoded) != 16 || strings.ToLower(c.StoreID) != c.StoreID {
		return errors.New("invalid logical StoreID")
	}
	if c.Remote.Bucket != "rtk-cloud-"+c.Environment+"-billing-backup-"+c.Remote.Region || !billingarchive.SafeID(c.Remote.Region) || !billingarchive.SafeID(c.Remote.SigningRegion) {
		return errors.New("dedicated environment Billing backup bucket required")
	}
	u, err := url.Parse(c.Remote.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("HTTPS object storage endpoint required")
	}
	if _, err := c.PublicKeys(); err != nil {
		return err
	}
	if _, ok := c.VerifierKeys[c.VerifierKeyID]; !ok {
		return errors.New("active verifier key is not registered")
	}
	if c.AutoRetirement && (!billingarchive.SafeID(c.PolicyID) || c.PolicyVersion < 1 || !billingarchive.SafeID(c.ClearanceID)) {
		return errors.New("automatic retirement requires explicit approved policy and clearance")
	}
	if c.HotDays() < 90 || c.HotDays() > 36500 {
		return errors.New("invalid minimum hot retention configuration")
	}
	if len(c.EncryptionRecipients) == 0 {
		return errors.New("approved encryption recipient registry required")
	}
	for id, recipients := range c.EncryptionRecipients {
		if !billingarchive.SafeID(id) || len(recipients) == 0 || len(recipients) > 16 {
			return errors.New("invalid encryption recipient registry")
		}
		seen := map[string]bool{}
		for _, recipient := range recipients {
			if _, err := age.ParseX25519Recipient(recipient); err != nil || seen[recipient] {
				return errors.New("invalid or duplicate age recipient")
			}
			seen[recipient] = true
		}
	}
	if len(c.SourceEnvironments()) > 8 {
		return errors.New("source environment registry exceeds bound")
	}
	seenEnvironments := map[string]bool{}
	for _, environment := range c.SourceEnvironments() {
		if !billingarchive.SafeID(environment) || seenEnvironments[environment] {
			return errors.New("invalid source event environment registry")
		}
		seenEnvironments[environment] = true
	}
	return nil
}

func (c Config) SourceEnvironments() []string {
	if len(c.SourceEventEnvironments) == 0 {
		return []string{c.Environment}
	}
	return c.SourceEventEnvironments
}

func (c Config) HotDays() int {
	if c.HotRetentionDays == 0 {
		return 90
	}
	return c.HotRetentionDays
}

func (c Config) PublicKeys() (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for id, value := range c.VerifierKeys {
		b, err := base64.StdEncoding.DecodeString(value)
		if !billingarchive.SafeID(id) || err != nil || len(b) != ed25519.PublicKeySize {
			return nil, errors.New("invalid verifier public registry")
		}
		out[id] = ed25519.PublicKey(b)
	}
	if len(out) == 0 {
		return nil, errors.New("verifier public registry required")
	}
	return out, nil
}

// CheckCustodyFile validates real, private directories down from the explicitly
// resolved environment root. Parent locations outside that boundary are not
// asserted to be encrypted or escrowed by this filesystem check.
func CheckCustodyFile(root, file string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(file) {
		return errors.New("absolute custody paths required")
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || !strings.HasPrefix(filepath.ToSlash(rel), "operator/recovery/billing-backup/") {
		return errors.New("identity outside environment Billing custody")
	}
	for current := file; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("custody material unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe custody permissions or symlink")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Getuid() {
			return errors.New("custody owner mismatch")
		}
		if current == file {
			if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
				return errors.New("invalid private key file")
			}
		} else if !info.IsDir() {
			return errors.New("invalid custody directory")
		}
		if current == root {
			break
		}
	}
	return nil
}

func ReadIdentity(root, file string) ([]age.Identity, error) {
	if err := CheckCustodyFile(root, file); err != nil {
		return nil, err
	}
	if filepath.Ext(file) != ".agekey" {
		return nil, errors.New("native age identity required")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := age.ParseIdentities(io.LimitReader(f, 1<<20))
	if err != nil || len(ids) == 0 {
		return nil, errors.New("native identity unavailable; unwrap escrow separately")
	}
	return ids, nil
}

func ReadSigner(root, file string, expected ed25519.PublicKey) (ed25519.PrivateKey, error) {
	if err := CheckCustodyFile(root, file); err != nil {
		return nil, err
	}
	if filepath.Ext(file) != ".ed25519key" {
		return nil, errors.New("dedicated verifier signing key required")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid native Ed25519 key")
	}
	private := ed25519.PrivateKey(key)
	if !private.Public().(ed25519.PublicKey).Equal(expected) {
		return nil, errors.New("verifier key does not match approved registry")
	}
	return private, nil
}

type ObjectStore interface {
	Read(context.Context, string) (io.ReadCloser, error)
	PutImmutable(context.Context, string, []byte) error
}

type Engine struct {
	Config     Config
	Objects    ObjectStore
	Identities []age.Identity
	Signer     ed25519.PrivateKey
}
type Verified struct {
	Manifest   billingarchive.Manifest         `json:"manifest"`
	Completion billingarchive.SignedCompletion `json:"completion"`
	Prefix     string                          `json:"prefix"`
}

func Prefix(m billingarchive.Manifest, kind string) string {
	ns := "billing-inbox-snapshots"
	if kind == "events" {
		ns = "billing-raw"
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s", ns, m.Stack, m.StoreID, m.CreatedAt.UTC().Format("2006/01/02"), m.SetID)
}

func (e Engine) readManifest(ctx context.Context, prefix string) (billingarchive.Manifest, []byte, error) {
	var m billingarchive.Manifest
	if strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") || !strings.HasPrefix(prefix, "billing-inbox-snapshots/"+e.Config.Stack+"/"+e.Config.StoreID+"/") {
		return m, nil, errors.New("invalid archive prefix")
	}
	r, err := e.Objects.Read(ctx, prefix+"/manifest.json")
	if err != nil {
		return m, nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, billingarchive.MaxManifestBytes+1))
	if err != nil {
		return m, nil, err
	}
	m, err = e.validateManifest(prefix, b)
	return m, b, err
}

// The registration journal retains the original manifest bytes, not a
// re-encoding: the independently signed receipt binds those exact bytes.
func (e Engine) validateManifest(prefix string, b []byte) (billingarchive.Manifest, error) {
	m, err := billingarchive.DecodeManifest(strings.NewReader(string(b)))
	if err != nil {
		return m, err
	}
	if m.Environment != e.Config.Environment || m.Stack != e.Config.Stack || m.StoreID != e.Config.StoreID || Prefix(m, "snapshot") != prefix {
		return m, errors.New("manifest scope mismatch")
	}
	if m.CreatedAt.After(time.Now().UTC().Add(30 * time.Second)) {
		return m, errors.New("future writer horizon rejected")
	}
	approved := e.Config.SourceEnvironments()
	if len(approved) != len(m.SourceEventEnvironments) {
		return m, errors.New("unapproved historical source environment mapping")
	}
	for i, environment := range approved {
		if environment != m.SourceEventEnvironments[i] {
			return m, errors.New("source environment substitution")
		}
	}
	recipients, ok := e.Config.EncryptionRecipients[m.EncryptionKeyID]
	if !ok || len(recipients) != len(m.RecipientFingerprints) {
		return m, errors.New("unregistered archive encryption key")
	}
	for i, v := range recipients {
		if billingarchive.Fingerprint(v) != m.RecipientFingerprints[i] {
			return m, errors.New("archive recipient substitution")
		}
	}
	return m, nil
}

// Verify downloads to a new operation-owned private directory. Plaintext is
// removed on success or handled failure; a host crash still requires the
// recorded-directory cleanup procedure and encrypted storage qualification.
func (e Engine) Verify(ctx context.Context, prefix string) (Verified, error) {
	var out Verified
	if err := e.Config.Validate(); err != nil {
		return out, err
	}
	if e.Objects == nil || len(e.Identities) == 0 {
		return out, errors.New("isolated verifier dependencies required")
	}
	m, raw, err := e.readManifest(ctx, prefix)
	if err != nil {
		return out, err
	}
	if err = privateDirectory(e.Config.Directory); err != nil {
		return out, err
	}
	if err = checkScratch(e.Config.Directory, m, false); err != nil {
		return out, err
	}
	work, err := os.MkdirTemp(e.Config.Directory, "verify-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(work)
	if err = atomicJSON(filepath.Join(work, "operation.json"), map[string]string{"set_id": m.SetID, "prefix": prefix}); err != nil {
		return out, err
	}
	snapshotPath := filepath.Join(work, "inbox.bbolt")
	eventsPath := filepath.Join(work, "events.ndjson")
	snapshot, err := os.OpenFile(snapshotPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer snapshot.Close()
	events, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer events.Close()
	for _, p := range m.Parts {
		r, err := e.Objects.Read(ctx, Prefix(m, p.Kind)+"/"+p.Name)
		if err != nil {
			return out, err
		}
		w := snapshot
		if p.Kind == "events" {
			w = events
		}
		err = billingarchive.DecodePart(ctx, w, r, e.Identities, p)
		r.Close()
		if err != nil {
			return out, err
		}
	}
	if err = snapshot.Sync(); err != nil {
		return out, err
	}
	if err = events.Sync(); err != nil {
		return out, err
	}
	snapshot.Close()
	f, err := os.Open(snapshotPath)
	if err != nil {
		return out, err
	}
	hash := sha256.New()
	n, hashErr := io.Copy(hash, f)
	f.Close()
	if hashErr != nil || n != m.SnapshotBytes || hex.EncodeToString(hash.Sum(nil)) != m.SnapshotSHA256 {
		return out, errors.New("whole snapshot digest mismatch")
	}
	keys, _ := e.Config.PublicKeys()
	if err = logger.ValidateBillingSnapshot(ctx, snapshotPath, m, keys); err != nil {
		return out, err
	}
	if _, err = events.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	if err = logger.ValidateBillingSnapshotExports(ctx, snapshotPath, m, events); err != nil {
		return out, err
	}
	if _, err = events.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	count, maxTime, bindings, err := validateEvents(ctx, events, m)
	if err != nil {
		return out, err
	}
	if count != m.RecordCount || (!maxTime.Equal(m.MaxReceivedAt)) || bindings != m.RecordsBindingsSHA256 {
		return out, errors.New("raw binding, age or count mismatch")
	}
	payload := billingarchive.PayloadFor(m, raw, e.Config.VerifierKeyID, "billing-raw-v1", "rtk-cloud-billing-verifier-v1", time.Now().UTC())
	if m.CreatedAt.After(payload.VerifiedAt) || m.MaxReceivedAt.After(payload.VerifiedAt) {
		return out, errors.New("writer clock is ahead of verifier; retry without publishing completion")
	}
	completion, err := billingarchive.SignCompletion(payload, e.Signer)
	if err != nil {
		return out, err
	}
	if _, err = billingarchive.VerifyCompletion(completion, m, raw, keys); err != nil {
		return out, err
	}
	if prior, err := e.Objects.Read(ctx, prefix+"/complete.json"); err == nil {
		var existing billingarchive.SignedCompletion
		err = billingarchive.StrictDecode(prior, 32768, &existing)
		prior.Close()
		if err != nil {
			return out, err
		}
		if _, err = verifyCompletionNow(existing, m, raw, keys); err != nil {
			return out, err
		}
		completion = existing
	} else if !errors.Is(err, ErrObjectMissing) {
		return out, err
	}
	if saved, err := os.ReadFile(filepath.Join(e.Config.Directory, m.SetID+".verified.json")); err == nil {
		var previous Verified
		if err = billingarchive.StrictDecode(strings.NewReader(string(saved)), billingarchive.MaxManifestBytes+32768, &previous); err != nil {
			return out, err
		}
		if previous.Prefix != prefix {
			return out, errors.New("existing receipt scope mismatch")
		}
		if _, err = verifyCompletionNow(previous.Completion, m, raw, keys); err != nil {
			return out, err
		}
		completion = previous.Completion
	} else if !os.IsNotExist(err) {
		return out, err
	}
	b, _ := json.Marshal(completion)
	// Keep an independently controlled verification receipt before publishing.
	if err = atomicJSON(filepath.Join(e.Config.Directory, m.SetID+".verified.json"), Verified{m, completion, prefix}); err != nil {
		return out, err
	}
	if err = e.Objects.PutImmutable(ctx, prefix+"/complete.json", b); err != nil {
		return out, err
	}
	return Verified{m, completion, prefix}, nil
}

func verifyCompletionNow(c billingarchive.SignedCompletion, m billingarchive.Manifest, raw []byte, keys map[string]ed25519.PublicKey) (billingarchive.VerificationPayload, error) {
	p, err := billingarchive.VerifyCompletion(c, m, raw, keys)
	if err == nil && (m.CreatedAt.After(p.VerifiedAt) || p.MaxReceivedAt.After(p.VerifiedAt)) {
		err = errors.New("verification precedes captured receipt horizon")
	}
	if err == nil && p.VerifiedAt.After(time.Now().UTC().Add(30*time.Second)) {
		err = errors.New("future verification time rejected")
	}
	return p, err
}

func validateEvents(ctx context.Context, r io.Reader, m billingarchive.Manifest) (uint64, time.Time, string, error) {
	var count uint64
	var maxTime time.Time
	h := sha256.New()
	h.Write([]byte("["))
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64<<10), 2<<20)
	for scan.Scan() {
		if err := ctx.Err(); err != nil {
			return count, maxTime, "", err
		}
		var record billingarchive.ExportRecord
		if err := billingarchive.StrictDecode(strings.NewReader(scan.Text()), 2<<20, &record); err != nil {
			return count, maxTime, "", err
		}
		if record.Sequence != m.FromSequence+count || record.Sequence > m.ThroughSequence {
			return count, maxTime, "", errors.New("non-contiguous raw export")
		}
		stored, err := logger.ValidateBillingExportRecord(record)
		if err != nil {
			return count, maxTime, "", err
		}
		usageRaw, err := json.Marshal(stored.Event.Fields["usage_event"])
		if err != nil {
			return count, maxTime, "", err
		}
		ref, err := billingarchive.CanonicalUsageBinding(record.Sequence, record.ContentSHA256, usageRaw)
		if err != nil {
			return count, maxTime, "", err
		}
		b, err := json.Marshal(ref)
		if err != nil {
			return count, maxTime, "", err
		}
		if count > 0 {
			h.Write([]byte(","))
		}
		h.Write(b)
		count++
		if record.ReceivedAt.After(maxTime) {
			maxTime = record.ReceivedAt
		}
	}
	if err := scan.Err(); err != nil {
		return count, maxTime, "", err
	}
	h.Write([]byte("]"))
	return count, maxTime, hex.EncodeToString(h.Sum(nil)), nil
}

func privateDirectory(dir string) error {
	i, err := os.Lstat(dir)
	if err != nil {
		return errors.New("pre-existing private controller directory required")
	}
	if !i.IsDir() || i.Mode().Perm()&0077 != 0 || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe controller directory")
	}
	if stat, ok := i.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("controller directory owner mismatch")
	}
	return nil
}

// The verifier checks the whole intended plaintext requirement before opening
// scratch files. DecodePart also enforces per-part bounds and disk-full errors
// stop the operation; this check is not a claim about external filesystem quotas.
func checkScratch(dir string, m billingarchive.Manifest, eventsOnly bool) error {
	var need uint64 = 32 << 20
	for _, part := range m.Parts {
		if !eventsOnly || part.Kind == "events" {
			need += uint64(part.PlainBytes)
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return errors.New("scratch capacity unavailable")
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < need {
		return errors.New("insufficient private scratch capacity")
	}
	return nil
}

func atomicJSON(file string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(file); err == nil {
		if string(existing) != string(b) {
			return errors.New("immutable controller receipt conflict")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".receipt-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// link prevents replacement even if another controller published meanwhile.
	if err = os.Link(name, file); err != nil {
		return errors.New("controller publication conflict")
	}
	d, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
