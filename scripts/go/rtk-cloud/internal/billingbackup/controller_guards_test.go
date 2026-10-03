package billingbackup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

func TestControllerConfigDefaultsAndRejectsUnqualifiedRegistry(t *testing.T) {
	e, _, _ := fixture(t)
	if err := e.Config.Validate(); err != nil || e.Config.HotDays() != 90 || e.Config.AutoRetirement || e.Config.AutoCompaction {
		t.Fatal("safe controller defaults changed", err)
	}
	if got := e.Config.SourceEnvironments(); len(got) != 1 || got[0] != e.Config.Environment {
		t.Fatal("implicit source scope changed", got)
	}
	clone := func() Config {
		raw, err := json.Marshal(e.Config)
		if err != nil {
			t.Fatal(err)
		}
		var out Config
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"version", func(c *Config) { c.Version = 2 }},
		{"scope", func(c *Config) { c.Environment = "../dev" }},
		{"store-length", func(c *Config) { c.StoreID = "short" }},
		{"store-hex", func(c *Config) { c.StoreID = strings.Repeat("g", 32) }},
		{"store-uppercase", func(c *Config) { c.StoreID = strings.Repeat("A", 32) }},
		{"directory", func(c *Config) { c.Directory = "relative" }},
		{"bucket-scope", func(c *Config) { c.Remote.Bucket = "shared-backup" }},
		{"bucket-shared", func(c *Config) {
			c.Environment = "shared"
			c.Remote.Bucket = "rtk-cloud-shared-billing-backup-" + c.Remote.Region
		}},
		{"bucket-region-underscore", func(c *Config) {
			c.Remote.Region = "us_sea"
			c.Remote.Bucket = "rtk-cloud-dev-billing-backup-" + c.Remote.Region
		}},
		{"bucket-region-empty-segment", func(c *Config) {
			c.Remote.Region = "us--sea"
			c.Remote.Bucket = "rtk-cloud-dev-billing-backup-" + c.Remote.Region
		}},
		{"bucket-region-trailing-separator", func(c *Config) {
			c.Remote.Region = "us-sea-"
			c.Remote.Bucket = "rtk-cloud-dev-billing-backup-" + c.Remote.Region
		}},
		{"bucket-length", func(c *Config) {
			c.Remote.Region = strings.Repeat("a", 45)
			c.Remote.Bucket = "rtk-cloud-dev-billing-backup-" + c.Remote.Region
		}},
		{"bucket-region", func(c *Config) { c.Remote.SigningRegion = "../region" }},
		{"endpoint-http", func(c *Config) { c.Remote.Endpoint = "http://storage.example" }},
		{"endpoint-empty", func(c *Config) { c.Remote.Endpoint = "https:///" }},
		{"endpoint-userinfo", func(c *Config) { c.Remote.Endpoint = "https://user:password@storage.example" }},
		{"endpoint-query", func(c *Config) { c.Remote.Endpoint = "https://storage.example?token=x" }},
		{"endpoint-fragment", func(c *Config) { c.Remote.Endpoint = "https://storage.example#fragment" }},
		{"endpoint-path", func(c *Config) { c.Remote.Endpoint = "https://storage.example/path" }},
		{"verifier-empty", func(c *Config) { c.VerifierKeys = nil }},
		{"verifier-key-id", func(c *Config) {
			c.VerifierKeys = map[string]string{"../key": base64.StdEncoding.EncodeToString(make([]byte, 32))}
		}},
		{"verifier-invalid-base64", func(c *Config) { c.VerifierKeys = map[string]string{"key": "invalid"} }},
		{"verifier-wrong-size", func(c *Config) {
			c.VerifierKeys = map[string]string{"key": base64.StdEncoding.EncodeToString(make([]byte, 31))}
		}},
		{"verifier-unregistered", func(c *Config) { c.VerifierKeyID = "unregistered-key" }},
		{"auto-without-financial-policy", func(c *Config) { c.AutoRetirement = true }},
		{"under-90-days", func(c *Config) { c.HotRetentionDays = 89 }},
		{"age-upper-bound", func(c *Config) { c.HotRetentionDays = 36501 }},
		{"recipient-empty-registry", func(c *Config) { c.EncryptionRecipients = nil }},
		{"recipient-key-id", func(c *Config) { c.EncryptionRecipients = map[string][]string{"../key": {"recipient"}} }},
		{"recipient-empty-set", func(c *Config) { c.EncryptionRecipients = map[string][]string{"key": {}} }},
		{"recipient-upper-bound", func(c *Config) { c.EncryptionRecipients = map[string][]string{"key": make([]string, 17)} }},
		{"recipient-invalid", func(c *Config) { c.EncryptionRecipients = map[string][]string{"key": {"invalid"}} }},
		{"recipient-duplicate", func(c *Config) {
			for id, recipients := range c.EncryptionRecipients {
				c.EncryptionRecipients[id] = []string{recipients[0], recipients[0]}
			}
		}},
		{"source-upper-bound", func(c *Config) { c.SourceEventEnvironments = make([]string, 9) }},
		{"source-invalid", func(c *Config) { c.SourceEventEnvironments = []string{"../staging"} }},
		{"source-duplicate", func(c *Config) { c.SourceEventEnvironments = []string{"dev", "dev"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clone()
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("unqualified automatic backup configuration accepted")
			}
		})
	}
	c := clone()
	c.AutoRetirement, c.PolicyID, c.PolicyVersion, c.ClearanceID, c.HotRetentionDays = true, "approved-policy", 1, "approved-clearance", 36500
	c.SourceEventEnvironments = []string{"dev", "staging"}
	if err := c.Validate(); err != nil || c.HotDays() != 36500 || len(c.SourceEnvironments()) != 2 {
		t.Fatal("explicit qualified registry rejected", err)
	}
}

func TestCustodyGuardsNativeIdentityAndSignerRoles(t *testing.T) {
	root := privateTestDir(t)
	dir := filepath.Join(root, "operator", "recovery", "billing-backup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "native.agekey")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(file, identity.String()+"\n")
	if ids, err := ReadIdentity(root, file); err != nil || len(ids) != 1 {
		t.Fatal("qualified native identity rejected", err)
	}
	for _, tc := range []struct{ root, path string }{{"relative", file}, {root, "relative"}, {root, root}, {dir, root}, {root, filepath.Join(root, "unqualified.agekey")}, {root, filepath.Join(dir, "missing.agekey")}, {root, dir}} {
		if err := CheckCustodyFile(tc.root, tc.path); err == nil {
			t.Fatal("unqualified custody path accepted", tc.path)
		}
	}
	write(file, "")
	if err := CheckCustodyFile(root, file); err == nil {
		t.Fatal("empty private identity accepted")
	}
	write(file, strings.Repeat("x", 1<<20+1))
	if err := CheckCustodyFile(root, file); err == nil {
		t.Fatal("oversized private identity accepted")
	}
	write(file, "not-an-age-key")
	if _, err := ReadIdentity(root, file); err == nil {
		t.Fatal("invalid native identity accepted")
	}
	write(file, "# comment without a private identity\n")
	if _, err := ReadIdentity(root, file); err == nil {
		t.Fatal("comment-only escrow accepted as native identity")
	}
	write(file, identity.String()+"\n")
	if err := os.Chmod(filepath.Dir(dir), 0750); err != nil {
		t.Fatal(err)
	}
	if err := CheckCustodyFile(root, file); err == nil {
		t.Fatal("readable ancestor custody directory accepted")
	}
	if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	public, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signerFile := filepath.Join(dir, "native.ed25519key")
	write(signerFile, base64.StdEncoding.EncodeToString(signer))
	if got, err := ReadSigner(root, signerFile, public); err != nil || !bytes.Equal(got, signer) {
		t.Fatal("approved dedicated signer rejected", err)
	}
	if _, err := ReadIdentity(root, signerFile); err == nil {
		t.Fatal("signer reused as recovery identity")
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigner(root, signerFile, other); err == nil {
		t.Fatal("signer not bound to approved public registry")
	}
	for _, bad := range []string{"invalid-base64", base64.StdEncoding.EncodeToString(make([]byte, 63))} {
		write(signerFile, bad)
		if _, err := ReadSigner(root, signerFile, public); err == nil {
			t.Fatal("malformed signer accepted")
		}
	}
	if _, err := ReadSigner(root, filepath.Join(dir, "missing.ed25519key"), public); err == nil {
		t.Fatal("missing signer accepted")
	}
}

func TestPrivateDirectoryScratchAndImmutableJournal(t *testing.T) {
	dir := privateTestDir(t)
	if err := privateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("nonexistent private controller directory accepted")
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(dir); err == nil {
		t.Fatal("readable private directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(privateTestDir(t), "linked-directory")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(link); err == nil {
		t.Fatal("symlink controller directory accepted")
	}
	path := filepath.Join(dir, "intent.json")
	value := map[string]string{"operation_id": "immutable"}
	if err := atomicJSON(path, value); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(path, value); err != nil {
		t.Fatal("identical durable intent not replayable", err)
	}
	if err := atomicJSON(path, map[string]string{"operation_id": "changed"}); err == nil {
		t.Fatal("immutable intent overwritten")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || privateDirectory(path) == nil {
		t.Fatal("journal mode or directory-type guard failed", err)
	}
	if err := atomicJSON(filepath.Join(dir, "bad.json"), make(chan int)); err == nil {
		t.Fatal("unserializable intent published")
	}
	if err := atomicJSON(dir, value); err == nil {
		t.Fatal("journal path replaced a directory")
	}
	if err := atomicJSON(filepath.Join(dir, "absent-parent", "intent.json"), value); err == nil {
		t.Fatal("missing custody directory silently created")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "intent.json" {
		t.Fatal("temporary publication file retained", files, err)
	}
	// A stale dangling directory entry must not be overwritten even though an
	// ordinary read reports that its target is absent. The hard-link publish
	// step must reject it and remove only its operation-owned temporary file.
	stale := filepath.Join(dir, "stale.json")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), stale); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(stale, value); err == nil {
		t.Fatal("journal publication replaced a pre-existing directory entry")
	}
	files, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".receipt-") {
			t.Fatal("failed publication retained temporary journal material")
		}
	}
	m := billingarchive.Manifest{Parts: []billingarchive.Part{{Kind: "snapshot", PlainBytes: 1 << 62}, {Kind: "events", PlainBytes: 1024}}}
	if err := checkScratch(dir, m, false); err == nil {
		t.Fatal("impossible full-archive scratch capacity accepted")
	}
	if err := checkScratch(dir, m, true); err != nil {
		t.Fatal("events-only scratch incorrectly includes snapshot", err)
	}
	if err := checkScratch(filepath.Join(dir, "absent"), m, true); err == nil {
		t.Fatal("unavailable scratch capacity accepted")
	}
}

func TestReadManifestRejectsScopeTimeSourceAndRecipientSubstitution(t *testing.T) {
	for _, damage := range []string{"prefix", "missing", "json", "scope", "future", "source-length", "source-identity", "key-unregistered", "recipient-count", "recipient-identity"} {
		t.Run(damage, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			ctx := context.Background()
			m, _, err := e.readManifest(ctx, prefix)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "prefix":
				prefix += "/../foreign"
			case "missing":
				delete(objects.data, prefix+"/manifest.json")
			case "json":
				objects.data[prefix+"/manifest.json"] = []byte(`{"version":1,"unknown":true}`)
			case "scope":
				m.Environment = "staging"
			case "future":
				m.CreatedAt = time.Now().UTC().Add(time.Hour)
				prefix = Prefix(m, "snapshot")
			case "source-length":
				e.Config.SourceEventEnvironments = []string{"dev", "staging"}
			case "source-identity":
				e.Config.SourceEventEnvironments = []string{"staging"}
			case "key-unregistered":
				delete(e.Config.EncryptionRecipients, m.EncryptionKeyID)
			case "recipient-count":
				e.Config.EncryptionRecipients[m.EncryptionKeyID] = append(e.Config.EncryptionRecipients[m.EncryptionKeyID], "extra")
			case "recipient-identity":
				m.RecipientFingerprints[0] = strings.Repeat("0", 64)
			}
			if damage == "scope" || damage == "future" || damage == "recipient-identity" {
				objects.data[prefix+"/manifest.json"], err = billingarchive.MarshalManifest(m)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err = e.readManifest(ctx, prefix); err == nil {
				t.Fatal("unqualified manifest accepted")
			}
		})
	}
}

type guardFailReadStore struct{ ObjectStore }

func (guardFailReadStore) Read(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(guardFailReader{}), nil
}

type guardFailReader struct{}

func (guardFailReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

type guardPathReadErrorStore struct {
	ObjectStore
	path string
}

func (s guardPathReadErrorStore) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	if path == s.path {
		return nil, errors.New("injected object store failure")
	}
	return s.ObjectStore.Read(ctx, path)
}

func TestVerifierRejectsUnusableScratchSignerAndSavedReceipts(t *testing.T) {
	for _, damage := range []string{"scratch-missing", "scratch-public", "signer-missing", "signer-foreign", "part-read-error", "completion-read-error", "completion-malformed", "saved-malformed", "saved-foreign-prefix", "saved-invalid-signature", "saved-unreadable"} {
		t.Run(damage, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			ctx := context.Background()
			m, _, err := e.readManifest(ctx, prefix)
			if err != nil {
				t.Fatal(err)
			}
			var saved Verified
			if strings.HasPrefix(damage, "saved-") || damage == "completion-malformed" {
				saved, err = e.Verify(ctx, prefix)
				if err != nil {
					t.Fatal(err)
				}
			}
			receipt := filepath.Join(e.Config.Directory, m.SetID+".verified.json")
			switch damage {
			case "scratch-missing":
				e.Config.Directory = filepath.Join(e.Config.Directory, "missing")
			case "scratch-public":
				if err := os.Chmod(e.Config.Directory, 0750); err != nil {
					t.Fatal(err)
				}
			case "signer-missing":
				e.Signer = nil
			case "signer-foreign":
				_, e.Signer, err = ed25519.GenerateKey(rand.Reader)
			case "part-read-error":
				e.Objects = guardPathReadErrorStore{e.Objects, Prefix(m, m.Parts[0].Kind) + "/" + m.Parts[0].Name}
			case "completion-read-error":
				e.Objects = guardPathReadErrorStore{e.Objects, prefix + "/complete.json"}
			case "completion-malformed":
				objects.data[prefix+"/complete.json"] = []byte(`{"unknown":true}`)
			case "saved-malformed":
				err = os.WriteFile(receipt, []byte(`{"unknown":true}`), 0600)
			case "saved-foreign-prefix":
				saved.Prefix = "foreign-prefix"
				var raw []byte
				raw, err = json.Marshal(saved)
				if err == nil {
					err = os.WriteFile(receipt, raw, 0600)
				}
			case "saved-invalid-signature":
				saved.Completion.SignatureB64 = "invalid-signature"
				var raw []byte
				raw, err = json.Marshal(saved)
				if err == nil {
					err = os.WriteFile(receipt, raw, 0600)
				}
			case "saved-unreadable":
				if err = os.Remove(receipt); err == nil {
					err = os.Mkdir(receipt, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Verify(ctx, prefix); err == nil {
				t.Fatal("unusable verifier dependency or saved receipt accepted")
			}
			if !strings.HasPrefix(damage, "saved-") && damage != "completion-malformed" {
				if _, exists := objects.data[prefix+"/complete.json"]; exists {
					t.Fatal("failed independent verification published a completion")
				}
			}
			files, err := os.ReadDir(filepath.Dir(receipt))
			if err != nil {
				// The explicit missing scratch test points at an absent child.
				if damage != "scratch-missing" {
					t.Fatal(err)
				}
				return
			}
			for _, file := range files {
				if strings.HasPrefix(file.Name(), "verify-") {
					t.Fatal("failed verification retained operation-owned plaintext")
				}
			}
		})
	}
}

func TestVerifierDependencyAndCompletionTimeGuards(t *testing.T) {
	e, prefix, _ := fixture(t)
	ctx := context.Background()
	for _, bad := range []Engine{{}, {Config: e.Config}, {Config: e.Config, Objects: e.Objects}} {
		if _, err := bad.Verify(ctx, prefix); err == nil {
			t.Fatal("unconfigured verifier accepted archive")
		}
		if _, _, _, err := bad.ReadRange(ctx, prefix, 1, 1); err == nil {
			t.Fatal("unconfigured archive reader accepted archive")
		}
	}
	bad := e
	bad.Objects = guardFailReadStore{e.Objects}
	if _, _, err := bad.readManifest(ctx, prefix); err == nil {
		t.Fatal("manifest I/O failure ignored")
	}
	m, raw, err := e.readManifest(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := e.Config.PublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, verifiedAt := range []time.Time{m.CreatedAt.Add(-time.Second), time.Now().UTC().Add(time.Minute)} {
		payload := billingarchive.PayloadFor(m, raw, e.Config.VerifierKeyID, "billing-raw-v1", "test", verifiedAt)
		signed, err := billingarchive.SignCompletion(payload, e.Signer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifyCompletionNow(signed, m, raw, keys); err == nil {
			t.Fatal("completion outside captured horizon or verifier clock accepted")
		}
	}
	for _, input := range []io.Reader{strings.NewReader("invalid\n"), strings.NewReader(strings.Repeat("x", (2<<20)+1)), guardFailReader{}} {
		if _, _, _, err := validateEvents(ctx, input, m); err == nil {
			t.Fatal("invalid or unreadable raw event stream accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := validateEvents(cancelled, strings.NewReader("{}\n"), m); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled verification continued", err)
	}
	if _, _, _, err := validateEvents(ctx, strings.NewReader(`{"sequence":"999"}`+"\n"), m); err == nil {
		t.Fatal("raw sequence gap accepted")
	}
	if _, _, _, err := validateEvents(ctx, strings.NewReader(`{"sequence":"1"}`+"\n"), m); err == nil {
		t.Fatal("unbound raw record accepted")
	}
}

func TestRecoveryStageGuardsNeverLeaveFailedPlaintext(t *testing.T) {
	for _, damage := range []string{"config", "dependencies", "relative-destination", "missing-destination", "manifest", "missing-completion", "invalid-completion", "foreign-completion", "part-read-error", "foreign-identity"} {
		t.Run(damage, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			ctx := context.Background()
			destination := privateTestDir(t)
			m, _, err := e.readManifest(ctx, prefix)
			if err != nil {
				t.Fatal(err)
			}
			if damage != "missing-completion" {
				if _, err := e.Verify(ctx, prefix); err != nil {
					t.Fatal(err)
				}
			}
			switch damage {
			case "config":
				e.Config.Version = 2
			case "dependencies":
				e.Identities = nil
			case "relative-destination":
				destination = "relative"
			case "missing-destination":
				destination = filepath.Join(destination, "missing")
			case "manifest":
				prefix += "/../foreign"
			case "invalid-completion":
				objects.data[prefix+"/complete.json"] = []byte(`{"unknown":true}`)
			case "foreign-completion":
				var completion billingarchive.SignedCompletion
				if err := json.Unmarshal(objects.data[prefix+"/complete.json"], &completion); err != nil {
					t.Fatal(err)
				}
				completion.SignatureB64 = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
				objects.data[prefix+"/complete.json"], err = json.Marshal(completion)
			case "part-read-error":
				e.Objects = guardPathReadErrorStore{e.Objects, Prefix(m, m.Parts[0].Kind) + "/" + m.Parts[0].Name}
			case "foreign-identity":
				var identity *age.X25519Identity
				identity, err = age.GenerateX25519Identity()
				e.Identities = []age.Identity{identity}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.StageRestore(ctx, prefix, destination); err == nil {
				t.Fatal("unqualified recovery stage accepted")
			}
			if damage == "relative-destination" || damage == "missing-destination" {
				return
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed recovery retained plaintext or published a live target", entries, err)
			}
		})
	}
}

func TestLifecycleTransportRejectsUnsafeInputsAndOversizeEvidence(t *testing.T) {
	ctx := context.Background()
	for _, base := range []string{"://bad", "https:///", "https://user:password@host", "https://host/path", "https://host?query=x", "https://host#fragment", "http://external.example", "ftp://localhost"} {
		if err := (Client{BaseURL: base, Token: strings.Repeat("x", 32)}).Request(ctx, http.MethodGet, "/v1/internal/status", nil, new(any)); err == nil {
			t.Fatal("unsafe lifecycle origin accepted", base)
		}
	}
	for _, path := range []string{"https://foreign.example/v1/internal/status", "/public/status", "/v1/internal/../escape", "/v1/internal/status#fragment"} {
		if err := (Client{BaseURL: "https://host", Token: strings.Repeat("x", 32)}).Request(ctx, http.MethodGet, path, nil, new(any)); err == nil {
			t.Fatal("unsafe lifecycle path accepted", path)
		}
	}
	for _, token := range []string{"short", strings.Repeat("x", 32) + "\n"} {
		if err := (Client{BaseURL: "https://host", Token: token}).Request(ctx, http.MethodGet, "/v1/internal/status", nil, new(any)); err == nil {
			t.Fatal("unqualified lifecycle credential accepted")
		}
	}
	client := Client{BaseURL: "https://host", Token: strings.Repeat("x", 32)}
	for _, body := range []any{make(chan int), strings.Repeat("x", 4<<20)} {
		if err := client.Request(ctx, http.MethodPost, "/v1/internal/status", body, new(any)); err == nil {
			t.Fatal("invalid or oversized lifecycle command accepted")
		}
	}
	if err := client.Request(ctx, "invalid\nmethod", "/v1/internal/status", nil, new(any)); err == nil {
		t.Fatal("invalid lifecycle method accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.Request(cancelled, http.MethodGet, "/v1/internal/status", nil, new(any)); err == nil || strings.Contains(err.Error(), "host") {
		t.Fatal("transport failure leaked endpoint or became authority evidence", err)
	}
	for _, body := range []string{"invalid", strings.Repeat("x", (8<<20)+1), `{}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, body)
		}))
		client := Client{BaseURL: server.URL, Token: strings.Repeat("x", 32), HTTP: &http.Client{}}
		var result any
		err := client.Request(ctx, http.MethodGet, "/v1/internal/status?scope=dev", nil, &result)
		if body == `{}` {
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Request(ctx, http.MethodGet, "/v1/internal/status", nil, nil); err != nil {
				t.Fatal("ack-only transport rejected", err)
			}
		} else if err == nil {
			t.Fatal("invalid or oversized evidence accepted")
		}
		server.Close()
	}
}
