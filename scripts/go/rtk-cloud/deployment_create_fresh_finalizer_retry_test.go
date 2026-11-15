package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshFourteenFinalizerResumesNativeFailureAndLostResponse(t *testing.T) {
	for _, mode := range []string{"native-503", "lost-native-response"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			runFreshFourteenProductProtocol(t, false, func(t *testing.T, f *productProtocolFixture, s environmentCreateServiceCeremony, _ []deploymentplan.Definition) {
				base := f.server.Config.Handler
				f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize") {
						posts++
						if posts == 1 {
							if mode == "native-503" {
								f.status = "sealed"
								w.WriteHeader(http.StatusServiceUnavailable)
								return
							}
							recorder := httptest.NewRecorder()
							base.ServeHTTP(recorder, r)
							if recorder.Code != http.StatusOK {
								t.Error("native finalizer did not commit before lost response")
							}
							delete(f.b.objects, "Secret/"+s.BootstrapCredentialNamespace+"/"+s.BootstrapCredentialSecret)
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = connection.Close()
							return
						}
					}
					base.ServeHTTP(w, r)
				})
				f.finalizeAttempts = func(finalize func() error) error {
					before := deploymentplan.Digest(f.rows)
					if err := finalize(); err == nil {
						t.Fatal("native finalizer interruption not reached")
					}
					entry := createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-finalize")
					if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
						t.Fatal("native failure lost the original pending intent")
					}
					started, intent := entry.StartedAt, entry.IntentSHA256
					entry.IntentSHA256 = strings.Repeat("f", 64)
					if err := finalize(); err == nil || posts != 1 {
						t.Fatal("changed original intent admitted a finalizer request")
					}
					entry.IntentSHA256 = intent
					issuer := f.next.ID
					f.next.ID = "99999999-9999-4999-8999-999999999999"
					if err := finalize(); err == nil || posts != 1 {
						t.Fatal("foreign native issuer admitted a finalizer request")
					}
					f.next.ID = issuer
					if mode == "lost-native-response" {
						before := deploymentplan.Digest(f.e.Journal)
						checked := 0
						for _, accepted := range f.e.Journal.Entries {
							if accepted.State == "completed" && strings.HasPrefix(accepted.Key, "kubernetes/") && strings.Contains(accepted.Key, "service-credential-secrets") {
								if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), f.store, f.e, f.runtime.Backend, accepted.Key); err != nil {
									t.Fatal("native completion lost reply blocked original owner history", err)
								}
								checked++
							}
						}
						if checked == 0 || before != deploymentplan.Digest(f.e.Journal) {
							t.Fatal("native completion history mutated original evidence or missed credential source")
						}
					}
					if mode == "native-503" {
						// A renewed global session has no cleanup tombstone before native
						// finalization. Observing that prefix must not require one.
						runtime := f.runtime
						runtime.ServiceSessionRecovery = &createServiceSessionRecovery{Renewal: true, SecondSession: true, RenewedIntent: createServiceSessionIntent{SessionID: s.BootstrapSessionID, Subjects: slices.Clone(f.subjects)}}
						if _, done, err := resumeSecondSessionNativeCleanup(t.Context(), f.cfg, f.store, runtime, s); err != nil || done {
							t.Fatal("unfinished native finalization incorrectly requires completed custody", err)
						}
						originalInstalled := slices.Clone(f.installed)
						f.installed = f.installed[:len(f.installed)-1]
						if err := finalize(); err == nil {
							t.Fatal("pending finalizer resumed with incomplete native ACKs")
						}
						f.installed = originalInstalled
						if posts != 1 {
							t.Fatal("invalid pending state repeated native POST")
						}
					}
					if err := finalize(); err != nil {
						return err
					}
					entry = createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-finalize")
					if entry == nil || !entry.StartedAt.Equal(started) || entry.IntentSHA256 != intent || entry.State != "completed" || deploymentplan.Digest(f.rows) != before {
						t.Fatal("resume replaced original request or permanent issuance")
					}
					for _, accepted := range f.e.Journal.Entries {
						if accepted.State == "completed" && strings.HasPrefix(accepted.Key, "kubernetes/") && strings.Contains(accepted.Key, "service-credential-secrets") {
							if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), f.store, f.e, f.runtime.Backend, accepted.Key); err != nil {
								t.Fatal("completed cleanup blocked original credential history", err)
							}
						}
					}
					return nil
				}
			})
			want := 2
			if mode == "lost-native-response" {
				want = 1
			}
			if posts != want {
				t.Fatal("native completed finalization replayed", posts, want)
			}
		})
	}
}
