package cloudmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type collectorRunner struct {
	calls [][]string
	fn    func([]string) ([]byte, int, error)
}

func (f *collectorRunner) Run(_ context.Context, name string, args []string, _ []byte, _ []string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.fn(args)
}
func findCollectorResult(t *testing.T, c Collection, id string) Result {
	t.Helper()
	for _, r := range c.Results {
		if r.CheckID == id {
			return r
		}
	}
	t.Fatalf("missing result %s", id)
	return Result{}
}
func TestWorkloadReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       Status
	}{{"zero", `{"kind":"Deployment","metadata":{"generation":1},"spec":{"replicas":0}}`, Fail}, {"stale", `{"kind":"Deployment","metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":1,"readyReplicas":1}}`, Fail}, {"ready", `{"kind":"Deployment","metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"readyReplicas":1,"availableReplicas":1,"updatedReplicas":1}}`, Pass}, {"pvc", `{"kind":"PersistentVolumeClaim","status":{"phase":"Pending"}}`, Fail}} {
		t.Run(tc.name, func(t *testing.T) {
			var o kubeObject
			if json.Unmarshal([]byte(tc.body), &o) != nil {
				t.Fatal("fixture")
			}
			s, _ := workloadStatus(o)
			if s != tc.want {
				t.Fatalf("status %s", s)
			}
		})
	}
}
func TestKubernetesBulkMissingDriftAndUnknownIntent(t *testing.T) {
	f := &collectorRunner{fn: func(args []string) ([]byte, int, error) {
		return []byte(`{"items":[{"kind":"Deployment","metadata":{"name":"extra","namespace":"env-app"},"spec":{"replicas":1},"status":{"readyReplicas":1}}]}`), 0, nil
	}}
	inv := Inventory{Targets: []WorkloadTarget{{ID: "missing", Name: "missing", Service: "s", Namespace: "env-app", Kind: "Deployment", Enabled: true, Required: true}, {ID: "uncertain", Name: "extra", Service: "s", Namespace: "env-app", Kind: "Deployment", Enabled: true, Required: true, IntentUnknown: true}}}
	c := CollectKubernetes(context.Background(), Config{Concurrency: 1}, inv, Runtime{}, f, time.Now())
	if len(f.calls) != 1 {
		t.Fatalf("expected namespace bulk request, got %d", len(f.calls))
	}
	if findCollectorResult(t, c, "k8s/missing").Status != Fail {
		t.Fatal("missing expected workload must fail")
	}
	if findCollectorResult(t, c, "k8s/uncertain").Status != Unknown {
		t.Fatal("unknown intent must stay unknown")
	}
}
func TestCertificateToolExitOneIsValidEvidence(t *testing.T) {
	now := time.Now().UTC()
	body, _ := json.Marshal(map[string]any{"environment": "dev", "stack": "video-cloud-dev", "checked_at": now, "results": []map[string]any{{"source": "endpoint/service", "kind": "certificate", "status": "RENEW_NOW", "not_after": now.Add(time.Hour)}}})
	f := &collectorRunner{fn: func(args []string) ([]byte, int, error) { return body, 1, errors.New("sensitive-stderr-DO-NOT-REPORT") }}
	c := CollectCertificates(context.Background(), Config{Environment: "dev", Stack: "video-cloud-dev", CertificateTool: "cert"}, Runtime{ConfigRoot: "/store/dev"}, f, now)
	if len(c.Results) != 2 || c.Results[0].Status != Pass || c.Results[1].Status != Warn {
		t.Fatalf("exit1 should retain result: %#v", c.Results)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "sensitive-stderr") {
		t.Fatal("diagnostic leak")
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "--config-root /store") {
		t.Fatal("certificate CLI must receive store parent")
	}
}
func TestCertificateInvalidOutputUnknown(t *testing.T) {
	for _, body := range []string{"", `{"environment":"other"}`, "passwordSECRET invalid JSON"} {
		f := &collectorRunner{fn: func([]string) ([]byte, int, error) { return []byte(body), 1, errors.New("secret") }}
		c := CollectCertificates(context.Background(), Config{Environment: "dev", CertificateTool: "tool"}, Runtime{}, f, time.Now())
		if c.Results[0].Status != Unknown || strings.Contains(c.Results[0].Reason, "passwordSECRET") {
			t.Fatal("invalid output must be unknown and redacted")
		}
	}
}

func TestPKITurnRecentSweepAfterCollectionStart(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	f := &collectorRunner{fn: func(args []string) ([]byte, int, error) {
		if strings.Contains(strings.Join(args, " "), "/app/pkiturn health") {
			raw, _ := json.Marshal(map[string]any{"running": false, "last_succeeded": true, "started_at": time.Now().Add(-2 * time.Second), "finished_at": time.Now().Add(-time.Second)})
			return raw, 0, nil
		}
		return nil, 1, errors.New("missing manifest")
	}}
	c := collectTurnPKI(context.Background(), Config{}, Runtime{}, f, WorkloadTarget{Service: "turn", Namespace: "env-turn"}, start)
	r := findCollectorResult(t, c, "pki/recent-sweep")
	if r.Status != Pass || !r.ObservedAt.After(start) {
		t.Fatalf("recent sweep after group start must pass: %#v", r)
	}
}

func TestKubernetesReadinessPodsPVCDriftAndController(t *testing.T) {
	now := time.Now()
	raw := []byte(`{"items":[{"kind":"Deployment","metadata":{"name":"pki-controller","generation":1},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"pki-controller","env":[{"name":"PKI_APP_CRL_WORKER_ENABLED","value":"true"}]}]}}},"status":{"observedGeneration":1,"readyReplicas":1,"updatedReplicas":1,"availableReplicas":1}},{"kind":"StatefulSet","metadata":{"name":"postgres","generation":1},"spec":{"replicas":1},"status":{"observedGeneration":1,"readyReplicas":1}},{"kind":"DaemonSet","metadata":{"name":"agent"},"status":{"desiredNumberScheduled":3,"numberReady":2}},{"kind":"Deployment","metadata":{"name":"stranger"}},{"kind":"PersistentVolumeClaim","metadata":{"name":"storage"},"status":{"phase":"Pending"}},{"kind":"Pod","metadata":{"name":"broken","uid":"one"},"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":4,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"OOMKilled"}}}]}},{"kind":"Pod","metadata":{"name":"completed"},"status":{"phase":"Succeeded"}}]}`)
	f := &collectorRunner{fn: func([]string) ([]byte, int, error) { return raw, 0, nil }}
	inv := Inventory{Targets: []WorkloadTarget{{ID: "pki", Name: "pki-controller", Service: "pki", Namespace: "env-ns", Kind: "Deployment", Enabled: true, Required: true}, {ID: "pg", Name: "postgres", Service: "pg", Namespace: "env-ns", Kind: "StatefulSet", Enabled: true, Required: true}, {ID: "agent", Name: "agent", Service: "ops", Namespace: "env-ns", Kind: "DaemonSet", Enabled: true, Required: true}, {ID: "disabled", Name: "disabled", Service: "optional", Kind: "Deployment", Enabled: false, DisabledReason: "explicitly disabled"}, {ID: "external", Name: "external", Service: "external", Kind: "external", Enabled: true, Required: true}}}
	c := CollectKubernetes(context.Background(), Config{}, inv, Runtime{}, f, now)
	for id, want := range map[string]Status{"k8s/pki": Pass, "k8s/pg": Pass, "k8s/agent": Fail, "k8s/disabled": NotApplicable, "k8s/external": Unknown, "pki/app-crl-worker": Pass, "k8s/drift": Warn, "k8s/pvc/storage": Fail, "k8s/pod/broken": Fail} {
		if r := findCollectorResult(t, c, id); r.Status != want {
			t.Fatalf("%s: %s", id, r.Status)
		}
	}
	if len(c.Samples) != 1 || c.Samples[0].Name != "container_restarts" || c.Samples[0].Value != 4 || c.Samples[0].Identity != "one/app" {
		t.Fatal("restart instance counter missing")
	}
	for _, r := range c.Results {
		if r.CheckID == "k8s/pod/completed" {
			t.Fatal("completed Job pod should not fail")
		}
	}
}
func TestWorkloadKindConditions(t *testing.T) {
	for _, tc := range []struct {
		body string
		want Status
	}{{`{"kind":"Deployment","spec":{"replicas":2},"status":{"readyReplicas":1}}`, Fail}, {`{"kind":"Deployment","spec":{"replicas":1},"status":{"readyReplicas":1}}`, Fail}, {`{"kind":"DaemonSet","status":{"desiredNumberScheduled":2,"numberReady":2}}`, Pass}, {`{"kind":"PersistentVolumeClaim","status":{"phase":"Bound"}}`, Pass}, {`{"kind":"Pod","status":{"phase":"Pending"}}`, Fail}, {`{"kind":"Pod","status":{"phase":"Running"}}`, Unknown}, {`{"kind":"Pod","status":{"phase":"Running","containerStatuses":[{"ready":true}]}}`, Pass}, {`{"kind":"Unsupported"}`, Unknown}} {
		var o kubeObject
		json.Unmarshal([]byte(tc.body), &o)
		status, _ := workloadStatus(o)
		if status != tc.want {
			t.Fatalf("%s status %s", tc.body, status)
		}
	}
}
func TestPKITurnInstalledRootCRLReadOnlyStates(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, path, kind string
		hours            int
		want             Status
	}{{"valid", "/run/pki-state/root.json", "root", 72, Pass}, {"expires", "/run/pki-state/root.json", "root", 12, Fail}, {"unsafe-path", "/tmp/unreviewed.json", "root", 72, Unknown}, {"no-roots", "/run/pki-state/root.json", "intermediate", 72, Unknown}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &collectorRunner{fn: func(args []string) ([]byte, int, error) {
				text := strings.Join(args, " ")
				if strings.Contains(text, "sweep") {
					t.Fatal("monitor must not run sweep")
				}
				if strings.Contains(text, "/app/pkiturn health") {
					raw, _ := json.Marshal(map[string]any{"running": false, "last_succeeded": true, "started_at": now.Add(-2 * time.Second), "finished_at": now.Add(-time.Second)})
					return raw, 0, nil
				}
				if strings.Contains(text, "PKI_TURN_APP_CRL_MANIFEST") {
					raw, _ := json.Marshal([]map[string]any{{"issuer": map[string]string{"issuer_id": "root-1", "kind": tc.kind, "trust_domain": "app"}, "state_path": tc.path}})
					return raw, 0, nil
				}
				raw, _ := json.Marshal(map[string]any{"crl": map[string]any{"issuer_id": "root-1", "next_update": now.Add(time.Duration(tc.hours) * time.Hour)}})
				return raw, 0, nil
			}}
			c := collectTurnPKI(context.Background(), Config{}, Runtime{}, f, WorkloadTarget{Service: "turn", Namespace: "env-turn"}, now)
			if r := findCollectorResult(t, c, "pki/root-crl-installed"); r.Status != tc.want {
				t.Fatalf("%s:%s", r.Status, r.Reason)
			}
		})
	}
}
func TestKubernetesPermissionFailureRemainsUnknownAndRedacted(t *testing.T) {
	f := &collectorRunner{fn: func([]string) ([]byte, int, error) { return nil, 1, errors.New("secret must not escape") }}
	c := CollectKubernetes(context.Background(), Config{}, Inventory{Targets: []WorkloadTarget{{ID: "s", Name: "s", Service: "s", Namespace: "env-ns", Kind: "Deployment", Enabled: true, Required: true}}}, Runtime{}, f, time.Now())
	for _, r := range c.Results {
		if r.Status != Unknown || strings.Contains(r.Reason, "secret") {
			t.Fatal("permission failure unknown redacted")
		}
	}
}
