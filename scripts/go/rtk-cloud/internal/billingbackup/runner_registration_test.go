package billingbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type countedManifestLister struct {
	singleManifestLister
	reads atomic.Int32
}

func (s *countedManifestLister) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads.Add(1)
	return s.memoryStore.Read(ctx, key)
}

func TestRunnerConfirmedRegistrationDoesNotReplayHistoricalObjectsAfterRestart(t *testing.T) {
	e, prefix, objects := fixture(t)
	// Signed manifest bytes are not necessarily canonical JSON. Preserve them
	// exactly across durable cache/restart instead of re-encoding for validation.
	var indented bytes.Buffer
	if err := json.Indent(&indented, objects.data[prefix+"/manifest.json"], "", "  "); err != nil {
		t.Fatal(err)
	}
	objects.data[prefix+"/manifest.json"] = indented.Bytes()
	verified, err := e.Verify(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	tracked := &countedManifestLister{singleManifestLister: singleManifestLister{objects, prefix}}
	e.Objects = tracked
	var registrations atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, "/verify") {
			registrations.Add(1)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer local.Close()
	client := Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}
	for cycle := 0; cycle < 3; cycle++ {
		status := runOneControllerCycle(t, e, tracked, client, Client{})
		if status.ProtectionOverdue || !status.VerifiedHorizon.Equal(verified.Manifest.CreatedAt) || status.ProtectedHighWater != verified.Manifest.HighWater || len(status.Errors) != 0 {
			t.Fatal("confirmed registration lost qualified protection", cycle, status)
		}
		if registrations.Load() != 1 || tracked.reads.Load() != 2 {
			t.Fatal("historical archive replayed after confirmed durable registration", cycle, registrations.Load(), tracked.reads.Load())
		}
	}
}

func TestRunnerAmbiguousRegistrationRetriesSameReceiptUntilDurablyConfirmed(t *testing.T) {
	e, prefix, objects := fixture(t)
	verified, err := e.Verify(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	tracked := &countedManifestLister{singleManifestLister: singleManifestLister{objects, prefix}}
	e.Objects = tracked
	var registrations atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, "/verify") {
			var submitted struct {
				SetID      string                          `json:"set_id"`
				Completion billingarchive.SignedCompletion `json:"completion"`
			}
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Error(err)
			}
			want, _ := json.Marshal(verified.Completion)
			got, _ := json.Marshal(submitted.Completion)
			if submitted.SetID != verified.Manifest.SetID || !bytes.Equal(want, got) {
				t.Error("ambiguous registration changed receipt bytes")
			}
			if registrations.Add(1) == 1 {
				// The server may have committed; no acknowledgement was confirmed.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer local.Close()
	client := Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}
	status := runOneControllerCycle(t, e, tracked, client, Client{})
	if !status.ProtectionOverdue || len(status.Errors) == 0 {
		t.Fatal("unconfirmed registration manufactured a durable acknowledgement", status)
	}
	file := filepath.Join(e.Config.Directory, verified.Manifest.SetID+".registered.json")
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatal("ambiguous POST was marked registered", err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		status = runOneControllerCycle(t, e, tracked, client, Client{})
		if status.ProtectionOverdue || len(status.Errors) != 0 || registrations.Load() != 2 || tracked.reads.Load() != 4 {
			t.Fatal("pending registration did not retry exactly once then stop replaying", status, registrations.Load(), tracked.reads.Load())
		}
	}
}

func TestRunnerRegistrationJournalRejectsTamperingOrUnapprovedScope(t *testing.T) {
	for _, mode := range []string{"manifest", "signature", "prefix", "permissions", "encryption-registry", "verifier-registry"} {
		t.Run(mode, func(t *testing.T) {
			e, prefix, _ := fixture(t)
			verified, err := e.Verify(context.Background(), prefix)
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := e.readManifest(context.Background(), prefix)
			if err != nil {
				t.Fatal(err)
			}
			saved := archiveRegistration{prefix, raw, verified.Completion}
			file := filepath.Join(e.Config.Directory, verified.Manifest.SetID+".registered.json")
			switch mode {
			case "manifest":
				saved.ManifestBytes = []byte(`{}`)
			case "signature":
				saved.Completion.SignatureB64 = "invalid"
			case "prefix":
				saved.Prefix += "-foreign"
			case "encryption-registry":
				delete(e.Config.EncryptionRecipients, verified.Manifest.EncryptionKeyID)
			case "verifier-registry":
				delete(e.Config.VerifierKeys, e.Config.VerifierKeyID)
			}
			if err := atomicJSON(file, saved); err != nil {
				t.Fatal(err)
			}
			if mode == "permissions" {
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise the cache loader directly when Config.Validate itself also
			// rejects a removed active key; no remote fallback masks corruption.
			if _, found, err := registeredArchive(e, prefix); err == nil || found {
				t.Fatal("invalid durable registration trusted", mode, found, err)
			}
		})
	}
}
