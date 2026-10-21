package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func (f *acceptedCoreExecutionContract) installFormalRepairPublication(t *testing.T) environmentReleaseImage {
	t.Helper()
	repo := "hkt999rtk/rtk_video_cloud"
	revision := f.review[:40]
	imageManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"layers":[]}`
	digest := "sha256:" + environmentPreflightBytesSHA256([]byte(imageManifest))
	manifest := mustProviderRecoveryJSON(map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": revision, "run_id": "77", "run_attempt": "2", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": digest})
	image, err := selectEnvironmentPublishedImage(manifest, repo, 77, 2, revision, "ghcr.io/"+repo+"/video-cloud-api", false)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "deployment/repairs/service-bootstrap/selection.json", map[string]any{"schema": "rtk-cloud.create-service-trust-repair-selection/v1", "parent_plan_sha256": f.e.Journal.PlanSHA256, "source_revision": revision, "run_id": 77, "run_attempt": 2, "manifest_sha256": environmentPreflightBytesSHA256(manifest)})
	if err := f.store.write(filepath.Join("deployment/repairs/service-bootstrap", revision, "image-manifest.json"), manifest, false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.write("operator/env/GHCR_PULL_TOKEN", []byte("isolated-fake-gh-only"), true); err != nil {
		t.Fatal(err)
	}
	if f.protocol["image_manifests"] == nil {
		f.protocol["image_manifests"] = map[string]string{}
	}
	f.protocol["image_manifests"].(map[string]string)[image.Reference] = imageManifest
	if err := f.store.write("operator/env/GHCR_PULL_USERNAME", []byte("isolated-registry-user"), true); err != nil {
		t.Fatal(err)
	}
	archive := publicationZIP(t, "image-manifest.json", manifest)
	f.protocol["ci_run"] = map[string]any{"id": 77, "head_sha": revision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": 2}
	f.protocol["ci_listing"] = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 99, "name": "image-manifest", "expired": false, "size_in_bytes": len(archive)}}}
	f.protocol["ci_archive"] = base64.StdEncoding.EncodeToString(archive)
	f.publish(t)
	script := `#!/usr/bin/env python3
import sys,os,json,base64
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as f:d=json.load(f)
requests={'repos/hkt999rtk/rtk_video_cloud/actions/runs/77':'ci_run','repos/hkt999rtk/rtk_video_cloud/actions/runs/77/artifacts?per_page=100':'ci_listing','repos/hkt999rtk/rtk_video_cloud/actions/artifacts/99/zip':'ci_archive'}
a=sys.argv[1:]
if len(a)!=2 or a[0]!='api' or a[1] not in requests or os.environ.get('GH_TOKEN')!='isolated-fake-gh-only':sys.exit('unexpected isolated formal publication request')
k=requests[a[1]]
if k=='ci_archive':sys.stdout.buffer.write(base64.b64decode(d[k]))
else:print(json.dumps(d[k]))
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return image
}

func newApplicationTrustRecoveryExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *createServiceSessionRecovery, environmentReleaseImage) {
	t.Helper()
	f := newAcceptedCoreExecutionContract(t)
	grants, err := renderEnvironmentCreatePKIPhase(context.Background(), f.cfg, f.p.Plan.Bootstrap, "grants")
	if err != nil {
		t.Fatal(err)
	}
	f.stage(t, "kubernetes/pki-grants", grants)
	defs, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for _, d := range defs {
		if strings.HasPrefix(deploymentplan.ID(d.Object), "Job/"+f.p.Plan.Stack+"-video-cloud/pki-enroll-factoryenroll-client-") {
			failed = deploymentplan.ID(d.Object)
		}
	}
	p, j := f.interruptedStage(t, "kubernetes/application-client-enrollment", defs, failed)
	if !knownApplicationEnrollmentTrustFailure(p, j, f.p.Plan.Stack) {
		t.Fatal("application fixture has no original known trust refusal")
	}
	r := &createServiceSessionRecovery{PartialCore: p, PartialCoreJournal: j, Ceremony: f.service, SourceRenewalStage: "kubernetes/service-session-original-key-renewal-" + f.review}
	image := f.installFormalRepairPublication(t)
	objects := coreContractNativeInventory(f.native.objects)
	objects[failed]["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}
	f.protocol["objects"] = objects
	name := deploymentplan.String(deploymentplan.Map(objects[failed]["metadata"])["name"])
	f.protocol["logs"] = map[string]string{"job/" + name: "Service client enrollment refused: Post \"https://certissuer." + f.p.Plan.Stack + "-video-cloud.svc:18445/v1/enroll\": remote error: tls: bad certificate"}
	f.protocol["issued"] = []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
	f.publish(t)
	return f, r, image
}

func TestApplicationTrustRecoveryExecutionContractPreparesOnlySameKeyRenewalAndNativeGrant(t *testing.T) {
	f, r, image := newApplicationTrustRecoveryExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	if err := prepareApplicationTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal("genuine original TLS refusal and formal repair rejected", err)
	}
	if r.Candidate == nil || len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" || r.GrantCandidate == nil || len(r.GrantCandidate.Resources) != 1 || r.GrantCandidate.Resources[0].Action != "create" || r.GrantImage.Reference != image.Reference {
		t.Fatal("repair created unrelated work or selected a different formal publication")
	}
	env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(r.Candidate.Resources[0].Desired)["containers"])[0]))
	if env["PKI_BOOTSTRAP_KEY_PATH"] != "/run/pki-bootstrap/bootstrap.key" || env["PKI_BOOTSTRAP_CSR_PATH"] != "/run/pki-bootstrap/bootstrap.csr" || env["PKI_BOOTSTRAP_TTL"] != "8h" || env["PKI_BOOTSTRAP_EXPIRES_AT"] != "" || env["PKI_BOOTSTRAP_SESSION_ID"] != sessionRenewalUUID(f.e.Journal.PlanSHA256, r.PartialCore.SHA256(), f.session) {
		t.Fatal("repair replaced original key/CSR custody or changed its bounded new session")
	}
	if p.Materials["accepted-service-core-application-grant-plan"] != r.GrantCandidate.SHA256() || p.Materials["accepted-service-core-application-renewal-plan"] != r.Candidate.SHA256() {
		t.Fatal("qualified candidates were not bound")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	// A second qualification must fetch the current native inventory. A Job
	// created after the first read cannot be quietly reused as an unrun repair.
	resource := r.Candidate.Resources[0]
	observed := deploymentplan.Clone(resource.Desired)
	deploymentplan.Map(observed["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	f.protocol["objects"].(map[string]deploymentplan.Object)[resource.ID] = observed
	f.publish(t)
	if err := prepareApplicationTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err == nil || !strings.Contains(err.Error(), "renewal already exists") {
		t.Fatal("fresh native candidate collision was not rejected", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestApplicationTrustRecoveryExecutionContractRejectsChangedNativeRefusalAndPublication(t *testing.T) {
	for _, mutation := range []string{"not-tls-refusal", "unknown-issued-subject", "publication-rerun", "expired-artifact"} {
		t.Run(mutation, func(t *testing.T) {
			f, r, _ := newApplicationTrustRecoveryExecutionContract(t)
			switch mutation {
			case "not-tls-refusal":
				for k := range f.protocol["logs"].(map[string]string) {
					f.protocol["logs"].(map[string]string)[k] = "database temporarily unavailable"
				}
			case "unknown-issued-subject":
				f.protocol["issued"] = []string{"service:account-manager", "service:certissuer", "service:factory-enroll", "service:pki-controller"}
			case "publication-rerun":
				f.protocol["ci_run"].(map[string]any)["run_attempt"] = 3
			case "expired-artifact":
				f.protocol["ci_listing"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)["expired"] = true
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareApplicationTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err == nil {
				t.Fatal("changed native or formal provenance accepted", mutation)
			}
			if r.Candidate != nil || r.GrantCandidate != nil {
				t.Fatal("refused source produced executable repair candidates")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
