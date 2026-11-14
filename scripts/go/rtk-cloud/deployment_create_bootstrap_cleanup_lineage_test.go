package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The real product ceremony supplies the current session and credentials.
// Call the common finalizer directly to prove its guard protects callers that
// have not already performed their own installation-completeness check.
func TestBootstrapFinalizerRequiresAllACKsBeforeAnyCleanup(t *testing.T) {
	f := newProductProtocolFixture(t, true)
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	installed, err := readProductInstalled(f.store, f.l)
	if err != nil {
		t.Fatal(err)
	}
	s := installed.Ceremony
	f.installed = append([]string(nil), f.subjects[:len(f.subjects)-1]...)
	namespace := f.p.Stack + "-account-manager"
	primary := f.b.objects["Secret/"+s.BootstrapCredentialNamespace+"/"+s.BootstrapCredentialSecret]
	copy := deploymentplan.Clone(primary)
	metadata := deploymentplan.Map(copy["metadata"])
	metadata["namespace"], metadata["uid"], metadata["resourceVersion"] = namespace, "77777777-7777-4777-8777-777777777777", "1"
	id := deploymentplan.ID(copy)
	f.b.objects[id] = copy
	s.BootstrapCredentialNamespaces = append(s.BootstrapCredentialNamespaces, namespace)
	s.CredentialUIDs[namespace] = deploymentplan.String(metadata["uid"])
	posts, deletes := 0, 0
	base := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize") {
			posts++
		}
		if r.URL.Path == "/boundary/kubectl" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			var request struct {
				Args []string `json:"args"`
			}
			if json.Unmarshal(raw, &request) != nil {
				t.Error("malformed isolated native command")
				w.WriteHeader(500)
				return
			}
			for _, arg := range request.Args {
				if arg == "delete" {
					deletes++
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		base.ServeHTTP(w, r)
	})
	before := deploymentplan.Digest(f.bootstrap)
	err = finalizeEnvironmentCreateServiceSession(t.Context(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, s, nil)
	if err == nil {
		t.Fatal("incomplete current session reached finalization")
	}
	if posts != 0 || deletes != 0 || f.b.objects[id] == nil || before != deploymentplan.Digest(f.bootstrap) {
		t.Fatalf("cleanup preceded actual all-ACK proof: finalize_posts=%d deletes=%d copy_present=%v", posts, deletes, f.b.objects[id] != nil)
	}
	if createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime)) != nil {
		t.Fatal("incomplete session admitted native finalization")
	}
}

// This fixture begins with authenticated historical sessions and real scoped
// parent/child journals. Only native Kubernetes/custody transport is isolated.
func TestSecondSessionBootstrapCleanupRetiresAuthenticatedAncestors(t *testing.T) {
	f := newBootstrapCleanupLineageFixture(t)
	current := f.service
	if err := retireExpiredServiceBootstrapCopies(t.Context(), f.cfg, f.store, f.runtime, current); err != nil {
		t.Fatal("retire admitted ancestors", err)
	}
	for _, sid := range []string{f.q.Renewal.OriginalState.Session, f.q.Renewal.Intent.SessionID} {
		actual, err := f.native.Read(t.Context(), "Secret/"+current.BootstrapCredentialNamespace+"/service-bootstrap-"+sid)
		if err != nil || actual != nil {
			t.Fatal("expired ancestor copy retained", err)
		}
	}
	actual, err := f.native.Read(t.Context(), "Secret/"+current.BootstrapCredentialNamespace+"/"+current.BootstrapCredentialSecret)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != current.CredentialUIDs[current.BootstrapCredentialNamespace] {
		t.Fatal("cleanup changed the current credential owner", err)
	}
	if err := retireExpiredServiceBootstrapCopies(t.Context(), f.cfg, f.store, f.runtime, current); err != nil {
		t.Fatal("completed ancestor retirement retry", err)
	}
}

func TestSecondSessionBootstrapCleanupArchivesOriginalAndResumesLink(t *testing.T) {
	for _, stop := range []string{"cleanup-stop-after-archive", "cleanup-stop-after-unlink"} {
		t.Run(stop, func(t *testing.T) {
			f := newBootstrapCleanupLineageFixture(t)
			r := f.runtime.ServiceSessionRecovery
			raw, err := f.native.command(t.Context(), nil, "--namespace", f.service.BootstrapCredentialNamespace, "exec", "deployment/"+f.service.CustodyReader, "--container", "custody", "--", "cat", r.RenewedCertPath)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := parseFirstCertificatePEM(raw)
			if err != nil {
				t.Fatal(err)
			}
			finalized := environmentCreateBootstrapFinalization{Environment: f.cfg.Environment, SessionID: f.service.BootstrapSessionID, LeafSHA256: environmentPreflightBytesSHA256(leaf.Raw), FinalizedAt: time.Now().UTC()}
			root := filepath.Join(f.nativeDir, "cleanup-custody")
			original, err := os.ReadFile(filepath.Join(root, "bootstrap.crt"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.nativeDir, stop), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := prepareRenewedBootstrapCleanup(t.Context(), f.cfg, f.runtime, f.service, finalized); err == nil {
				t.Fatal("native archive interruption was not reached")
			}
			archive := filepath.Join(root, "expired-"+f.q.Renewal.OriginalState.Session, "bootstrap.crt")
			archived, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(archived, original) {
				t.Fatal("original certificate was archived under another session", err)
			}
			if _, err := os.Stat(filepath.Join(root, "expired-"+r.Intent.SessionID)); !os.IsNotExist(err) {
				t.Fatal("original certificate was mislabeled as the predecessor")
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := prepareRenewedBootstrapCleanup(t.Context(), f.cfg, f.runtime, f.service, finalized); err != nil {
					t.Fatal("resume original archive/link", err)
				}
			}
			for name, want := range map[string]string{"bootstrap.key": r.OriginalKeySHA, "bootstrap.csr": r.OriginalCSRSHA} {
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || environmentPreflightBytesSHA256(raw) != want {
					t.Fatal("archive/link retry changed original private custody", err)
				}
			}
			canonical, err := os.ReadFile(filepath.Join(root, "bootstrap.crt"))
			if err != nil || !bytes.Equal(canonical, raw) {
				t.Fatal("canonical cleanup slot does not contain finalized current certificate", err)
			}
			key := createOpenBaoACKCompletionEntry(f.e.Journal, "bootstrap-renewed-cleanup-custody-"+r.Review)
			var proof struct {
				Original string `json:"original_session_id"`
			}
			if key == nil || key.State != "completed" || json.Unmarshal(key.Receipt, &proof) != nil || proof.Original != f.q.Renewal.OriginalState.Session {
				t.Fatal("archive receipt lost original session lineage")
			}
		})
	}
}

func TestSecondSessionBootstrapCleanupRejectsChangedAncestorOwners(t *testing.T) {
	for _, mode := range []string{"wrong-original", "wrong-ancestor", "wrong-uid", "live-uid", "unknown-installed", "current-replacement"} {
		t.Run(mode, func(t *testing.T) {
			f := newBootstrapCleanupLineageFixture(t)
			base := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service")
			path := filepath.Join(base, "session-observed.json")
			if mode == "wrong-ancestor" {
				path = filepath.Join(base, "session-renewal", f.q.Renewal.OriginalReview, "observed.json")
			}
			var observed createServiceSessionObserved
			if err := readExecutionJSON(path, &observed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong-original", "wrong-ancestor":
				observed.Cleanup.SessionID = "99999999-9999-4999-8999-999999999999"
			case "unknown-installed":
				statesPath := filepath.Join(f.nativeDir, "mint-states.json")
				var states map[string]managedSessionState
				if err := readExecutionJSON(statesPath, &states); err != nil {
					t.Fatal(err)
				}
				changed := states[f.q.Renewal.Intent.SessionID]
				changed.Installed = []string{"service:foreign"}
				states[changed.Session] = changed
				if err := os.WriteFile(statesPath, mustProviderRecoveryJSON(states), 0600); err != nil {
					t.Fatal(err)
				}
			case "live-uid":
				id := "Secret/" + f.service.BootstrapCredentialNamespace + "/service-bootstrap-" + f.q.Renewal.OriginalState.Session
				actual, err := f.native.Read(t.Context(), id)
				if err != nil || actual == nil {
					t.Fatal(err)
				}
				deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				if err := os.WriteFile(filepath.Join(f.nativeDir, strings.ReplaceAll(id, "/", "_")+".json"), mustProviderRecoveryJSON(actual), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-uid":
				observed.UIDs[f.service.BootstrapCredentialNamespace] = "99999999-9999-4999-8999-999999999999"
			case "current-replacement":
				observed.Cleanup.SessionID = f.service.BootstrapSessionID
				observed.UIDs = f.service.CredentialUIDs
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(observed), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.calls)
			if err != nil {
				t.Fatal(err)
			}
			if err := retireExpiredServiceBootstrapCopies(t.Context(), f.cfg, f.store, f.runtime, f.service); err == nil {
				t.Fatal("changed ancestor accepted")
			}
			after, err := os.ReadFile(f.calls)
			if err != nil || strings.Count(string(after), "delete --raw=/api/v1/") != strings.Count(string(before), "delete --raw=/api/v1/") {
				t.Fatal("changed ancestor caused native retirement", err)
			}
		})
	}
}

func TestSecondSessionBootstrapCleanupRequiresRetainedArchiveOnRetry(t *testing.T) {
	f := newBootstrapCleanupLineageFixture(t)
	if err := retireExpiredServiceBootstrapCopies(t.Context(), f.cfg, f.store, f.runtime, f.service); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "expired-credential-copies", f.q.Renewal.OriginalState.Session, f.service.BootstrapCredentialNamespace+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := retireExpiredServiceBootstrapCopies(t.Context(), f.cfg, f.store, f.runtime, f.service); err == nil {
		t.Fatal("completed retirement concealed a missing original archive")
	}
}

func TestSecondSessionBootstrapCleanupResumesAfterNativeUnlink(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, boundary := range []string{"cleanup-fail-after-native", "cleanup-fail-before-delete", "cleanup-fail-after-delete"} {
			name := boundary
			if named {
				name = "named-current/" + boundary
			}
			t.Run(name, func(t *testing.T) {
				f := newBootstrapCleanupLineageFixture(t)
				observation := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "observed.json")
				originalObservation, err := os.ReadFile(observation)
				if err != nil {
					t.Fatal(err)
				}
				if named {
					f.service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + f.service.BootstrapSessionID[:12]
					f.runtime.ServiceCeremony = &f.service
				}
				client, _, posts := installBootstrapCleanupFinalizer(t, f)
				if err := os.WriteFile(filepath.Join(f.nativeDir, boundary), nil, 0600); err != nil {
					t.Fatal(err)
				}
				err = finalizeEnvironmentCreateServiceSession(t.Context(), client, f.cfg, f.parent.Bootstrap, f.store, f.runtime, f.service, nil)
				if err == nil {
					t.Fatal("native cleanup interruption not reached")
				}
				root := filepath.Join(f.nativeDir, "cleanup-custody")
				var tombstone managedLateCustodyTombstone
				if err := readExecutionJSON(filepath.Join(root, "bootstrap.finalized.json"), &tombstone); err != nil {
					t.Fatal("genuine native cleanup tombstone missing", err)
				}
				for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatal("native cleanup did not unlink", name, err)
					}
				}
				local := filepath.Join(f.store.Root, environmentCreateCeremonyDirectory(f.runtime, "service"), "finalized.json")
				if _, err := os.Stat(local); !os.IsNotExist(err) {
					t.Fatal("interruption fabricated local finalization", err)
				}
				if *posts != 1 {
					t.Fatalf("native finalizer POST count before retry=%d", *posts)
				}
				if err := finalizeEnvironmentCreateServiceSession(t.Context(), client, f.cfg, f.parent.Bootstrap, f.store, f.runtime, f.service, nil); err != nil {
					t.Fatal("resume accepted native cleanup without erased private files", err)
				}
				if *posts != 1 {
					t.Fatal("cleanup retry repeated native finalization")
				}
				restored, err := secondSessionBootstrapCleanupCurrent(f.runtime)
				if err != nil || restored.CleanupBinding != f.service.CleanupBinding {
					t.Fatal("retry lost its exact recorded current cleanup binding", err)
				}
				retainedObservation, err := os.ReadFile(observation)
				if err != nil || !bytes.Equal(originalObservation, retainedObservation) {
					t.Fatal("cleanup retry rewrote immutable current observation", err)
				}
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime))
				if entry == nil || entry.State != "completed" {
					t.Fatal("original finalization request not completed")
				}
				if _, err := os.Stat(local); err != nil {
					t.Fatal("verified local finalization missing", err)
				}
				for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatal("retry recreated erased custody", name, err)
					}
				}
			})
		}
	}
}

func TestSecondSessionBootstrapCleanupRejectsUnprovenTombstoneRetry(t *testing.T) {
	for _, mode := range []string{"tombstone-key", "cleanup-proof", "pending-replacement", "pending-archive"} {
		t.Run(mode, func(t *testing.T) {
			f := newBootstrapCleanupLineageFixture(t)
			client, _, posts := installBootstrapCleanupFinalizer(t, f)
			stop := "cleanup-fail-after-native"
			if strings.HasPrefix(mode, "pending-") {
				stop = "cleanup-fail-before-delete"
			}
			if err := os.WriteFile(filepath.Join(f.nativeDir, stop), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := finalizeEnvironmentCreateServiceSession(t.Context(), client, f.cfg, f.parent.Bootstrap, f.store, f.runtime, f.service, nil); err == nil {
				t.Fatal("expected native cleanup interruption")
			}
			switch mode {
			case "tombstone-key":
				path := filepath.Join(f.nativeDir, "cleanup-custody/bootstrap.finalized.json")
				var marker managedLateCustodyTombstone
				if err := readExecutionJSON(path, &marker); err != nil {
					t.Fatal(err)
				}
				marker.Files["bootstrap.key"] = deploymentplan.Digest("foreign-key")
				if err := os.WriteFile(path, mustProviderRecoveryJSON(marker), 0600); err != nil {
					t.Fatal(err)
				}
			case "cleanup-proof":
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, "bootstrap-renewed-cleanup-custody-"+f.review)
				if entry == nil {
					t.Fatal("accepted cleanup proof missing")
				}
				value := jsonRawObject(entry.Receipt)
				value["csr_sha256"] = deploymentplan.Digest("foreign-csr")
				entry.Receipt = mustProviderRecoveryJSON(value)
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			case "pending-replacement":
				id := "Secret/" + f.service.BootstrapCredentialNamespace + "/service-bootstrap-" + f.q.Renewal.OriginalState.Session
				actual, err := f.native.Read(t.Context(), id)
				if err != nil || actual == nil {
					t.Fatal(err)
				}
				deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				if err := os.WriteFile(filepath.Join(f.nativeDir, strings.ReplaceAll(id, "/", "_")+".json"), mustProviderRecoveryJSON(actual), 0600); err != nil {
					t.Fatal(err)
				}
			case "pending-archive":
				path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "expired-credential-copies", f.q.Renewal.OriginalState.Session, f.service.BootstrapCredentialNamespace+".json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(f.calls)
			if err != nil {
				t.Fatal(err)
			}
			if err := finalizeEnvironmentCreateServiceSession(t.Context(), client, f.cfg, f.parent.Bootstrap, f.store, f.runtime, f.service, nil); err == nil {
				t.Fatal("unproven cleanup retry accepted")
			}
			after, err := os.ReadFile(f.calls)
			if err != nil {
				t.Fatal(err)
			}
			if *posts != 1 || strings.Count(string(before), "delete --raw=/api/v1/") != strings.Count(string(after), "delete --raw=/api/v1/") {
				t.Fatal("unproven cleanup retry mutated finalization or credentials")
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime))
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
				t.Fatal("unproven cleanup retry fabricated completion")
			}
		})
	}
}
