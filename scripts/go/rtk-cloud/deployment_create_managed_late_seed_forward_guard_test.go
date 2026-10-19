package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateSeedForwardFixture(t *testing.T) *createManagedLateSeedForward {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated-original-transport-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, root, root, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	stack, ns := "video-cloud-dev", "video-cloud-dev-video-cloud"
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-bootstrap-renew-5c2183838db1", "namespace": ns}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"rtk.cloud/pki-consumer": "true"}}, "spec": deploymentplan.Object{
		"restartPolicy": "Never", "serviceAccountName": "pki-service-bootstrap", "securityContext": deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001)},
		"volumes":        []any{deploymentplan.Object{"name": "openbao-ca", "configMap": deploymentplan.Object{"name": "pki-openbao-transport-ca", "defaultMode": float64(384)}}, deploymentplan.Object{"name": "bootstrap-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "pki-bootstrap-state"}}},
		"containers":     []any{deploymentplan.Object{"name": "bootstrap", "image": "ghcr.io/rtk-cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), "command": []any{"/app/serviceidentity-bootstrap", "deployment-bootstrap"}, "env": environmentCreateEnv(map[string]string{"OPENBAO_ADDR": "https://openbao.video-cloud-dev-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt", "OPENBAO_KUBERNETES_ROLE": "pki-service-bootstrap-dev", "PKI_BOOTSTRAP_SESSION_ID": "a849212c-47ee-4b29-8f98-bb281fb45bb4", "PKI_BOOTSTRAP_DEPLOYMENT_ID": "create-742be9e4e0311ac4842b-r5c2183838db1", "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-5c2183838db1/bootstrap.crt", "PKI_BOOTSTRAP_TTL": "8h"}), "volumeMounts": []any{deploymentplan.Object{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}, deploymentplan.Object{"name": "bootstrap-state", "mountPath": "/run/pki-bootstrap", "readOnly": false}}}},
		"initContainers": []any{deploymentplan.Object{"name": "permissions", "image": "ghcr.io/rtk-cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64), "command": []any{"/bin/sh", "-ec", "existing original key/CSR permission-only init"}}},
	}}}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("isolatedconfig"), ReleaseSHA256: deploymentplan.Digest("originalrelease"), BindingsSHA256: deploymentplan.Digest("originalseed")}
	plan, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Phase: "bootstrap", Object: job}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &createManagedLateSeedForward{Schema: managedLateSeedForwardSchema, OriginalReview: deploymentplan.Digest("originalqualifiedreview"), SourceSHA: deploymentplan.Digest("actual362"), SourceOwnerSHA: deploymentplan.Digest("originalheldowner"), OriginalArtifactSHA: deploymentplan.Digest("original6GOartifact"), OriginalSource: environmentCreateJournal{Environment: "dev", Stack: stack, PlanSHA256: deploymentplan.Digest("originalparent742")}, FailedPlan: plan, FailedPlanSHA: plan.SHA256(), FailedJobUID: "a857ab21-eb0c-48d4-a423-37e18df08f91", FailedPodUID: "00c369bb-2040-4d52-9d04-dc324f2ac1dd"}
	f.FailedJournal = deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: plan.Resources[0].ID, Owners: map[string]string{plan.Resources[0].ID: f.FailedJobUID}}
	f.FailedJournalSHA = deploymentplan.Digest(f.FailedJournal)
	f.TrustOwner = deploymentplan.Resource{ID: "ConfigMap/" + ns + "/pki-create-openbao-tls-consumer-trust", UID: "4543596a-cf3b-4ac8-ae15-931c2ee7a106", Desired: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-openbao-tls-consumer-trust"}, "data": deploymentplan.Object{"roots.pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}}}
	return f
}

func managedLateSeedForwardFixtureCandidate(t *testing.T, f *createManagedLateSeedForward) *deploymentplan.Plan {
	t.Helper()
	defs, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		t.Fatal(err)
	}
	in := f.FailedPlan.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-seed-trust-forward", defs)
	p, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManagedLateSeedForwardOnlyRemapsPublicCAProjection(t *testing.T) {
	f := managedLateSeedForwardFixture(t)
	before := deploymentplan.Digest(f.FailedPlan)
	p := managedLateSeedForwardFixtureCandidate(t, f)
	if err := validateManagedLateSeedForwardCandidate(f, p); err != nil {
		t.Fatal(err)
	}
	got := deploymentplan.Clone(p.Resources[0].Desired)
	deploymentplan.Map(got["metadata"])["name"] = deploymentplan.Map(f.FailedPlan.Resources[0].Desired["metadata"])["name"]
	volumes, _ := environmentPKINamedFields(environmentPKIPod(got)["volumes"])
	original, _ := environmentPKINamedFields(environmentPKIPod(f.FailedPlan.Resources[0].Desired)["volumes"])
	projection := deploymentplan.Map(volumes["openbao-ca"]["configMap"])
	if deploymentplan.Digest(projection) != deploymentplan.Digest(deploymentplan.Object{"name": "pki-create-openbao-tls-consumer-trust", "defaultMode": float64(384), "items": []any{deploymentplan.Object{"key": "roots.pem", "path": "ca.crt"}}}) {
		t.Fatal("wrong public projection")
	}
	volumes["openbao-ca"]["configMap"] = original["openbao-ca"]["configMap"]
	if deploymentplan.Digest(got) != deploymentplan.Digest(f.FailedPlan.Resources[0].Desired) || before != deploymentplan.Digest(f.FailedPlan) {
		t.Fatal("changed original seed identity or recipe")
	}
	for _, mode := range []string{"sid", "image", "key", "csr", "cert", "role", "ttl", "claim", "init", "env-cacert", "name", "second-resource", "uid", "baseline", "bindings", "release"} {
		t.Run(mode, func(t *testing.T) {
			var changed deploymentplan.Plan
			raw, _ := json.Marshal(p)
			_ = json.Unmarshal(raw, &changed)
			pod := environmentPKIPod(changed.Resources[0].Desired)
			c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			fields := map[string]string{"sid": "PKI_BOOTSTRAP_SESSION_ID", "key": "PKI_BOOTSTRAP_KEY_PATH", "csr": "PKI_BOOTSTRAP_CSR_PATH", "cert": "PKI_BOOTSTRAP_CERT_PATH", "role": "OPENBAO_KUBERNETES_ROLE", "ttl": "PKI_BOOTSTRAP_TTL", "env-cacert": "OPENBAO_CACERT"}
			if field := fields[mode]; field != "" {
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{field: "different"}))
			}
			switch mode {
			case "image":
				c["image"] = "different"
			case "claim":
				v, _ := environmentPKINamedFields(pod["volumes"])
				deploymentplan.Map(v["bootstrap-state"]["persistentVolumeClaim"])["claimName"] = "newstate"
			case "init":
				deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])["command"] = []any{"newkey"}
			case "name":
				deploymentplan.Map(changed.Resources[0].Desired["metadata"])["name"] = "unbound"
			case "second-resource":
				changed.Resources = append(changed.Resources, changed.Resources[0])
			case "uid":
				changed.Resources[0].UID = f.FailedJobUID
			case "baseline":
				changed.Resources[0].Baseline = deploymentplan.Clone(f.FailedPlan.Resources[0].Desired)
			case "bindings":
				changed.BindingsSHA256 = deploymentplan.Digest("foreign")
			case "release":
				changed.ReleaseSHA256 = deploymentplan.Digest("newrelease")
			}
			if validateManagedLateSeedForwardCandidate(f, &changed) == nil {
				t.Fatal("accepted extra mutation", mode)
			}
		})
	}
}

func TestManagedLateSeedForwardRequiresExactFailedHistory(t *testing.T) {
	for _, mode := range []string{"accepted", "acceptance-started", "inflight", "completed", "missing-owner", "foreign-owner", "foreign-pod", "journalhash", "planhash", "update", "baseline", "extra", "parent"} {
		t.Run(mode, func(t *testing.T) {
			f := managedLateSeedForwardFixture(t)
			switch mode {
			case "accepted":
				f.FailedJournal.Accepted = true
			case "acceptance-started":
				f.FailedJournal.AcceptanceStarted = true
			case "inflight":
				f.FailedJournal.InFlight = f.FailedPlan.Resources[0].ID
			case "completed":
				f.FailedJournal.Completed = []string{f.FailedPlan.Resources[0].ID}
			case "missing-owner":
				f.FailedJournal.Owners = nil
			case "foreign-owner":
				f.FailedJournal.Owners[f.FailedPlan.Resources[0].ID] = "different"
			case "foreign-pod":
				f.FailedPodUID = "invalid"
			case "journalhash":
				f.FailedJournalSHA = deploymentplan.Digest("different")
			case "planhash":
				f.FailedPlanSHA = deploymentplan.Digest("different")
			case "update":
				f.FailedPlan.Resources[0].Action = "update"
			case "baseline":
				f.FailedPlan.Resources[0].Baseline = deploymentplan.Object{}
			case "extra":
				f.FailedPlan.Resources = append(f.FailedPlan.Resources, f.FailedPlan.Resources[0])
			case "parent":
				f.OriginalSource.Environment = "staging"
			}
			if mode != "planhash" {
				f.FailedPlanSHA = f.FailedPlan.SHA256()
				f.FailedJournal.PlanSHA256 = f.FailedPlanSHA
			}
			if mode != "journalhash" {
				f.FailedJournalSHA = deploymentplan.Digest(f.FailedJournal)
			}
			if validateManagedLateSeedForwardFailedChild(f) == nil {
				t.Fatal("accepted unsafe history", mode)
			}
		})
	}
}

func managedLateSeedForwardTerminalFixture(t *testing.T) (*createManagedLateSeedForward, deploymentplan.Object, deploymentplan.Object, []byte) {
	f := managedLateSeedForwardFixture(t)
	job := deploymentplan.Clone(f.FailedPlan.Resources[0].Desired)
	deploymentplan.Map(job["metadata"])["uid"] = f.FailedJobUID
	job["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}
	pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "original-failed-pod", "namespace": f.FailedPlan.Stack + "-video-cloud", "uid": f.FailedPodUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Job", "name": deploymentplan.Map(job["metadata"])["name"], "uid": f.FailedJobUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(job)), "status": deploymentplan.Object{"phase": "Failed", "initContainerStatuses": []any{deploymentplan.Object{"name": "permissions", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed"}}}}, "containerStatuses": []any{deploymentplan.Object{"name": "bootstrap", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(1), "reason": "Error"}}}}}}
	logs := []byte("bootstrap OpenBao client: openbao Kubernetes login failed: Post \"https://openbao.video-cloud-dev-secrets.svc:8200/v1/auth/kubernetes/login\": tls: failed to verify certificate: x509: certificate signed by unknown authority\n")
	return f, job, pod, logs
}

func TestManagedLateSeedForwardTerminalRequiresSoleOwnedExitedTLSFailure(t *testing.T) {
	f, job, pod, logs := managedLateSeedForwardTerminalFixture(t)
	if err := validateManagedLateSeedForwardTerminal(f, job, pod, logs); err != nil {
		t.Fatal(err)
	}
	// Real API omission of readOnly=false does not weaken any identity check.
	c := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])
	m, _ := environmentPKINamedFields(c["volumeMounts"])
	delete(m["bootstrap-state"], "readOnly")
	if err := validateManagedLateSeedForwardTerminal(f, job, pod, logs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"jobuid", "poduid", "foreign-owner", "no-controller", "active", "succeeded", "nonterminal", "running", "waiting", "initfail", "restarted", "different-image", "sidecar", "signing-error", "authorization-error", "extra-log", "missing-prefix", "badURL"} {
		t.Run(mode, func(t *testing.T) {
			f, j, p, l := managedLateSeedForwardTerminalFixture(t)
			js := deploymentplan.Map(j["status"])
			ps := deploymentplan.Map(p["status"])
			st := deploymentplan.Map(certIssuerObjectList(ps["containerStatuses"])[0])
			switch mode {
			case "jobuid":
				deploymentplan.Map(j["metadata"])["uid"] = "different"
			case "poduid":
				deploymentplan.Map(p["metadata"])["uid"] = "different"
			case "foreign-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "different"
			case "no-controller":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["controller"] = false
			case "active":
				js["active"] = float64(1)
			case "succeeded":
				js["succeeded"] = float64(1)
			case "nonterminal":
				js["conditions"] = nil
			case "running":
				ps["phase"] = "Running"
			case "waiting":
				st["state"] = deploymentplan.Object{"waiting": deploymentplan.Object{"reason": "CrashLoopBackOff"}}
			case "initfail":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(ps["initContainerStatuses"])[0])["state"])["terminated"])["exitCode"] = float64(1)
			case "restarted":
				st["restartCount"] = float64(1)
			case "different-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["spec"])["containers"])[0])["image"] = "different"
			case "sidecar":
				deploymentplan.Map(p["spec"])["containers"] = append(certIssuerObjectList(deploymentplan.Map(p["spec"])["containers"]), deploymentplan.Object{"name": "runningforeign"})
			case "signing-error":
				l = []byte("signing outcome unknown")
			case "authorization-error":
				l = []byte(strings.ReplaceAll(string(l), "tls: failed to verify certificate: x509: certificate signed by unknown authority", "pki authorization denied"))
			case "extra-log":
				l = append(l, []byte("minted\n")...)
			case "missing-prefix":
				l = []byte(strings.TrimPrefix(string(l), "bootstrap OpenBao client: "))
			case "badURL":
				l = []byte(strings.ReplaceAll(string(l), "svc:8200", "example.com:8200"))
			}
			if validateManagedLateSeedForwardTerminal(f, j, p, l) == nil {
				t.Fatal("accepted unsafe terminal source", mode)
			}
		})
	}
}

func TestManagedLateSeedForwardNoMintRequiresEveryUnfilteredZero(t *testing.T) {
	query, err := managedLateSeedForwardNoMintQuery("dev", "a849212c-47ee-4b29-8f98-bb281fb45bb4", "create-742be9e4e0311ac4842b-r5c2183838db1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "status") || strings.Contains(query, "caller") || strings.Count(query, "count(*)") != 3 || !strings.Contains(query, "request_id='deployment-bootstrap:create-742be9e4e0311ac4842b-r5c2183838db1'") {
		t.Fatal("query hides signing progress")
	}
	if !managedLateSeedForwardNoMintCountsValid([]byte(`{"sessions":0,"deployments":0,"issuances":0}`)) {
		t.Fatal("genuine zero snapshot rejected")
	}
	for _, raw := range []string{`{}`, `{"sessions":0,"deployments":0}`, `{"sessions":null,"deployments":0,"issuances":0}`, `{"sessions":1,"deployments":0,"issuances":0}`, `{"sessions":0,"deployments":1,"issuances":0}`, `{"sessions":0,"deployments":0,"issuances":1}`, `{"sessions":0,"deployments":0,"issuances":-1}`, `{"sessions":0,"deployments":0,"issuances":0,"foreign":0}`} {
		if managedLateSeedForwardNoMintCountsValid([]byte(raw)) {
			t.Fatal("accepted missing/nonzero native proof", raw)
		}
	}
	if _, err := managedLateSeedForwardNoMintQuery("dev", "a849212c-47ee-4b29-8f98-bb281fb45bb4", "unsafe' OR true"); err == nil {
		t.Fatal("unbound SQL identifier")
	}
}

func TestManagedLateSeedForwardTrustPinsOnlyGenuineCurrentRoot(t *testing.T) {
	f := managedLateSeedForwardFixture(t)
	value := deploymentplan.String(deploymentplan.Map(f.TrustOwner.Desired["data"])["roots.pem"])
	block, _ := pem.Decode([]byte(value))
	root := environmentPreflightBytesSHA256(block.Bytes)
	if !managedLateSeedForwardRoots(value, root, time.Now().UTC()) {
		t.Fatal("original current CA rejected")
	}
	for _, value := range []string{"", "unverified material\n" + value, value + "private material", value + value, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block.Bytes}))} {
		if managedLateSeedForwardRoots(value, root, time.Now().UTC()) {
			t.Fatal("extra/invalid identity trust accepted")
		}
	}
	if managedLateSeedForwardRoots(value, strings.Repeat("f", 64), time.Now().UTC()) || managedLateSeedForwardRoots(value, root, time.Now().UTC().Add(2*time.Hour)) {
		t.Fatal("foreign or expired root accepted")
	}
	defs, _ := managedLateSeedForwardDefinitions(f)
	before := slices.Clone(certIssuerObjectList(environmentPKIPod(f.FailedPlan.Resources[0].Desired)["volumes"]))
	if len(before) != len(certIssuerObjectList(environmentPKIPod(defs[0].Object)["volumes"])) {
		t.Fatal("changed native volume inventory")
	}
}

func managedLateSeedForwardCustodyFixture(t *testing.T) (deploymentplan.Object, *createManagedLateSessionRecovery) {
	t.Helper()
	f := managedLateSeedForwardFixture(t)
	ns, name := f.FailedPlan.Stack+"-video-cloud", "pki-bootstrap-custody-original"
	reader := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"securityContext": deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001)}, "containers": []any{deploymentplan.Object{"name": "custody", "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": "/run/pki-bootstrap"}}}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "pki-bootstrap-state"}}}}}}}
	late := &createManagedLateSessionRecovery{SourceCeremony: environmentCreateServiceCeremony{BootstrapCredentialNamespace: ns, CustodyReader: name, BootstrapClaim: "pki-bootstrap-state"}, Custody: deploymentplan.Resource{ID: deploymentplan.ID(reader), UID: "7a2e9865-b34c-447f-b50f-8a554f3742f3", Desired: reader}, Renewal: &createManagedSessionRecovery{OriginalCertificatePath: "/run/pki-bootstrap/renew-bb424558f951/bootstrap.crt"}, NewCertificatePath: "/run/pki-bootstrap/renew-5c2183838db1/bootstrap.crt"}
	return deploymentplan.Clone(f.FailedPlan.Resources[0].Desired), late
}

func TestManagedLateSeedForwardPublicCertificatePermissionsRequireOriginalSignerAndPVC(t *testing.T) {
	for _, mode := range []string{"valid reader without fsGroup", "missing signer fsGroup", "foreign signer fsGroup", "foreign signer user", "foreign signer group", "different Job claim", "different reader claim", "Job subPath", "reader subPath", "alternate reader mount", "wrong reader UID", "wrong reader ID", "unsafe certificate path"} {
		t.Run(mode, func(t *testing.T) {
			seed, late := managedLateSeedForwardCustodyFixture(t)
			security := deploymentplan.Map(environmentPKIPod(seed)["securityContext"])
			jm, _ := environmentPKINamedFields(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(seed)["containers"])[0])["volumeMounts"])
			rm, _ := environmentPKINamedFields(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(late.Custody.Desired)["containers"])[0])["volumeMounts"])
			switch mode {
			case "missing signer fsGroup":
				delete(security, "fsGroup")
			case "foreign signer fsGroup":
				security["fsGroup"] = float64(10002)
			case "foreign signer user":
				security["runAsUser"] = float64(10002)
			case "foreign signer group":
				security["runAsGroup"] = float64(10002)
			case "different Job claim":
				v, _ := environmentPKINamedFields(environmentPKIPod(seed)["volumes"])
				deploymentplan.Map(v["bootstrap-state"]["persistentVolumeClaim"])["claimName"] = "different"
			case "different reader claim":
				v, _ := environmentPKINamedFields(environmentPKIPod(late.Custody.Desired)["volumes"])
				deploymentplan.Map(v["state"]["persistentVolumeClaim"])["claimName"] = "different"
			case "Job subPath":
				jm["bootstrap-state"]["subPath"] = "alternate"
			case "reader subPath":
				rm["state"]["subPath"] = "alternate"
			case "alternate reader mount":
				rm["state"]["mountPath"] = "/different"
			case "wrong reader UID":
				late.Custody.UID = "missing"
			case "wrong reader ID":
				late.Custody.ID = "Deployment/other/reader"
			case "unsafe certificate path":
				late.NewCertificatePath = "/run/pki-bootstrap/../outside.crt"
			}
			before := deploymentplan.Digest(late.Custody)
			script, err := managedLateSeedForwardCustodyCommand(seed, late)
			if (err == nil) != (mode == "valid reader without fsGroup") {
				t.Fatalf("wrong source binding result for %s", mode)
			}
			if err == nil && (strings.Contains(script, "chmod") || strings.Contains(script, "chown") || deploymentplan.Digest(late.Custody) != before || deploymentplan.Map(environmentPKIPod(late.Custody.Desired)["securityContext"])["fsGroup"] != nil) {
				t.Fatal("native permission/reader recipe mutated")
			}
		})
	}
}

func TestManagedLateSeedForwardNativePublicCertificateModesDoNotWidenPrivateFiles(t *testing.T) {
	for _, mode := range []string{"original 600", "observed public 660", "key 660", "CSR 660", "public 640", "public 666", "public executable", "foreign public user", "foreign public group", "private root group access", "private root world access", "private root setuid", "private root sticky", "foreign root user", "foreign root group", "key symlink", "CSR symlink", "public symlink", "root symlink", "old directory symlink", "new directory symlink", "new certificate exists", "new certificate dangling symlink"} {
		t.Run(mode, func(t *testing.T) {
			seed, late := managedLateSeedForwardCustodyFixture(t)
			script, err := managedLateSeedForwardCustodyCommand(seed, late)
			if err != nil {
				t.Fatal(err)
			}
			fixture := t.TempDir()
			root := filepath.Join(fixture, "bootstrap")
			oldDir := filepath.Join(root, "renew-bb424558f951")
			newDir := filepath.Join(root, "renew-5c2183838db1")
			for _, dir := range []string{oldDir, newDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			key, csr, cert := filepath.Join(root, "bootstrap.key"), filepath.Join(root, "bootstrap.csr"), filepath.Join(oldDir, "bootstrap.crt")
			for _, path := range []string{key, csr, cert} {
				if err := os.WriteFile(path, []byte("unchanged original fixture bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rootTuple, keyTuple, csrTuple, certTuple := "2700 10001 10001", "600 10001 10001", "600 10001 10001", "660 10001 10001"
			switch mode {
			case "original 600":
				rootTuple, certTuple = "700 10001 10001", "600 10001 10001"
			case "key 660":
				keyTuple = "660 10001 10001"
			case "CSR 660":
				csrTuple = "660 10001 10001"
			case "public 640":
				certTuple = "640 10001 10001"
			case "public 666":
				certTuple = "666 10001 10001"
			case "public executable":
				certTuple = "760 10001 10001"
			case "foreign public user":
				certTuple = "660 10002 10001"
			case "foreign public group":
				certTuple = "660 10001 10002"
			case "private root group access":
				rootTuple = "2770 10001 10001"
			case "private root world access":
				rootTuple = "2701 10001 10001"
			case "private root setuid":
				rootTuple = "4700 10001 10001"
			case "private root sticky":
				rootTuple = "1700 10001 10001"
			case "foreign root user":
				rootTuple = "2700 10002 10001"
			case "foreign root group":
				rootTuple = "2700 10001 10002"
			case "key symlink", "CSR symlink", "public symlink", "root symlink", "old directory symlink", "new directory symlink":
				path := map[string]string{"key symlink": key, "CSR symlink": csr, "public symlink": cert, "root symlink": root, "old directory symlink": oldDir, "new directory symlink": newDir}[mode]
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-original", path); err != nil {
					t.Fatal(err)
				}
			case "new certificate exists":
				if err := os.WriteFile(filepath.Join(newDir, "bootstrap.crt"), []byte("already minted"), 0600); err != nil {
					t.Fatal(err)
				}
			case "new certificate dangling symlink":
				if err := os.Symlink(filepath.Join(newDir, "missing"), filepath.Join(newDir, "bootstrap.crt")); err != nil {
					t.Fatal(err)
				}
			}
			stat := filepath.Join(fixture, "stat")
			shim := `#!/bin/sh
set -eu
[ "$1" = -c ] && [ "$2" = '%a %u %g' ]
shift 2
for file in "$@"; do
 case "$file" in
  "$RTK_SEED_ROOT") printf '%s\n' "$RTK_SEED_ROOT_TUPLE" ;;
  "$RTK_SEED_ROOT/bootstrap.key") printf '%s\n' "$RTK_SEED_KEY_TUPLE" ;;
  "$RTK_SEED_ROOT/bootstrap.csr") printf '%s\n' "$RTK_SEED_CSR_TUPLE" ;;
  "$RTK_SEED_ROOT/renew-bb424558f951/bootstrap.crt") printf '%s\n' "$RTK_SEED_CERT_TUPLE" ;;
  *) exit 71 ;;
 esac
done
`
			if err := os.WriteFile(stat, []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("/bin/sh", "-ec", strings.ReplaceAll(script, "/run/pki-bootstrap", root))
			command.Env = append(os.Environ(), "PATH="+fixture+string(os.PathListSeparator)+os.Getenv("PATH"), "RTK_SEED_ROOT="+root, "RTK_SEED_ROOT_TUPLE="+rootTuple, "RTK_SEED_KEY_TUPLE="+keyTuple, "RTK_SEED_CSR_TUPLE="+csrTuple, "RTK_SEED_CERT_TUPLE="+certTuple)
			// Run actual production /bin/sh checks on real isolated paths. Only
			// Linux numeric mode/UID/GID are supplied by the portable stat shim.
			raw, err := command.Output()
			if err == nil {
				err = validateManagedLateSeedForwardCustodyPermissions(raw)
			}
			if (err == nil) != (mode == "original 600" || mode == "observed public 660") {
				t.Fatalf("wrong native permission/absence result for %s", mode)
			}
			for _, path := range []string{key, csr, cert} {
				if b, err := os.ReadFile(path); err != nil || string(b) != "unchanged original fixture bytes" {
					t.Fatal("native guard modified original material")
				}
			}
		})
	}
}
