package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOriginalKeyBootstrapExpiryRequiresRealCompleteInstallation(t *testing.T) {
	now := time.Now().UTC()
	s := environmentCreateServiceCeremony{BootstrapSessionID: "current", Root: environmentCreateRegisteredIssuer{RootFingerprint: "original-root"}, Intermediate: environmentCreateRegisteredIssuer{ID: "original-intermediate"}}
	valid := completedBootstrapExpiry{Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Root: s.Root.RootFingerprint, Status: "active", Expires: now.Add(-time.Second), Complete: true}
	if !completedBootstrapMayExpire(valid, s, now) {
		t.Fatal("actual completed expired session rejected")
	}
	for _, mode := range []string{"incomplete", "still-valid", "unknown-time", "other-active", "foreign-session", "foreign-root", "foreign-issuer", "already-sealed"} {
		t.Run(mode, func(t *testing.T) {
			state := valid
			switch mode {
			case "incomplete":
				state.Complete = false
			case "still-valid":
				state.Expires = now.Add(time.Minute)
			case "unknown-time":
				state.Expires = time.Time{}
			case "other-active":
				state.Others = 1
			case "foreign-session":
				state.Session = "different"
			case "foreign-root":
				state.Root = "different"
			case "foreign-issuer":
				state.Issuer = "different"
			case "already-sealed":
				state.Status = "sealed"
			}
			if completedBootstrapMayExpire(state, s, now) {
				t.Fatal("unsafe expiry accepted", mode)
			}
		})
	}
}

func TestOriginalKeyBootstrapRecoveryRequiresBoundStoppedChild(t *testing.T) {
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, []deploymentplan.Definition{{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "failed", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "no-lease", "wrong-intent", "receipt", "accepted", "in-flight", "completed-write", "missing-owner", "missing-failure"} {
		t.Run(mode, func(t *testing.T) {
			store := secretStore{Root: t.TempDir()}
			key := "kubernetes/service-session-original-key-renewal-test"
			e := &environmentCreateExecution{Journal: environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: key, State: "pending", IntentSHA256: hash}}, KubernetesLeases: []deploymentplan.LeaseOwner{{Holder: plan.SHA256() + ":original-owner"}}}}
			j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{plan.Resources[0].ID: "original-job-owner"}, Failed: plan.Resources[0].ID}
			switch mode {
			case "no-lease":
				e.Journal.KubernetesLeases = nil
			case "wrong-intent":
				e.Journal.Entries[0].IntentSHA256 = deploymentplan.Digest("different")
			case "receipt":
				e.Journal.Entries[0].Receipt = json.RawMessage(`{}`)
			case "accepted":
				j.Accepted = true
			case "in-flight":
				j.InFlight = plan.Resources[0].ID
			case "completed-write":
				j.Completed = []string{plan.Resources[0].ID}
			case "missing-owner":
				j.Owners = nil
			case "missing-failure":
				j.Failed = ""
			}
			dir := filepath.Join(store.Root, "deployment/rollback", plan.SHA256())
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{"plan.json": plan, "journal.json": j} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			found, _, err := originalPendingServicePlan(store, e, key, 1)
			if mode == "valid" {
				if err != nil || found == nil || found.SHA256() != plan.SHA256() {
					t.Fatal("bound failed child rejected", err)
				}
			} else if err == nil {
				t.Fatal("uncertain or unbound recovery accepted", mode)
			}
		})
	}
}

func TestOriginalKeyBootstrapRenewalPreservesCustodyAndImage(t *testing.T) {
	parent, child := strings.Repeat("a", 64), strings.Repeat("b", 64)
	old := "11111111-1111-4111-8111-111111111111"
	session := sessionRenewalUUID(parent, child, old)
	if !environmentBootstrapOwnerUUID.MatchString(session) || session == old || session != sessionRenewalUUID(parent, child, old) || session == sessionRenewalUUID(parent, strings.Repeat("c", 64), old) {
		t.Fatal("renewal identity is not bound to its one reviewed checkpoint")
	}
	image := "ghcr.io/example/native@sha256:" + strings.Repeat("c", 64)
	container := deploymentplan.Object{"name": "bootstrap", "image": image, "command": []any{"/app/serviceidentity-bootstrap"}, "args": []any{"bootstrap"}, "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt", "PKI_BOOTSTRAP_SESSION_ID": old})}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "old", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}, "initContainers": []any{deploymentplan.Object{"name": "permissions"}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-state"}}}}}}}
	before := deploymentplan.Digest(job)
	d, err := originalKeyRenewalDefinition(job, parent, child, session)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(d.Object)
	initializer := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
	command := certIssuerObjectList(initializer["command"])
	if len(command) != 3 || !strings.Contains(deploymentplan.String(command[2]), "chmod 0700 /run/pki-bootstrap") || !strings.Contains(deploymentplan.String(command[2]), "bootstrap.crt bootstrap.lock;") || !strings.Contains(deploymentplan.String(command[2]), "chmod 0600 /run/pki-bootstrap/$f") || !strings.Contains(deploymentplan.String(command[2]), "mkdir -p /run/pki-bootstrap/renew-") || strings.Contains(deploymentplan.String(command[2]), "cp ") || strings.Contains(deploymentplan.String(command[2]), "rm ") {
		t.Fatal("renewal must correct remounted parent directory permissions without rewriting original key or CSR")
	}
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if deploymentplan.Digest(job) != before || c["image"] != image || mqttFoundationEnv(c, "PKI_BOOTSTRAP_KEY_PATH") != "/run/pki-bootstrap/bootstrap.key" || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CSR_PATH") != "/run/pki-bootstrap/bootstrap.csr" || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CERT_PATH") == "/run/pki-bootstrap/bootstrap.crt" || deploymentplan.Digest(pod["volumes"]) != deploymentplan.Digest(environmentPKIPod(job)["volumes"]) {
		t.Fatal("renewal replaced original custody, image or certificate")
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/different/key"}))
	if _, err := originalKeyRenewalDefinition(job, parent, child, session); err == nil {
		t.Fatal("unverified original key path accepted")
	}
}
