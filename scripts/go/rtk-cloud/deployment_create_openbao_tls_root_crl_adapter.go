// Renew the original OpenBao TLS Root CRL through its offline custody.
// Qualification checks the same issuer, live revocations, signed request and
// native consumers. Execution preserves their owners and reconciles known
// publication outcomes through the original journal and lease.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createOpenBaoTLSRootCRLSchema = "rtk-cloud.create-openbao-tls-root-crl-recovery/v1"

var createOpenBaoTLSRootCRLGates = []string{"deployment.openbao-tls-root-crl.original-authority", "deployment.openbao-tls-root-crl.live-revocations", "deployment.openbao-tls-root-crl.native-signature", "deployment.openbao-tls-root-crl.native-dry-run", "deployment.openbao-tls-root-crl.original-consumers", "deployment.openbao-tls-root-crl.execution-lease"}

type createOpenBaoTLSRootCRLPlan struct {
	Schema, Parent, SourceOwnerSHA, SourceJournalSHA   string
	CheckerSHA, ConfigurationSHA, KubeconfigSHA, Image string
	Configuration                                      deploymentConfig
	Generation                                         environmentCreatePlan
	SourceArtifactSHA, SourceCoreSHA, SourceAppsSHA    string
	Root                                               environmentCreateRegisteredIssuer
	Intermediate                                       environmentCreateRegisteredIssuer
	IntermediateCRL                                    createRootCRLRecord
	ConsumerVolumes                                    map[string]environmentCreateVolumeOwner
	Snapshot                                           createRootCRLNativeSnapshot
	SnapshotSHA                                        string
	Request                                            createRootCRLRequest
	Signed                                             createRootCRLRecord
	Candidate                                          string
	Material                                           map[string]string
	ConfigMap, Controller                              deploymentplan.Resource
	ControllerPodName, ControllerPodUID                string
	Consumers                                          []createRootCRLStateTarget
	NativeSourceEvidence                               map[string]string
	NativeDryRunSHA                                    string
	OriginalVolumes                                    map[string]environmentCreateVolumeOwner
}

type createOpenBaoTLSRootCRLArtifact struct {
	Schema                  string
	Plan                    createOpenBaoTLSRootCRLPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}

type createOpenBaoTLSRootCRLReceipt struct {
	Schema, Parent, Review, File, Digest, NativeEvidenceSHA string
	Completed                                               bool
}

type createOpenBaoTLSRootCRLCompletion struct {
	Schema, Parent, Review            string
	Completed                         bool
	SourceEntryCount, FinalEntryCount int
	FinalEntriesSHA, FinalJournalSHA  string
	NativeEvidence                    map[string]string
	Receipt                           createOpenBaoTLSRootCRLReceipt
}

type openBaoTLSRootCRLCandidate struct {
	Schema, Parent, SourceOwnerSHA, SourceJournalSHA, SnapshotSHA string
	Root                                                          environmentCreateRegisteredIssuer
	Snapshot                                                      createRootCRLNativeSnapshot
	Material                                                      map[string]string
	Request                                                       createRootCRLRequest
}

func openBaoTLSRootCRLError(stage string) error {
	return createProviderRecoveryBlock("same original OpenBaoTLS Root CRL requires actual accepted Core, exact live inventory and genuine native two-consumer proof: stage=" + stage)
}
func openBaoTLSRootCRLBase(review string) string {
	return filepath.Join("deployment/create/openbao-tls-root-crl-recovery", review)
}

func runEnvironmentCreateOpenBaoTLSRootCRL(args []string) error {
	return runEnvironmentCreateOpenBaoTLSRootCRLContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateOpenBaoTLSRootCRLContext(parentContext context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-openbao-tls-root-crl", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	workspaceOpt := fs.String("workspace", "", "original logical workspace")
	execute := fs.Bool("execute", false, "execute exact admitted OpenBaoTLS CRL publication")
	review := fs.String("review-sha256", "", "exact scoped review")
	confirm := fs.String("confirm", "", "exact original stack")
	timeout := fs.Duration("timeout", 15*time.Minute, "complete bounded operation")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return openBaoTLSRootCRLError("arguments")
	}
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-openbao-tls-root-crl"}, args...)) != nil {
		return openBaoTLSRootCRLError("mutation-guard")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOpt != "" {
		workspace = *workspaceOpt
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parentContext, *timeout)
	defer cancel()
	profile := make([]bool, 11)
	for i := range profile {
		profile[i] = true
	}
	e, original, _, _, err := loadInitializedCreateAudit(cfg, store, *parent, false, false, false, false, false, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return openBaoTLSRootCRLError("original-operator")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return openBaoTLSRootCRLError("canonical-kubeconfig")
	}
	b, err := newEnvironmentCreateCRLBackend(string(kube), *env, cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	if *execute {
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(*review) {
			return openBaoTLSRootCRLError("confirmation")
		}
		a, source, err := loadCreateOpenBaoTLSRootCRLArtifact(cfg, store, e, *review)
		if err != nil {
			return err
		}
		if a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || a.Plan.Image != original.Plan.Plan.Bootstrap.Image {
			return openBaoTLSRootCRLError("qualified-inputs")
		}
		return executeCreateOpenBaoTLSRootCRL(ctx, cfg, store, e, b, a, source, out)
	}
	p := createFoundationRecoveryPlan{Plan: original.Plan.Plan, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(cfg, store, e, &p, managedLateSessionOpenBaoTLSRepair)
	if err != nil {
		return err
	}
	if r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || late.AcceptedCore == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil {
		return openBaoTLSRootCRLError("actual-late-source")
	}
	// This mode proves every original native client/server/volume and current
	// accepted owner, while authenticating only the signed original OpenBaoTLS prior.
	if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, &p, late, managedLateSessionOpenBaoTLSRepair); err != nil {
		return err
	}
	native := map[string]string{}
	for key, value := range p.Materials {
		native[key] = value
	}
	root := *r.OpenBaoRoot
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil || snapshot.Prior.Number != "1" {
		return openBaoTLSRootCRLError("original-prior-one")
	}
	material, err := createOpenBaoTLSRootCRLMaterial(store, e.Journal.PlanSHA256, root)
	if err != nil {
		return err
	}
	candidate, request, signed, err := prepareOpenBaoTLSRootCRLCandidate(ctx, store, e, p.Plan.Bootstrap.Image, root, snapshot, material)
	if err != nil {
		return err
	}
	controller, cm, targets, podName, podUID, err := loadOpenBaoTLSRootCRLTargets(ctx, cfg, e, b, r, late, root, snapshot.Prior)
	if err != nil {
		return err
	}
	plan := createOpenBaoTLSRootCRLPlan{Schema: createOpenBaoTLSRootCRLSchema, Parent: e.Journal.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: deploymentplan.Digest(e.Journal), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), Configuration: cfg, Generation: original.Plan.Plan, KubeconfigSHA: environmentPreflightBytesSHA256(kube), Image: p.Plan.Bootstrap.Image, SourceArtifactSHA: late.SourceArtifactSHA, SourceCoreSHA: late.AcceptedCore.SHA256(), SourceAppsSHA: r.ManagedCorePartial.Apps.SHA256(), Root: root, Intermediate: *r.OpenBaoIntermediate, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed, Candidate: candidate, Material: material, ConfigMap: cm, Controller: controller, ControllerPodName: podName, ControllerPodUID: podUID, Consumers: targets, NativeSourceEvidence: native}
	plan.IntermediateCRL, err = readOpenBaoTLSRootCRLIntermediate(ctx, cfg, e, b, plan.Root, plan.Intermediate)
	if err != nil {
		return err
	}
	plan.ConsumerVolumes, plan.OriginalVolumes, err = readOpenBaoTLSRootCRLVolumes(e, plan.Consumers, plan.NativeSourceEvidence, late.RestoreReview)
	if err != nil {
		return err
	}
	if !openBaoTLSRootCRLAcceptedTargetsValid(plan, r, late) {
		return openBaoTLSRootCRLError("strict-native-root-ICA-and-private-targets")
	}

	dryRun, err := runNativeCreateRootCRLImport(ctx, cfg, b, openBaoTLSRootCRLNativeImportPlan(plan), false)
	if err != nil || !openBaoTLSRootCRLNativeResultValid(dryRun, plan, false) {
		return openBaoTLSRootCRLError("actual-supported-native-domain-dryrun")
	}
	plan.NativeDryRunSHA = environmentPreflightBytesSHA256(dryRun)
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(candidate, "native-dry-run.json"), map[string]string{"output_sha256": plan.NativeDryRunSHA, "request_sha256": deploymentplan.Digest(plan.Request), "root_sha256": root.RootFingerprint, "domain": "openbao_tls"}); err != nil {
		return err
	}
	// Native dry-run creates no publication/ACK. Recheck the live snapshot and
	// all source/custody bytes after local signing and native dry-run.
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil || deploymentplan.Digest(fresh) != plan.SnapshotSHA {
		return openBaoTLSRootCRLError("post-dryrun-snapshot")
	}
	if err := verifyOpenBaoTLSRootCRLInputs(ctx, cfg, store, e, b, plan, false); err != nil {
		return err
	}
	sha := deploymentplan.Digest(plan)
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, sha)); err != nil {
		return err
	}
	now := time.Now().UTC()
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range createOpenBaoTLSRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
	}
	a := createOpenBaoTLSRootCRLArtifact{Schema: createOpenBaoTLSRootCRLSchema + "/artifact", Plan: plan, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	for name, value := range map[string]any{"source-journal.json": e.Journal, "source-owner.json": e.owner, "qualification.json": a} {
		if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(openBaoTLSRootCRLBase(sha), name), value); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-openbao-tls-root-crl review-sha256=%s crl-number=2 native-consumers=2\n", sha)
	return nil
}

func createOpenBaoTLSRootCRLMaterial(store secretStore, parent string, root environmentCreateRegisteredIssuer) (map[string]string, error) {
	if root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" || root.Domain != "openbao_tls" || root.Environment != store.Environment || root.Kind != "root" || root.Status != "active" {
		return nil, openBaoTLSRootCRLError("original-openbao-tls-custody")
	}
	if !environmentPreflightDigest.MatchString(parent) || !environmentBootstrapOwnerUUID.MatchString(root.ID) {
		return nil, openBaoTLSRootCRLError("custody-parent")
	}
	paths := []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/openbao_tls-root-v1-key-passphrase", filepath.Join("deployment/create", parent, "bootstrap/openbao_tls/root-active.json")}
	out := map[string]string{}
	for _, relative := range paths {
		if err := validateEnvironmentPreflightDirectories(store, filepath.Dir(filepath.Join(store.Root, relative))); err != nil {
			return nil, err
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
		if err != nil {
			return nil, err
		}
		out[filepath.ToSlash(relative)] = environmentPreflightBytesSHA256(raw)
	}
	return out, nil
}

func prepareOpenBaoTLSRootCRLCandidate(ctx context.Context, store secretStore, e *environmentCreateExecution, image string, root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, material map[string]string) (string, createRootCRLRequest, createRootCRLRecord, error) {
	var request createRootCRLRequest
	var signed createRootCRLRecord
	selector := deploymentplan.Digest(deploymentplan.Object{"parent": e.Journal.PlanSHA256, "source": deploymentplan.Digest(e.Journal), "root": root, "snapshot": snapshot, "material": material})
	candidate := filepath.ToSlash(filepath.Join("deployment/repairs/openbao-tls-root-crl", selector))
	if err := prepareOpenBaoTLSRootCRLPrivateParent(store, filepath.Join(store.Root, candidate)); err != nil {
		return candidate, request, signed, err
	}
	descriptorPath := filepath.Join(store.Root, candidate, "candidate.json")
	var descriptor openBaoTLSRootCRLCandidate
	if _, err := os.Lstat(descriptorPath); os.IsNotExist(err) {
		request, err = buildCreateRootCRLRequestForDomain(root, snapshot, "openbao_tls", time.Now().UTC())
		if err != nil {
			return candidate, request, signed, err
		}
		descriptor = openBaoTLSRootCRLCandidate{createOpenBaoTLSRootCRLSchema + "/candidate", e.Journal.PlanSHA256, deploymentplan.Digest(e.owner), deploymentplan.Digest(e.Journal), deploymentplan.Digest(snapshot), root, snapshot, material, request}
		if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(candidate, "candidate.json"), descriptor); err != nil {
			return candidate, request, signed, err
		}
	} else if err != nil || readExecutionJSON(descriptorPath, &descriptor) != nil || descriptor.Schema != createOpenBaoTLSRootCRLSchema+"/candidate" || descriptor.Parent != e.Journal.PlanSHA256 || descriptor.SourceOwnerSHA != deploymentplan.Digest(e.owner) || descriptor.SourceJournalSHA != deploymentplan.Digest(e.Journal) || descriptor.SnapshotSHA != deploymentplan.Digest(snapshot) || deploymentplan.Digest(descriptor.Root) != deploymentplan.Digest(root) || deploymentplan.Digest(descriptor.Material) != deploymentplan.Digest(material) {
		return candidate, request, signed, openBaoTLSRootCRLError("stable-candidate-source")
	}
	request = descriptor.Request
	if request.Number != "2" || request.IssuerID != root.ID || request.Fingerprint != root.RootFingerprint {
		return candidate, request, signed, openBaoTLSRootCRLError("stable-request")
	}
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(candidate, "request.json"), request); err != nil {
		return candidate, request, signed, err
	}
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(candidate, "issuer.json"), root.Document); err != nil {
		return candidate, request, signed, err
	}
	signedDir := filepath.Join(store.Root, candidate, "signed")
	if _, err := os.Lstat(signedDir); os.IsNotExist(err) {
		if err := e.Check(ctx); err != nil {
			return candidate, request, signed, err
		}
		if err := runEnvironmentCreatePKICeremony(ctx, store, image, "crl", "--issuer", "/store/"+candidate+"/issuer.json", "--crl-request", "/store/"+candidate+"/request.json", "--key", "/store/pki/custody/roots/"+root.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/openbao_tls-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(request), "--out", "/store/"+candidate+"/signed"); err != nil {
			return candidate, request, signed, err
		}
	} else if err != nil {
		return candidate, request, signed, err
	}
	if err := validateEnvironmentPreflightDirectories(store, signedDir); err != nil {
		return candidate, request, signed, err
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(signedDir, "revocations.pem"), true)
	if err != nil {
		return candidate, request, signed, openBaoTLSRootCRLError("incomplete-signed-candidate-preserve")
	}
	signed, err = validateCreateRootCRLSignedForDomain(root, snapshot, request, raw, "openbao_tls", time.Now().UTC())
	return candidate, request, signed, err
}

func openBaoTLSRootCRLNativeImportPlan(p createOpenBaoTLSRootCRLPlan) createRootCRLRecoveryPlan {
	return createRootCRLRecoveryPlan{Root: p.Root, Snapshot: p.Snapshot, Request: p.Request, Signed: p.Signed, Targets: []createRootCRLStateTarget{{ID: p.Controller.ID, UID: p.Controller.UID, Container: "pki-controller", PodName: p.ControllerPodName, PodUID: p.ControllerPodUID, Desired: p.Controller.Desired}}}
}

func writeOpenBaoTLSRootCRLImmutable(store secretStore, relative string, value any) error {
	raw := mustProviderRecoveryJSON(value)
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if err := prepareOpenBaoTLSRootCRLPrivateParent(store, filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		old, readErr := readBoundedEnvironmentFile(path, true)
		if readErr != nil || !bytes.Equal(old, raw) {
			return openBaoTLSRootCRLError("immutable-artifact-conflict")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeEnvironmentCreateDerivedPrivateFile(store, relative, raw)
}

// Create only missing private parents. Existing mode/type mismatches fail;
// qualification never repairs directory permissions or follows a symlink.
func prepareOpenBaoTLSRootCRLPrivateParent(store secretStore, directory string) error {
	relative, err := filepath.Rel(store.Root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !filepath.IsAbs(store.Root) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) || validateEnvironmentPreflightDirectory(store.ConfigRoot) != nil || validateEnvironmentPreflightDirectory(store.Root) != nil {
		return openBaoTLSRootCRLError("canonical-private-parent")
	}
	if relative == "." {
		return nil
	}
	current := store.Root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if _, err := os.Lstat(current); os.IsNotExist(err) {
			if err := os.Mkdir(current, 0700); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if validateEnvironmentPreflightDirectory(current) != nil {
			return openBaoTLSRootCRLError("private-parent-mode-or-type")
		}
	}
	return nil
}

func loadOpenBaoTLSRootCRLTargets(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, root environmentCreateRegisteredIssuer, prior createRootCRLRecord) (deploymentplan.Resource, deploymentplan.Resource, []createRootCRLStateTarget, string, string, error) {
	var controller, cm deploymentplan.Resource
	if r == nil || r.OpenBaoIntermediate == nil || late == nil || late.AcceptedCore == nil || root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" {
		return controller, cm, nil, "", "", openBaoTLSRootCRLError("exact-original-core")
	}
	originals := map[string]deploymentplan.Resource{}
	for _, resource := range late.AcceptedCore.Resources {
		resource.UID = late.AcceptedCoreJournal.Owners[resource.ID]
		originals[resource.ID] = resource
	}
	controller = originals["Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller"]
	cm = originals["ConfigMap/"+e.Journal.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust"]
	if controller.UID == "" || cm.UID == "" {
		return controller, cm, nil, "", "", openBaoTLSRootCRLError("actual-completed-Core-owners")
	}
	ica, err := readOpenBaoTLSRootCRLIntermediate(ctx, cfg, e, b, root, *r.OpenBaoIntermediate)
	if err != nil {
		return controller, cm, nil, "", "", err
	}
	if validateOpenBaoTLSRootCRLManifest(cm, root, *r.OpenBaoIntermediate) != nil {
		return controller, cm, nil, "", "", openBaoTLSRootCRLError("unchanged-original-manifest")
	}
	live, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return controller, cm, nil, "", "", err
	}
	byID := map[string]deploymentplan.Object{}
	for _, object := range live {
		byID[deploymentplan.ID(object)] = object
	}
	ownedPod := func(id string) (string, string, error) {
		name, uid := "", ""
		for _, object := range live {
			if object["kind"] != "Pod" {
				continue
			}
			owner, err := environmentPodController(object, byID)
			if err == nil && owner == id {
				if uid != "" || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
					return "", "", openBaoTLSRootCRLError("one-genuine-owned-Pod")
				}
				name = deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
				uid = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			}
		}
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return "", "", openBaoTLSRootCRLError("original-owned-Pod-UID")
		}
		return name, uid, nil
	}
	cp, cu, err := ownedPod(controller.ID)
	if err != nil {
		return controller, cm, nil, "", "", err
	}
	targets := []createRootCRLStateTarget{}
	for _, name := range []string{"certissuer", "pki-controller"} {
		resource := originals["Deployment/"+e.Journal.Stack+"-video-cloud/"+name]
		c := managedNamedField(environmentPKIPod(resource.Desired)["containers"], name)
		file, err := coreMQTTFile(resource.Desired, c, "openbao-create-trust", "crls.json")
		if resource.UID == "" || c == nil || err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-openbao-tls-consumer-trust" || file.file != "/run/pki-openbao-create/crls.json" || mqttFoundationEnv(c, "OPENBAO_SERVER_CRL_MANIFEST") != file.file || mqttFoundationEnv(c, "OPENBAO_SERVER_PKI_ROOT_SHA256") != root.RootFingerprint {
			return controller, cm, nil, "", "", openBaoTLSRootCRLError("native-original-provider-consumer")
		}
		volumes, volErr := environmentPKINamedFields(environmentPKIPod(resource.Desired)["volumes"])
		mounts, mountErr := environmentPKINamedFields(c["volumeMounts"])
		claimName := deploymentplan.String(deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"])
		expectedClaim := "certissuer-service-state"
		if name == "pki-controller" {
			expectedClaim = "pki-controller-state"
		}
		if volErr != nil || mountErr != nil || claimName != expectedClaim || mounts["service-state"]["mountPath"] != "/var/lib/pki-service" || mounts["service-state"]["readOnly"] == true || mounts["service-state"]["subPath"] != nil {
			return controller, cm, nil, "", "", openBaoTLSRootCRLError("original-private-state-PVC-mount")
		}
		claimID := "PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/" + claimName
		claimUID := deploymentplan.String(deploymentplan.Map(byID[claimID]["metadata"])["uid"])
		pod, puid, err := ownedPod(resource.ID)
		if err != nil || !environmentBootstrapOwnerUUID.MatchString(claimUID) {
			return controller, cm, nil, "", "", openBaoTLSRootCRLError("original-claim-and-Pod")
		}
		target := createRootCRLStateTarget{ID: resource.ID, UID: resource.UID, Desired: resource.Desired, Container: name, PodName: pod, PodUID: puid, ClaimID: claimID, ClaimUID: claimUID, Paths: []string{"/var/lib/pki-service/private/crl/openbao-root.json", "/var/lib/pki-service/private/crl/openbao-root-" + r.OpenBaoIntermediate.ID + ".json"}, StateSHA: map[string]string{}}
		raw, err := readOpenBaoTLSRootCRLState(ctx, cfg, b, target, root, prior)
		if err != nil {
			return controller, cm, nil, "", "", err
		}
		rawICA, err := readOpenBaoTLSRootCRLIntermediateState(ctx, cfg, b, target, *r.OpenBaoIntermediate, ica)
		if err != nil {
			return controller, cm, nil, "", "", err
		}
		target.StateSHA[target.Paths[0]], target.StateSHA[target.Paths[1]] = environmentPreflightBytesSHA256(raw), environmentPreflightBytesSHA256(rawICA)
		targets = append(targets, target)
	}
	return controller, cm, targets, cp, cu, nil
}

func readOpenBaoTLSRootCRLState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, root environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]byte, error) {
	if root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" || root.Domain != "openbao_tls" || root.Kind != "root" || len(target.Paths) != 2 || target.Paths[0] != "/var/lib/pki-service/private/crl/openbao-root.json" {
		return nil, openBaoTLSRootCRLError("original-root-cache")
	}
	raw, err := readOpenBaoTLSRootCRLCacheBytes(ctx, cfg, b, target, target.Paths[0])
	if err != nil {
		return nil, err
	}
	if validateOpenBaoTLSRootCRLCache(raw, root, record, false) != nil {
		return nil, openBaoTLSRootCRLError("signed-root-cache")
	}
	return raw, nil
}
func readOpenBaoTLSRootCRLIntermediateState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, ica environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]byte, error) {
	if ica.Domain != "openbao_tls" || ica.Kind != "intermediate" || record.Number != "3" || len(target.Paths) != 2 || target.Paths[1] != "/var/lib/pki-service/private/crl/openbao-root-"+ica.ID+".json" {
		return nil, openBaoTLSRootCRLError("original-ICA3-cache")
	}
	raw, err := readOpenBaoTLSRootCRLCacheBytes(ctx, cfg, b, target, target.Paths[1])
	if err != nil {
		return nil, err
	}
	if validateOpenBaoTLSRootCRLCache(raw, ica, record, true) != nil {
		return nil, openBaoTLSRootCRLError("signed-ICA3-cache")
	}
	return raw, nil
}
func readOpenBaoTLSRootCRLCacheBytes(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, path string) ([]byte, error) {
	name := deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
	if name != "certissuer" && name != "pki-controller" || !slices.Contains(target.Paths, path) || target.Container != name {
		return nil, openBaoTLSRootCRLError("exact-two-native-consumer-owners")
	}
	if stage := verifyCreateRootCRLTargetOwnersStage(ctx, cfg, b, target); stage != "" {
		return nil, openBaoTLSRootCRLError("exact-two-native-consumer-owners-" + stage)
	}
	security := deploymentplan.Map(environmentPKIPod(target.Desired)["securityContext"])
	if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["fsGroup"] != float64(10001) {
		return nil, openBaoTLSRootCRLError("original-private-cache-fsgroup")
	}
	ready := deploymentplan.Resource{ID: target.ID, UID: target.UID, Desired: target.Desired, Action: "retain"}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := b.Wait(bounded, ready)
	cancel()
	if err != nil {
		return nil, openBaoTLSRootCRLError("actual-native-provider-consumer-Ready")
	}
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", openBaoTLSRootCRLCacheReadScript, "openbao-tls-root-crl-native-state", path)
	if err != nil {
		return nil, openBaoTLSRootCRLError("owned-regular0600-cache")
	}
	return raw, nil
}

// Kubernetes fsGroup may preserve the setgid bit on the original private PVC
// directories. Neither allowed tuple grants group or world access. Local
// environment-store directory guards remain independently restricted to 0700.
const openBaoTLSRootCRLCacheReadScript = `for rtk_tls_dir in /var/lib/pki-service/private /var/lib/pki-service/private/crl; do
test -d "$rtk_tls_dir" && test ! -L "$rtk_tls_dir" || exit 1
case "$(stat -c '%a %u %g' "$rtk_tls_dir")" in
'700 10001 10001'|'2700 10001 10001') ;;
*) exit 1 ;;
esac
done
test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`

func validateOpenBaoTLSRootCRLCache(raw []byte, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord, fresh bool) error {
	var state struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		CRL         createRootCRLRecord `json:"crl"`
		Terminal    string              `json:"terminal_issuer_id"`
		Retained    json.RawMessage     `json:"retained_crl"`
	}
	cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil || state.Fingerprint != environmentPreflightBytesSHA256(cert.Raw) || state.Terminal != "" || len(state.Retained) != 0 || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(record) {
		return openBaoTLSRootCRLError("exact-signed-cache-and-original-issuer")
	}
	if issuer.Kind == "root" {
		_, err = parseCreateRootCRLForDomain(issuer, record, "openbao_tls", fresh, record.ThisUpdate)
	} else {
		_, err = validateManagedLateSignedCRL("openbao_tls", issuer.ID, environmentPreflightBytesSHA256(cert.Raw), issuer.Status, issuer.ChainPEM, managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}, managedLateObserveACKReady, time.Now().UTC())
	}
	return err
}
func validateOpenBaoTLSRootCRLManifest(cm deploymentplan.Resource, root, ica environmentCreateRegisteredIssuer) error {
	var entries []struct {
		Issuer map[string]any `json:"issuer"`
		Path   string         `json:"state_path"`
	}
	raw := deploymentplan.String(deploymentplan.Map(cm.Desired["data"])["crls.json"])
	if decodeEnvironmentJSON([]byte(raw), &entries) != nil || len(entries) != 2 || deploymentplan.Digest(entries[0].Issuer) != deploymentplan.Digest(root.Document) || deploymentplan.Digest(entries[1].Issuer) != deploymentplan.Digest(ica.Document) || entries[0].Path != "/var/lib/pki-service/private/crl/openbao-root.json" || entries[1].Path != "/var/lib/pki-service/private/crl/openbao-root-"+ica.ID+".json" {
		return openBaoTLSRootCRLError("original-root-plus-ICA3-manifest")
	}
	return nil
}
func readOpenBaoTLSRootCRLIntermediate(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, root, ica environmentCreateRegisteredIssuer) (createRootCRLRecord, error) {
	var empty createRootCRLRecord
	if root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" || root.Domain != "openbao_tls" || ica.Domain != root.Domain || ica.Kind != "intermediate" || ica.Status != "active" || ica.RootFingerprint != root.RootFingerprint || ica.Document["parent_issuer_id"] != root.ID {
		return empty, openBaoTLSRootCRLError("original-native-ICA3-lineage")
	}
	rootCert, rootErr := parseFirstCertificatePEM([]byte(root.Certificate))
	icaCert, icaErr := parseFirstCertificatePEM([]byte(ica.Certificate))
	if rootErr != nil || icaErr != nil || icaCert.CheckSignatureFrom(rootCert) != nil {
		return empty, openBaoTLSRootCRLError("original-root-signed-ICA")
	}
	native, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, ica.ID)
	if err != nil || native.Prior.Number != "3" {
		return empty, openBaoTLSRootCRLError("genuine-latest-ICA3")
	}
	cert, err := parseFirstCertificatePEM([]byte(ica.Certificate))
	if err != nil {
		return empty, err
	}
	record := native.Prior
	_, err = validateManagedLateSignedCRL("openbao_tls", ica.ID, environmentPreflightBytesSHA256(cert.Raw), ica.Status, ica.ChainPEM, managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}, managedLateObserveACKReady, time.Now().UTC())
	return record, err
}
func readOpenBaoTLSRootCRLVolumes(e *environmentCreateExecution, targets []createRootCRLStateTarget, materials map[string]string, restoreReview string) (map[string]environmentCreateVolumeOwner, map[string]environmentCreateVolumeOwner, error) {
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return nil, nil, err
	}
	if len(ledger.Volumes) > 18 {
		var active managedTransportVolumeLedgerActive
		if createOpenBaoACKCompletionRead(e.Store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
			return nil, nil, openBaoTLSRootCRLError("original-active-volume-ledger")
		}
	}
	ledger, err = managedLateRedisHistoricalVolumes(e.Store, e, ledger)
	if err != nil {
		return nil, nil, err
	}
	if len(ledger.Volumes) != 18 {
		return nil, nil, openBaoTLSRootCRLError("eighteen-original-volumes")
	}
	all := map[string]environmentCreateVolumeOwner{}
	hashes := map[string]string{}
	for _, record := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + record.Namespace + "/" + record.PVCName
		owner, err := managedTransportRecordedHostVolume(ledger, id)
		if err != nil {
			return nil, nil, err
		}
		all[id] = owner
		hashes[id] = deploymentplan.Digest(owner)
	}
	restored, _, err := loadAdmittedCreateRecovery(e.Store, e, restoreReview)
	key := "accepted-service-core-managed-complete-current-native-volumes"
	if err != nil || len(all) != 18 || !environmentPreflightDigest.MatchString(restored.Plan.Materials[key]) || deploymentplan.Digest(hashes) != restored.Plan.Materials[key] || materials[key] != restored.Plan.Materials[key] {
		return nil, nil, openBaoTLSRootCRLError("original-accepted-Core-eighteen-volume-records")
	}
	owners := map[string]environmentCreateVolumeOwner{}
	for _, target := range targets {
		owner, ok := all[target.ClaimID]
		if !ok || owner.PVCUID != target.ClaimUID {
			return nil, nil, openBaoTLSRootCRLError("immutable-original-consumer-volume")
		}
		owners[target.ClaimID] = owner
	}
	if len(owners) != 2 {
		return nil, nil, openBaoTLSRootCRLError("two-original-private-consumer-claims")
	}
	return owners, all, nil
}

func verifyOpenBaoTLSRootCRLInputs(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, plan createOpenBaoTLSRootCRLPlan, afterImport bool) error {
	if e.Check(ctx) != nil || plan.Parent != e.Journal.PlanSHA256 || plan.SourceOwnerSHA != deploymentplan.Digest(e.owner) || plan.ConfigurationSHA != deploymentplan.Digest(cfg) || deploymentplan.Digest(plan.Configuration) != plan.ConfigurationSHA {
		return openBaoTLSRootCRLError("source-owner/configuration")
	}
	material, err := createOpenBaoTLSRootCRLMaterial(store, plan.Parent, plan.Root)
	if err != nil || deploymentplan.Digest(material) != deploymentplan.Digest(plan.Material) {
		return openBaoTLSRootCRLError("unchanged-original-offline-custody")
	}
	for _, resource := range []deploymentplan.Resource{plan.ConfigMap, plan.Controller} {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return openBaoTLSRootCRLError("original-owner-" + resource.ID)
		}
	}
	ica, icaErr := readOpenBaoTLSRootCRLIntermediate(ctx, cfg, e, b, plan.Root, plan.Intermediate)
	if icaErr != nil || deploymentplan.Digest(ica) != deploymentplan.Digest(plan.IntermediateCRL) {
		return openBaoTLSRootCRLError("original-ICA3-native-drift")
	}
	for _, target := range plan.Consumers {
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, target); err != nil {
			return err
		}
	}
	controllerPod, err := b.Read(ctx, "Pod/"+e.Journal.Stack+"-video-cloud/"+plan.ControllerPodName)
	if err != nil || controllerPod == nil || deploymentplan.Map(controllerPod["metadata"])["uid"] != plan.ControllerPodUID {
		return openBaoTLSRootCRLError("original-native-importer-Pod")
	}
	var before environmentCreateJournal
	sourcePath := filepath.Join(store.Root, openBaoTLSRootCRLBase(deploymentplan.Digest(plan)), "source-journal.json")
	if _, err := os.Lstat(sourcePath); os.IsNotExist(err) {
		// During first qualification the immutable source file is not written
		// yet; only the exact unchanged original snapshot may be used.
		if plan.SourceJournalSHA != deploymentplan.Digest(e.Journal) {
			return openBaoTLSRootCRLError("source-snapshot")
		}
		before = e.Journal
	} else if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(sourcePath)) != nil || readExecutionJSON(sourcePath, &before) != nil {
		return openBaoTLSRootCRLError("canonical-source-snapshot")
	}
	old := *e
	old.Journal = before
	p := createFoundationRecoveryPlan{Plan: plan.Generation, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(cfg, store, &old, &p, managedLateSessionOpenBaoTLSRepair)
	if err != nil || late.SourceArtifactSHA != plan.SourceArtifactSHA || late.AcceptedCore.SHA256() != plan.SourceCoreSHA || r.ManagedCorePartial.Apps.SHA256() != plan.SourceAppsSHA || r.OpenBaoRoot == nil || deploymentplan.Digest(*r.OpenBaoRoot) != deploymentplan.Digest(plan.Root) {
		return openBaoTLSRootCRLError("actual-source-ancestry")
	}
	// Preserve the independently authenticated late authority for current native
	// reads after import, without replaying an ancestral Core source loader.
	r.ManagedLateSession = late
	if !afterImport {
		if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, &p, late, managedLateSessionOpenBaoTLSRepair); err != nil {
			return err
		}
		return nil
	}
	// No expired-prior observer after native #2. Retain its independent strict
	// native identity/volume/export/session/registry checks; the caller separately
	// verifies exact admitted #2 publication, current root cache and real ACKs.
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, r, &p); err != nil {
		return err
	}
	if err := verifyManagedCoreAllVolumes(ctx, e, b, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, r.ManagedApplicationPartial, p.Materials); err != nil {
		return err
	}
	originalAPIRows := p.Materials[managedLateAPINativeMaterial]
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, r, p.Materials); err != nil {
		return err
	}
	if err := bindManagedLateFreshAPIServers(p.Materials, originalAPIRows); err != nil {
		return err
	}
	if err := verifyManagedCoreCurrentSession(ctx, cfg, store, e, b, r, &p, managedCoreRestoreOnly); err != nil {
		return err
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &p); err != nil {
		return err
	}
	return e.Check(ctx)
}

func openBaoTLSRootCRLImportIntent(p createOpenBaoTLSRootCRLPlan) map[string]string {
	return map[string]string{"issuer_id": p.Root.ID, "root_sha256": p.Root.RootFingerprint, "prior_crl_sha256": p.Snapshot.Prior.Digest, "signed_crl_sha256": p.Signed.Digest, "request_sha256": deploymentplan.Digest(p.Request)}
}
func openBaoTLSRootCRLRuntimeIntent(p createOpenBaoTLSRootCRLPlan, target createRootCRLStateTarget) deploymentplan.Object {
	return deploymentplan.Object{"target": target, "native_crl_sha256": p.Signed.Digest}
}

func executeCreateOpenBaoTLSRootCRL(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createOpenBaoTLSRootCRLArtifact, source environmentCreateJournal, out io.Writer) (resultErr error) {
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	key := "openbao-tls-root-crl-renewal-" + review
	if !openBaoTLSRootCRLProgressValid(e.Journal, a, source, false) {
		return openBaoTLSRootCRLError("exact-admitted-tail")
	}
	admitted := len(e.Journal.Entries) > len(source.Entries) && e.Journal.Entries[len(source.Entries)].Key == key
	if !admitted && !time.Now().Before(a.ValidUntil) {
		return openBaoTLSRootCRLError("fresh-go-required")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return err
	}
	signed, err := validateCreateRootCRLSignedForDomain(plan.Root, plan.Snapshot, plan.Request, raw, "openbao_tls", time.Now().UTC())
	if err != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(plan.Signed) {
		return openBaoTLSRootCRLError("qualified-signed-bytes")
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil {
		return err
	}
	imported := deploymentplan.Digest(current.Prior) == deploymentplan.Digest(plan.Signed)
	if !openBaoTLSRootCRLSnapshotMatches(plan, current, openBaoTLSRootCRLImportAdmitted(e.Journal, a, source)) {
		return openBaoTLSRootCRLError("qualified-live-revocations-changed")
	}
	if err := verifyOpenBaoTLSRootCRLInputs(ctx, cfg, store, e, b, plan, imported); err != nil {
		return err
	}
	// Once all four real native entries completed, a pointer-only publication
	// retry must not append a new Kubernetes lease or alter the immutable final
	// journal. Re-observe the same runtime evidence and publish its exact receipt.
	if openBaoTLSRootCRLProgressValid(e.Journal, a, source, true) {
		evidence := map[string]string{}
		for _, target := range plan.Consumers {
			observed, err := observeOpenBaoTLSRootCRLConsumer(ctx, cfg, store, e, b, a, target)
			if err != nil {
				return err
			}
			for key, value := range observed {
				if old, exists := evidence[key]; exists && old != value {
					return openBaoTLSRootCRLError("native-publication-disagrees")
				}
				evidence[key] = value
			}
		}
		return publishCreateOpenBaoTLSRootCRLCompletion(ctx, store, e, a, source, evidence, out)
	}
	if err := rootCRLStepBegin(e, key, plan); err != nil {
		return err
	}
	lease, err := b.AcquireExecution(ctx, rootCRLLeasePlan(cfg, review))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}
	importKey := key + "/native-import"
	importStarted := time.Now().UTC()
	if err := rootCRLStepBegin(e, importKey, openBaoTLSRootCRLImportIntent(plan)); err != nil {
		return err
	}
	if !imported {
		if err := guard(); err != nil {
			return err
		}
		if err := verifyOpenBaoTLSRootCRLInputs(ctx, cfg, store, e, b, plan, false); err != nil {
			return err
		}
		result, err := runNativeCreateRootCRLImport(ctx, cfg, b, openBaoTLSRootCRLNativeImportPlan(plan), true)
		if err != nil {
			return err
		}
		if !openBaoTLSRootCRLNativeResultValid(result, plan, true) {
			return openBaoTLSRootCRLError("native-import-result")
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(plan.Candidate, "native-import.log"), result); err != nil {
			return err
		}
	}
	current, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(plan.Signed) {
		return openBaoTLSRootCRLError("actual-native-publication")
	}
	if !openBaoTLSRootCRLSnapshotMatches(plan, current, true) {
		return openBaoTLSRootCRLError("native-revocation-inventory-changed")
	}
	if err := rootCRLStepComplete(e, importKey, map[string]any{"native_crl": plan.Signed, "request_sha256": deploymentplan.Digest(plan.Request)}); err != nil {
		return err
	}
	openBaoTLSRootCRLReportStep(out, "native-import-readback", importStarted)
	nativeEvidence := map[string]string{}
	for _, target := range plan.Consumers {
		consumerStarted := time.Now().UTC()
		name := deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
		step := key + "/native-runtime-" + name
		if err := rootCRLStepBegin(e, step, openBaoTLSRootCRLRuntimeIntent(plan, target)); err != nil {
			return err
		}
		var evidence map[string]string
		for {
			if err := guard(); err != nil {
				return err
			}
			evidence, err = observeOpenBaoTLSRootCRLConsumer(ctx, cfg, store, e, b, a, target)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return openBaoTLSRootCRLError("native-runtime-timeout-" + name)
			case <-time.After(2 * time.Second):
			}
		}
		for k, v := range evidence {
			if old, ok := nativeEvidence[k]; ok && old != v {
				return openBaoTLSRootCRLError("native-publication-disagrees")
			}
			nativeEvidence[k] = v
		}
		if err := rootCRLStepComplete(e, step, evidence); err != nil {
			return err
		}
		openBaoTLSRootCRLReportStep(out, "native-runtime-"+name, consumerStarted)
	}
	if err := guard(); err != nil {
		return err
	}
	if err := verifyOpenBaoTLSRootCRLInputs(ctx, cfg, store, e, b, plan, true); err != nil {
		return err
	}
	// Re-read both installed caches and the exact native ACK roster immediately
	// before completion. Earlier successful observations grant no stale authority.
	for _, target := range plan.Consumers {
		observed, err := observeOpenBaoTLSRootCRLConsumer(ctx, cfg, store, e, b, a, target)
		if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(openBaoTLSRootCRLEvidenceForTarget(nativeEvidence, target)) {
			return openBaoTLSRootCRLError("final-two-current-consumers")
		}
	}
	receipt := createOpenBaoTLSRootCRLReceipt{createOpenBaoTLSRootCRLSchema, plan.Parent, review, filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), plan.Signed.Digest, deploymentplan.Digest(nativeEvidence), true}
	if err := rootCRLStepComplete(e, key, receipt); err != nil {
		return err
	}
	return publishCreateOpenBaoTLSRootCRLCompletion(ctx, store, e, a, source, nativeEvidence, out)
}

func publishCreateOpenBaoTLSRootCRLCompletion(ctx context.Context, store secretStore, e *environmentCreateExecution, a createOpenBaoTLSRootCRLArtifact, source environmentCreateJournal, nativeEvidence map[string]string, out io.Writer) error {
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	if err := e.Check(ctx); err != nil {
		return err
	}
	if !openBaoTLSRootCRLProgressValid(e.Journal, a, source, true) {
		return openBaoTLSRootCRLError("actual-four-completed-steps")
	}
	receipt := createOpenBaoTLSRootCRLReceipt{createOpenBaoTLSRootCRLSchema, plan.Parent, review, filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), plan.Signed.Digest, deploymentplan.Digest(nativeEvidence), true}
	var actual createOpenBaoTLSRootCRLReceipt
	if decodeEnvironmentJSON(e.Journal.Entries[len(source.Entries)].Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(receipt) {
		return openBaoTLSRootCRLError("completed-exact-top-receipt")
	}
	for i, target := range plan.Consumers {
		var actual map[string]string
		if decodeEnvironmentJSON(e.Journal.Entries[len(source.Entries)+2+i].Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(openBaoTLSRootCRLEvidenceForTarget(nativeEvidence, target)) {
			return openBaoTLSRootCRLError("completed-exact-runtime-receipt")
		}
	}
	completion := createOpenBaoTLSRootCRLCompletion{Schema: createOpenBaoTLSRootCRLSchema + "/completion", Parent: plan.Parent, Review: review, Completed: true, SourceEntryCount: len(source.Entries), FinalEntryCount: len(e.Journal.Entries), FinalEntriesSHA: deploymentplan.Digest(e.Journal.Entries), FinalJournalSHA: deploymentplan.Digest(e.Journal), NativeEvidence: nativeEvidence, Receipt: receipt}
	base := openBaoTLSRootCRLBase(review)
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(base, "final-journal.json"), e.Journal); err != nil {
		return err
	}
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join(base, "completed.json"), completion); err != nil {
		return err
	}
	if err := writeOpenBaoTLSRootCRLImmutable(store, filepath.Join("deployment/create/material", plan.Parent, "openbao-tls-root-crl-renewal.json"), completion); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-openbao-tls-root-crl review-sha256=%s crl-number=2 native-consumers=2 original-identity-preserved=true\n", review)
	return nil
}

func openBaoTLSRootCRLReportStep(out io.Writer, stage string, started time.Time) {
	ended := time.Now().UTC()
	fmt.Fprintf(out, "PASS step=%s started=%s ended=%s duration=%.3fs\n", stage, started.Format(time.RFC3339Nano), ended.Format(time.RFC3339Nano), ended.Sub(started).Seconds())
}

func observeOpenBaoTLSRootCRLConsumer(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createOpenBaoTLSRootCRLArtifact, target createRootCRLStateTarget) (map[string]string, error) {
	p := a.Plan
	if _, err := parseCreateRootCRLForDomain(p.Root, p.Signed, "openbao_tls", true, time.Now().UTC()); err != nil {
		return nil, err
	}
	current, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(p.Signed) {
		return nil, openBaoTLSRootCRLError("native-current-two")
	}
	current.Prior = p.Snapshot.Prior
	if deploymentplan.Digest(current) != p.SnapshotSHA {
		return nil, openBaoTLSRootCRLError("current-genuine-inventory")
	}
	raw, err := readOpenBaoTLSRootCRLState(ctx, cfg, b, target, p.Root, p.Signed)
	if err != nil {
		return nil, err
	}
	icaSnapshot, icaHistorySHA, currentICA, icaErr := readCompletedOpenBaoTLSCurrentIntermediate(ctx, cfg, store, e, b, a)
	if icaErr != nil {
		return nil, icaErr
	}
	var icaRaw []byte
	if currentICA {
		icaRaw, icaErr = readOpenBaoTLSCurrentIntermediateState(ctx, cfg, b, target, p.Intermediate, icaSnapshot.Prior)
	} else {
		// First publication remains bound to its exact admitted ICA3 and cache.
		ica, readErr := readOpenBaoTLSRootCRLIntermediate(ctx, cfg, e, b, p.Root, p.Intermediate)
		if readErr != nil || deploymentplan.Digest(ica) != deploymentplan.Digest(p.IntermediateCRL) {
			return nil, openBaoTLSRootCRLError("unchanged-genuine-ICA3")
		}
		icaRaw, icaErr = readOpenBaoTLSRootCRLIntermediateState(ctx, cfg, b, target, p.Intermediate, ica)
		if icaErr == nil && environmentPreflightBytesSHA256(icaRaw) != target.StateSHA[target.Paths[1]] {
			return nil, openBaoTLSRootCRLError("unchanged-ICA3-cache")
		}
	}
	if icaErr != nil {
		return nil, icaErr
	}
	name := deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
	query := "SELECT json_build_object('imports',(SELECT count(*) FROM pki_audit WHERE issuer_id='" + p.Root.ID + "' AND event='crl_imported:" + p.Signed.Digest + "'),'imported_at',(SELECT min(created_at) FROM pki_audit WHERE issuer_id='" + p.Root.ID + "' AND event='crl_imported:" + p.Signed.Digest + "'),'acks',COALESCE((SELECT json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id) FROM pki_crl_acknowledgments WHERE issuer_id='" + p.Root.ID + "' AND digest='" + p.Signed.Digest + "'),'[]'::json))"
	ack, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var publication struct {
		Imports int                    `json:"imports"`
		At      time.Time              `json:"imported_at"`
		ACKs    []openBaoTLSRootCRLACK `json:"acks"`
	}
	if err != nil || decodeEnvironmentJSON(ack, &publication) != nil || publication.Imports != 1 || publication.At.Before(p.Request.ThisUpdate) || !openBaoTLSRootCRLACKsValid(publication.ACKs, p.Root.ID, p.Signed.Digest, publication.At, time.Now().UTC()) || name != "certissuer" && name != "pki-controller" {
		return nil, openBaoTLSRootCRLError("real-latest-consumer-ACK")
	}
	ackSHA := deploymentplan.Digest(publication)
	if currentICA {
		icaACKs, err := verifyOpenBaoTLSCurrentIntermediateInstallation(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, p.Intermediate, icaSnapshot)
		if err != nil {
			return nil, err
		}
		ackSHA = deploymentplan.Digest(map[string]string{"root": ackSHA, "intermediate": deploymentplan.Digest(icaACKs), "intermediate-history": icaHistorySHA, "intermediate-current": deploymentplan.Digest(icaSnapshot)})
	}
	// The native protocol emits this ACK only after actual prepared state and
	// connection sweep. Disk equality alone is never an installation receipt.
	return map[string]string{"native-publication": environmentPreflightBytesSHA256(native), target.ID + ":state": deploymentplan.Digest(map[string]string{"root": environmentPreflightBytesSHA256(raw), "intermediate": environmentPreflightBytesSHA256(icaRaw)}), target.ID + ":owner": deploymentplan.Digest(target), target.ID + ":native-ack": ackSHA}, nil
}

func openBaoTLSRootCRLSnapshotMatches(plan createOpenBaoTLSRootCRLPlan, current createRootCRLNativeSnapshot, admitted bool) bool {
	if deploymentplan.Digest(current.Prior) == deploymentplan.Digest(plan.Signed) {
		if !admitted {
			return false
		}
		current.Prior = plan.Snapshot.Prior
	}
	return deploymentplan.Digest(current) == plan.SnapshotSHA
}

func openBaoTLSRootCRLEvidenceForTarget(all map[string]string, target createRootCRLStateTarget) map[string]string {
	part := map[string]string{}
	for _, key := range []string{"native-publication", target.ID + ":state", target.ID + ":owner", target.ID + ":native-ack"} {
		part[key] = all[key]
	}
	return part
}

func loadCreateOpenBaoTLSRootCRLArtifact(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, review string) (createOpenBaoTLSRootCRLArtifact, environmentCreateJournal, error) {
	var a createOpenBaoTLSRootCRLArtifact
	var source environmentCreateJournal
	var owner environmentCreateOwner
	base := filepath.Join(store.Root, openBaoTLSRootCRLBase(review))
	if !environmentPreflightDigest.MatchString(review) || validateEnvironmentPreflightDirectories(store, base) != nil || readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil {
		return a, source, openBaoTLSRootCRLError("canonical-artifact")
	}
	if a.Schema != createOpenBaoTLSRootCRLSchema+"/artifact" || a.Plan.Schema != createOpenBaoTLSRootCRLSchema || deploymentplan.Digest(a.Plan) != review || a.Plan.Parent != source.PlanSHA256 || a.Plan.SourceJournalSHA != deploymentplan.Digest(source) || a.Plan.SourceOwnerSHA != deploymentplan.Digest(owner) || a.Plan.ConfigurationSHA != deploymentplan.Digest(a.Plan.Configuration) || cfg.Environment != owner.Environment || store.Environment != owner.Environment || owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != a.Plan.Parent || owner.OwnerToken == "" || owner.OwnerToken != source.OwnerToken || owner.Stack != source.Stack || filepath.Clean(owner.Journal) != owner.Journal || filepath.IsAbs(owner.Journal) || !strings.HasPrefix(owner.Journal, "deployment/create/executions/"+a.Plan.Parent+"/execution-") || filepath.Base(owner.Journal) != "journal.json" || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.QualifiedAt.After(time.Now()) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createOpenBaoTLSRootCRLGates) {
		return a, source, openBaoTLSRootCRLError("actual-qualified-source")
	}
	for i, id := range createOpenBaoTLSRootCRLGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return a, source, openBaoTLSRootCRLError("required-scoped-gates")
		}
	}
	if a.Plan.Root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" || a.Plan.Root.Domain != "openbao_tls" || a.Plan.Root.Kind != "root" || a.Plan.Snapshot.Prior.Number != "1" || a.Plan.Signed.Number != "2" || a.Plan.SnapshotSHA != deploymentplan.Digest(a.Plan.Snapshot) || len(a.Plan.Consumers) != 2 {
		return a, source, openBaoTLSRootCRLError("strict-original-openbao-tls-two")
	}
	selector := deploymentplan.Digest(deploymentplan.Object{"parent": a.Plan.Parent, "source": a.Plan.SourceJournalSHA, "root": a.Plan.Root, "snapshot": a.Plan.Snapshot, "material": a.Plan.Material})
	if a.Plan.Candidate != filepath.ToSlash(filepath.Join("deployment/repairs/openbao-tls-root-crl", selector)) || validateEnvironmentPreflightDirectories(store, filepath.Join(store.Root, a.Plan.Candidate)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Join(store.Root, a.Plan.Candidate, "signed")) != nil {
		return a, source, openBaoTLSRootCRLError("canonical-signed-candidate")
	}
	generation, configurationSHA, err := loadEnvironmentCreateAdmittedGeneration(store, a.Plan.Parent, source.ArtifactSHA256)
	if err != nil || !environmentCreateGenerationMatchesAdmitted(generation, a.Plan.Generation) || configurationSHA != a.Plan.ConfigurationSHA {
		return a, source, openBaoTLSRootCRLError("immutable-original-parent-plan")
	}
	if e != nil && (e.Journal.PlanSHA256 != a.Plan.Parent || deploymentplan.Digest(e.owner) != deploymentplan.Digest(owner) || e.Store.Root != store.Root) {
		return a, source, openBaoTLSRootCRLError("actual-original-fence")
	}
	var dryRun map[string]string
	if !environmentPreflightDigest.MatchString(a.Plan.NativeDryRunSHA) || createOpenBaoACKCompletionRead(store, filepath.Join(a.Plan.Candidate, "native-dry-run.json"), &dryRun) != nil || len(dryRun) != 4 || dryRun["output_sha256"] != a.Plan.NativeDryRunSHA || dryRun["request_sha256"] != deploymentplan.Digest(a.Plan.Request) || dryRun["root_sha256"] != a.Plan.Root.RootFingerprint || dryRun["domain"] != "openbao_tls" {
		return a, source, openBaoTLSRootCRLError("actual-domain-dryrun-evidence")
	}
	old := &environmentCreateExecution{Store: store, owner: owner, Journal: source}
	if err := bindManagedLateRedisRecordedVolumeExecution(store, old); err != nil {
		return a, source, err
	}
	p := createFoundationRecoveryPlan{Plan: a.Plan.Generation, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(a.Plan.Configuration, store, old, &p, managedLateSessionOpenBaoTLSRepair)
	if err != nil || r.OpenBaoRoot == nil || deploymentplan.Digest(*r.OpenBaoRoot) != deploymentplan.Digest(a.Plan.Root) || late.SourceArtifactSHA != a.Plan.SourceArtifactSHA || late.AcceptedCore.SHA256() != a.Plan.SourceCoreSHA || r.ManagedCorePartial.Apps.SHA256() != a.Plan.SourceAppsSHA || !openBaoTLSRootCRLAcceptedTargetsValid(a.Plan, r, late) {
		return a, source, openBaoTLSRootCRLError("independent-actual-Core-ancestry")
	}
	restored, _, restoreErr := loadAdmittedCreateRecovery(store, old, late.RestoreReview)
	if restoreErr != nil || a.Plan.NativeSourceEvidence["accepted-service-core-managed-complete-current-native-volumes"] != restored.Plan.Materials["accepted-service-core-managed-complete-current-native-volumes"] {
		return a, source, openBaoTLSRootCRLError("independent-accepted-eighteen-volume-authority")
	}

	return a, source, nil
}

func openBaoTLSRootCRLAcceptedTargetsValid(plan createOpenBaoTLSRootCRLPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) bool {
	if r == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || late == nil || late.AcceptedCore == nil || len(plan.Consumers) != 2 || len(plan.ConsumerVolumes) != 2 || len(plan.OriginalVolumes) != 18 || deploymentplan.Digest(plan.Root) != deploymentplan.Digest(*r.OpenBaoRoot) || deploymentplan.Digest(plan.Intermediate) != deploymentplan.Digest(*r.OpenBaoIntermediate) || plan.IntermediateCRL.IssuerID != plan.Intermediate.ID || plan.IntermediateCRL.Number != "3" || !environmentBootstrapOwnerUUID.MatchString(plan.ControllerPodUID) || plan.ControllerPodName == "" {
		return false
	}
	accepted := map[string]deploymentplan.Resource{}
	for _, resource := range late.AcceptedCore.Resources {
		resource.UID = late.AcceptedCoreJournal.Owners[resource.ID]
		accepted[resource.ID] = resource
	}
	volumeHashes := map[string]string{}
	for id, owner := range plan.OriginalVolumes {
		if id != "PersistentVolumeClaim/"+owner.Namespace+"/"+owner.PVCName || !environmentBootstrapOwnerUUID.MatchString(owner.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) {
			return false
		}
		volumeHashes[id] = deploymentplan.Digest(owner)
	}
	if deploymentplan.Digest(volumeHashes) != plan.NativeSourceEvidence["accepted-service-core-managed-complete-current-native-volumes"] {
		return false
	}
	stack := plan.Generation.Stack
	if plan.Controller.ID != "Deployment/"+stack+"-video-cloud/pki-controller" || deploymentplan.Digest(accepted[plan.Controller.ID]) != deploymentplan.Digest(plan.Controller) || plan.ConfigMap.ID != "ConfigMap/"+stack+"-video-cloud/pki-create-openbao-tls-consumer-trust" || deploymentplan.Digest(accepted[plan.ConfigMap.ID]) != deploymentplan.Digest(plan.ConfigMap) || validateOpenBaoTLSRootCRLManifest(plan.ConfigMap, plan.Root, plan.Intermediate) != nil {
		return false
	}
	for index, name := range []string{"certissuer", "pki-controller"} {
		target := plan.Consumers[index]
		resource := accepted["Deployment/"+stack+"-video-cloud/"+name]
		if target.ID != resource.ID || target.UID == "" || target.UID != resource.UID || deploymentplan.Digest(target.Desired) != deploymentplan.Digest(resource.Desired) || target.Container != name || !environmentBootstrapOwnerUUID.MatchString(target.PodUID) || target.PodName == "" || !environmentBootstrapOwnerUUID.MatchString(target.ClaimUID) || len(target.Paths) != 2 || target.Paths[0] != "/var/lib/pki-service/private/crl/openbao-root.json" || target.Paths[1] != "/var/lib/pki-service/private/crl/openbao-root-"+plan.Intermediate.ID+".json" || len(target.StateSHA) != 2 || !environmentPreflightDigest.MatchString(target.StateSHA[target.Paths[0]]) || !environmentPreflightDigest.MatchString(target.StateSHA[target.Paths[1]]) {
			return false
		}
		c := managedNamedField(environmentPKIPod(resource.Desired)["containers"], name)
		volumes, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["volumes"])
		mounts, mountErr := environmentPKINamedFields(c["volumeMounts"])
		claim := deploymentplan.String(deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"])
		expectedClaim := "certissuer-service-state"
		if name == "pki-controller" {
			expectedClaim = "pki-controller-state"
		}
		owner, ok := plan.ConsumerVolumes[target.ClaimID]
		if err != nil || mountErr != nil || c == nil || claim != expectedClaim || target.ClaimID != "PersistentVolumeClaim/"+stack+"-video-cloud/"+claim || !ok || owner.Namespace != stack+"-video-cloud" || owner.PVCName != claim || owner.PVCUID != target.ClaimUID || deploymentplan.Digest(plan.OriginalVolumes[target.ClaimID]) != deploymentplan.Digest(owner) || mounts["service-state"]["mountPath"] != "/var/lib/pki-service" || mounts["service-state"]["readOnly"] == true || mounts["service-state"]["subPath"] != nil || mqttFoundationEnv(c, "OPENBAO_SERVER_CRL_MANIFEST") != "/run/pki-openbao-create/crls.json" || mqttFoundationEnv(c, "OPENBAO_SERVER_PKI_ROOT_SHA256") != plan.Root.RootFingerprint {
			return false
		}
	}
	return true
}

// Strict append-only scoped admission. Completion permits later suffixes but
// pins this exact original source and its four actual entries forever.
func openBaoTLSRootCRLProgressValid(current environmentCreateJournal, a createOpenBaoTLSRootCRLArtifact, source environmentCreateJournal, completed bool) bool {
	if len(current.Entries) < len(source.Entries) || a.Plan.SourceJournalSHA != deploymentplan.Digest(source) {
		return false
	}
	for i, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(current.Entries[i]) {
			return false
		}
	}
	normalized := current
	normalized.Entries = source.Entries
	normalized.KubernetesLeases = source.KubernetesLeases
	if deploymentplan.Digest(normalized) != deploymentplan.Digest(source) || len(current.KubernetesLeases) < len(source.KubernetesLeases) {
		return false
	}
	for i, lease := range source.KubernetesLeases {
		if lease != current.KubernetesLeases[i] {
			return false
		}
	}
	review := deploymentplan.Digest(a.Plan)
	leaseSHA := rootCRLLeasePlan(a.Plan.Configuration, review).SHA256()
	for _, lease := range current.KubernetesLeases[len(source.KubernetesLeases):] {
		if lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(lease.UID) || !strings.HasPrefix(lease.Holder, leaseSHA+":") {
			return false
		}
	}
	key := "openbao-tls-root-crl-renewal-" + review
	keys := []string{key, key + "/native-import", key + "/native-runtime-certissuer", key + "/native-runtime-pki-controller"}
	tail := current.Entries[len(source.Entries):]
	if len(tail) > 4 || completed && len(tail) != 4 || len(tail) >= 2 && len(current.KubernetesLeases) == len(source.KubernetesLeases) {
		return false
	}
	if len(tail) > 0 && tail[0].State != "pending" {
		if len(tail) != 4 {
			return false
		}
		for _, child := range tail[1:] {
			if child.State != "completed" {
				return false
			}
		}
	}
	intents := []any{a.Plan, openBaoTLSRootCRLImportIntent(a.Plan)}
	for _, target := range a.Plan.Consumers {
		intents = append(intents, openBaoTLSRootCRLRuntimeIntent(a.Plan, target))
	}
	if len(intents) != 4 {
		return false
	}
	for i, entry := range tail {
		if entry.Key != keys[i] || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intents[i]) || entry.StartedAt.Before(a.QualifiedAt) || i == 0 && !entry.StartedAt.Before(a.ValidUntil) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || completed && entry.State != "completed" || i > 1 && tail[i-1].State != "completed" || !entry.CompletedAt.IsZero() && entry.CompletedAt.Before(entry.StartedAt) {
			return false
		}
		if entry.State == "pending" {
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return false
			}
			continue
		}
		if len(entry.Receipt) == 0 || entry.State == "completed" && entry.CompletedAt.IsZero() || entry.State == "allocated" && !entry.CompletedAt.IsZero() {
			return false
		}
		if i == 0 {
			var receipt createOpenBaoTLSRootCRLReceipt
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Schema != createOpenBaoTLSRootCRLSchema || receipt.Parent != a.Plan.Parent || receipt.Review != review || receipt.Digest != a.Plan.Signed.Digest || receipt.File != filepath.ToSlash(filepath.Join(a.Plan.Candidate, "signed/revocations.pem")) || !receipt.Completed || !environmentPreflightDigest.MatchString(receipt.NativeEvidenceSHA) {
				return false
			}
		}
		if i == 1 && activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(map[string]any{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request)}) {
			return false
		}
		if i > 1 {
			var evidence map[string]string
			if json.Unmarshal(entry.Receipt, &evidence) != nil || len(evidence) != 4 {
				return false
			}
			target := a.Plan.Consumers[i-2]
			for _, k := range []string{"native-publication", target.ID + ":state", target.ID + ":owner", target.ID + ":native-ack"} {
				if !environmentPreflightDigest.MatchString(evidence[k]) {
					return false
				}
			}
			if evidence[target.ID+":owner"] != deploymentplan.Digest(target) {
				return false
			}
		}
	}
	return true
}

func openBaoTLSRootCRLImportAdmitted(current environmentCreateJournal, a createOpenBaoTLSRootCRLArtifact, source environmentCreateJournal) bool {
	return openBaoTLSRootCRLProgressValid(current, a, source, false) && len(current.Entries) >= len(source.Entries)+2 && current.Entries[len(source.Entries)+1].Key == "openbao-tls-root-crl-renewal-"+deploymentplan.Digest(a.Plan)+"/native-import"
}

// Shared native registry classifier hook: add ONLY this strict predicate beside
// existing typed checkpoints, not a generic completed/pending prefix exemption.
func openBaoTLSRootCRLNativeSourceCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil {
		return false
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "openbao-tls-root-crl-renewal-"
		if entry.Phase == "bootstrap" && strings.HasPrefix(entry.Key, prefix) && !strings.Contains(strings.TrimPrefix(entry.Key, prefix), "/") {
			if review != "" {
				return false
			}
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if !environmentPreflightDigest.MatchString(review) {
		return false
	}
	var saved createOpenBaoTLSRootCRLArtifact
	if readExecutionJSON(filepath.Join(store.Root, openBaoTLSRootCRLBase(review), "qualification.json"), &saved) != nil {
		return false
	}
	a, source, err := loadCreateOpenBaoTLSRootCRLArtifact(saved.Plan.Configuration, store, e, review)
	return err == nil && openBaoTLSRootCRLProgressValid(e.Journal, a, source, false)
}

func readCreateOpenBaoTLSRootCRLRecovery(store secretStore, parent string) (*createOpenBaoTLSRootCRLArtifact, *createOpenBaoTLSRootCRLCompletion, error) {
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(parent) {
		return nil, nil, openBaoTLSRootCRLError("selected-parent")
	}
	pointerRelative := filepath.Join("deployment/create/material", parent, "openbao-tls-root-crl-renewal.json")
	pointer, err := privateDeploymentStatePath(store.Environment, pointerRelative)
	if err != nil || pointer != filepath.Join(store.Root, pointerRelative) {
		return nil, nil, openBaoTLSRootCRLError("canonical-selected-pointer")
	}
	if _, err := os.Lstat(pointer); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	var completion createOpenBaoTLSRootCRLCompletion
	if createOpenBaoACKCompletionRead(store, pointerRelative, &completion) != nil || completion.Schema != createOpenBaoTLSRootCRLSchema+"/completion" || completion.Parent != parent || !completion.Completed || !environmentPreflightDigest.MatchString(completion.Review) {
		return nil, nil, openBaoTLSRootCRLError("strict-completed-pointer")
	}
	base := openBaoTLSRootCRLBase(completion.Review)
	var mirror createOpenBaoTLSRootCRLCompletion
	var saved createOpenBaoTLSRootCRLArtifact
	var owner environmentCreateOwner
	var final, current environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "completed.json"), &mirror) != nil || deploymentplan.Digest(mirror) != deploymentplan.Digest(completion) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &saved) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != parent || filepath.Clean(owner.Journal) != owner.Journal || filepath.IsAbs(owner.Journal) || !strings.HasPrefix(owner.Journal, "deployment/create/executions/"+parent+"/execution-") || filepath.Base(owner.Journal) != "journal.json" || createOpenBaoACKCompletionRead(store, filepath.Join(base, "final-journal.json"), &final) != nil || createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil {
		return nil, nil, openBaoTLSRootCRLError("completed-authenticated-mirror")
	}
	a, source, err := loadCreateOpenBaoTLSRootCRLArtifact(saved.Plan.Configuration, store, nil, completion.Review)
	if err != nil {
		return nil, nil, err
	}
	if completion.SourceEntryCount != len(source.Entries) || completion.FinalEntryCount != len(source.Entries)+4 || len(final.Entries) != completion.FinalEntryCount || completion.FinalJournalSHA != deploymentplan.Digest(final) || completion.FinalEntriesSHA != deploymentplan.Digest(final.Entries) || !openBaoTLSRootCRLProgressValid(final, a, source, true) || !createOpenBaoACKCompletionMembership(final, current, owner) {
		return nil, nil, openBaoTLSRootCRLError("immutable-final-monotonic-membership")
	}
	ownerPath := "deployment/.execution-active/owner.json"
	if !current.CompletedAt.IsZero() {
		ownerPath = filepath.Join(filepath.Dir(owner.Journal), "owner/owner.json")
	}
	var actualOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, ownerPath, &actualOwner) != nil || deploymentplan.Digest(actualOwner) != deploymentplan.Digest(owner) {
		return nil, nil, openBaoTLSRootCRLError("original-owner-active-or-archive")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, a.Plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return nil, nil, err
	}
	// Historical crypto proof is evaluated at its signed epoch; it grants no
	// current-time freshness. The live freshness helper below checks real now.
	signed, err := validateCreateRootCRLSignedForDomain(a.Plan.Root, a.Plan.Snapshot, a.Plan.Request, raw, "openbao_tls", a.Plan.Signed.ThisUpdate)
	if err != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, nil, openBaoTLSRootCRLError("historical-exact-signed-proof")
	}
	material, err := createOpenBaoTLSRootCRLMaterial(store, parent, a.Plan.Root)
	if err != nil || deploymentplan.Digest(material) != deploymentplan.Digest(a.Plan.Material) {
		return nil, nil, openBaoTLSRootCRLError("completed-original-key-custody")
	}
	var top createOpenBaoTLSRootCRLReceipt
	if json.Unmarshal(final.Entries[completion.SourceEntryCount].Receipt, &top) != nil || deploymentplan.Digest(top) != deploymentplan.Digest(completion.Receipt) || top.NativeEvidenceSHA != deploymentplan.Digest(completion.NativeEvidence) {
		return nil, nil, openBaoTLSRootCRLError("completed-native-receipt")
	}
	combined := map[string]string{}
	for _, entry := range final.Entries[completion.SourceEntryCount+2 : completion.FinalEntryCount] {
		var evidence map[string]string
		if json.Unmarshal(entry.Receipt, &evidence) != nil {
			return nil, nil, openBaoTLSRootCRLError("completed-native-runtime-shape")
		}
		for k, v := range evidence {
			if old, ok := combined[k]; ok && old != v {
				return nil, nil, openBaoTLSRootCRLError("native-publication-disagrees")
			}
			combined[k] = v
		}
	}
	if deploymentplan.Digest(combined) != deploymentplan.Digest(completion.NativeEvidence) {
		return nil, nil, openBaoTLSRootCRLError("actual-two-runtime-evidence")
	}
	return &a, &completion, nil
}

func openBaoTLSRootCRLRenewalOverrides(store secretStore, parent string) (map[string]deploymentplan.Resource, error) {
	_, _, err := readCreateOpenBaoTLSRootCRLRecovery(store, parent)
	if err != nil {
		return nil, err
	}
	return map[string]deploymentplan.Resource{}, nil
}

func verifyCreateOpenBaoTLSRootCRLFreshness(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, owners map[string]deploymentplan.Resource) (map[string]string, error) {
	a, completion, err := readCreateOpenBaoTLSRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil || a == nil || completion == nil {
		return nil, openBaoTLSRootCRLError("genuine-completed-root-two")
	}
	if _, err := parseCreateRootCRLForDomain(a.Plan.Root, a.Plan.Signed, "openbao_tls", true, time.Now().UTC()); err != nil {
		return nil, err
	}
	for _, resource := range []deploymentplan.Resource{a.Plan.ConfigMap, a.Plan.Controller} {
		original, ok := owners[resource.ID]
		if !ok || original.UID != resource.UID || deploymentplan.Digest(original.Desired) != deploymentplan.Digest(resource.Desired) {
			return nil, openBaoTLSRootCRLError("completed-same-owner-body")
		}
	}
	current, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, openBaoTLSRootCRLError("current-root-two-publication")
	}
	current.Prior = a.Plan.Snapshot.Prior
	if deploymentplan.Digest(current) != a.Plan.SnapshotSHA {
		return nil, openBaoTLSRootCRLError("genuine-revocation-inventory")
	}
	evidence := map[string]string{"openbao-tls-root-crl-proof": deploymentplan.Digest(completion), "openbao-tls-root-crl-native-publication": environmentPreflightBytesSHA256(native)}
	for _, target := range a.Plan.Consumers {
		original, ok := owners[target.ID]
		if !ok || original.UID != target.UID || deploymentplan.Digest(original.Desired) != deploymentplan.Digest(target.Desired) {
			return nil, openBaoTLSRootCRLError("accepted-consumer-owner")
		}
		observed, err := observeOpenBaoTLSRootCRLConsumer(ctx, cfg, store, e, b, *a, target)
		if err != nil {
			return nil, err
		}
		for k, v := range observed {
			evidence[k] = v
		}
	}
	return evidence, e.Check(ctx)
}

// Actual native output confirms the selected original image implements the
// explicit openbao_tls target. Unsupported binaries cannot produce a usable GO.
func openBaoTLSRootCRLNativeResultValid(raw []byte, plan createOpenBaoTLSRootCRLPlan, execute bool) bool {
	return validateOpenBaoTLSRootCRLNativeResult(raw, plan.Request.NextUpdate, deploymentplan.Digest(plan.Request), plan.Signed.Digest, execute)
}
