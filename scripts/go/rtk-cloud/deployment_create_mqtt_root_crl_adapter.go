// Same-original-Root publication repair. Qualification binds native observations
// and existing custody; execution admits only the exact four native steps.
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
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createMQTTRootCRLSchema = "rtk-cloud.create-mqtt-root-crl-recovery/v1"

var createMQTTRootCRLGates = []string{"deployment.mqtt-root-crl.original-authority", "deployment.mqtt-root-crl.live-revocations", "deployment.mqtt-root-crl.native-signature", "deployment.mqtt-root-crl.native-dry-run", "deployment.mqtt-root-crl.original-consumers", "deployment.mqtt-root-crl.execution-lease"}

type createMQTTRootCRLPlan struct {
	Schema, Parent, SourceOwnerSHA, SourceJournalSHA   string
	CheckerSHA, ConfigurationSHA, KubeconfigSHA, Image string
	Configuration                                      deploymentConfig
	Generation                                         environmentCreatePlan
	SourceArtifactSHA, SourceCoreSHA, SourceAppsSHA    string
	Root                                               environmentCreateRegisteredIssuer
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
	PairedPrior                                        *managedLatePairedCRLPriorEvidence
	ConsumerVolumes                                    map[string]environmentCreateVolumeOwner `json:",omitempty"`
	OriginalVolumes                                    map[string]environmentCreateVolumeOwner `json:",omitempty"`
}

type createMQTTRootCRLArtifact struct {
	Schema                  string
	Plan                    createMQTTRootCRLPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}

type createMQTTRootCRLReceipt struct {
	Schema, Parent, Review, File, Digest, NativeEvidenceSHA string
	Completed                                               bool
}

type createMQTTRootCRLCompletion struct {
	Schema, Parent, Review            string
	Completed                         bool
	SourceEntryCount, FinalEntryCount int
	FinalEntriesSHA, FinalJournalSHA  string
	NativeEvidence                    map[string]string
	Receipt                           createMQTTRootCRLReceipt
	CompletionReview                  string `json:",omitempty"`
}

type mqttRootCRLCandidate struct {
	Schema, Parent, SourceOwnerSHA, SourceJournalSHA, SnapshotSHA string
	Root                                                          environmentCreateRegisteredIssuer
	Snapshot                                                      createRootCRLNativeSnapshot
	Material                                                      map[string]string
	Request                                                       createRootCRLRequest
}

type mqttRootCRLACK struct {
	IssuerID string    `json:"issuer_id"`
	Digest   string    `json:"digest"`
	Consumer string    `json:"consumer_id"`
	At       time.Time `json:"acknowledged_at"`
}

func mqttRootCRLError(stage string) error {
	return createProviderRecoveryBlock("same original MQTT Root CRL requires actual accepted Core, exact live inventory and genuine native two-consumer proof: stage=" + stage)
}
func mqttRootCRLBase(review string) string {
	return filepath.Join("deployment/create/mqtt-root-crl-recovery", review)
}

// Scoped CRL qualification and execution still use the common configured
// readiness deadline. A short observer context bounds each read; it does not
// replace the backend's explicit rollout timeout or authorize readiness.
func newEnvironmentCreateCRLBackend(kubeconfig, environment string, values map[string]string) (environmentKubernetesBackend, error) {
	backend, err := newEnvironmentKubernetesBackend(kubeconfig, environment)
	if err != nil {
		return environmentKubernetesBackend{}, err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(values)
	if err != nil {
		return environmentKubernetesBackend{}, err
	}
	return backend, nil
}

func runEnvironmentCreateMQTTRootCRL(args []string) error {
	return runEnvironmentCreateMQTTRootCRLContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateMQTTRootCRLContext(parentContext context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-mqtt-root-crl", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	workspaceOpt := fs.String("workspace", "", "original logical workspace")
	execute := fs.Bool("execute", false, "execute exact admitted MQTT CRL publication")
	review := fs.String("review-sha256", "", "exact scoped review")
	confirm := fs.String("confirm", "", "exact original stack")
	timeout := fs.Duration("timeout", 15*time.Minute, "complete bounded operation")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return mqttRootCRLError("arguments")
	}
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-mqtt-root-crl"}, args...)) != nil {
		return mqttRootCRLError("mutation-guard")
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
		return mqttRootCRLError("original-operator")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return mqttRootCRLError("canonical-kubeconfig")
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
			return mqttRootCRLError("confirmation")
		}
		a, source, err := loadCreateMQTTRootCRLArtifact(cfg, store, e, *review)
		if err != nil {
			return err
		}
		if a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || a.Plan.Image != original.Plan.Plan.Bootstrap.Image {
			return mqttRootCRLError("qualified-inputs")
		}
		return executeCreateMQTTRootCRL(ctx, cfg, store, e, b, a, source, out)
	}
	p := createFoundationRecoveryPlan{Plan: original.Plan.Plan, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(cfg, store, e, &p, managedLateSessionMQTTRepair)
	if err != nil {
		return err
	}
	if r.MQTTRoot == nil || r.MQTTIntermediate == nil || late.AcceptedCore == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil {
		return mqttRootCRLError("actual-late-source")
	}
	// This mode proves every original native client/server/volume and current
	// accepted owner, while binding both exact original signed Root#1 priors.
	if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, &p, late, managedLateSessionMQTTRepair); err != nil {
		return err
	}
	// Keep paired historical inspection out of the signing selector. A previous
	// unused MQTT#2 request/signature remains the exact same local candidate even
	// when background ACKs for the same Root#1 advance their timestamps.
	originals, err := managedLatePairedOriginalRecords(store, e, r, managedLateSessionMQTTRepair)
	if err != nil {
		return err
	}
	records, _, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, r, managedLateObserveMQTTPrior, originals)
	if err != nil {
		return err
	}
	paired, err := observeManagedLatePairedCRLPrior(ctx, cfg, store, e, b, r, late, records)
	if err != nil {
		return err
	}
	p.Materials[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(paired)
	native := map[string]string{}
	for key, value := range p.Materials {
		native[key] = value
	}
	root := *r.MQTTRoot
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil || snapshot.Prior.Number != "1" {
		return mqttRootCRLError("original-prior-one")
	}
	material, err := createMQTTRootCRLMaterial(store, e.Journal.PlanSHA256, root)
	if err != nil {
		return err
	}
	candidate, request, signed, err := prepareMQTTRootCRLCandidate(ctx, store, e, p.Plan.Bootstrap.Image, root, snapshot, material)
	if err != nil {
		return err
	}
	controller, cm, targets, podName, podUID, err := loadMQTTRootCRLTargets(ctx, cfg, e, b, r, late, root, snapshot.Prior)
	if err != nil {
		return err
	}
	plan := createMQTTRootCRLPlan{Schema: createMQTTRootCRLSchema, Parent: e.Journal.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: deploymentplan.Digest(e.Journal), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), Configuration: cfg, Generation: original.Plan.Plan, KubeconfigSHA: environmentPreflightBytesSHA256(kube), Image: p.Plan.Bootstrap.Image, SourceArtifactSHA: late.SourceArtifactSHA, SourceCoreSHA: late.AcceptedCore.SHA256(), SourceAppsSHA: r.ManagedCorePartial.Apps.SHA256(), Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed, Candidate: candidate, Material: material, ConfigMap: cm, Controller: controller, ControllerPodName: podName, ControllerPodUID: podUID, Consumers: targets, NativeSourceEvidence: native, PairedPrior: paired}
	plan.ConsumerVolumes, plan.OriginalVolumes, err = readMQTTRootCRLVolumes(e, plan.Consumers, plan.NativeSourceEvidence, late.RestoreReview)
	if err != nil {
		return err
	}
	// The same pure source/targets guard used by execution must pass before GO.
	// Original cache claims belong to the recorded volume ledger, not necessarily
	// the later Apps26/Core16 resource sets.
	if _, _, err := validateMQTTRootCRLSourceAncestry(store, e, plan, &p); err != nil {
		return err
	}
	if _, err := runNativeCreateRootCRLImport(ctx, cfg, b, mqttRootCRLNativeImportPlan(plan), false); err != nil {
		return err
	}
	// Native dry-run creates no publication/ACK. Recheck the live snapshot and
	// all source/custody bytes after local signing and native dry-run.
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil || deploymentplan.Digest(fresh) != plan.SnapshotSHA {
		return mqttRootCRLError("post-dryrun-snapshot")
	}
	if err := verifyMQTTRootCRLInputs(ctx, cfg, store, e, b, plan, false); err != nil {
		return err
	}
	sha := deploymentplan.Digest(plan)
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, sha)); err != nil {
		return err
	}
	now := time.Now().UTC()
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range createMQTTRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
	}
	a := createMQTTRootCRLArtifact{Schema: createMQTTRootCRLSchema + "/artifact", Plan: plan, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	for name, value := range map[string]any{"source-journal.json": e.Journal, "source-owner.json": e.owner, "qualification.json": a} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(mqttRootCRLBase(sha), name), value); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-mqtt-root-crl review-sha256=%s crl-number=2 native-consumers=2\n", sha)
	return nil
}

func createMQTTRootCRLMaterial(store secretStore, parent string, root environmentCreateRegisteredIssuer) (map[string]string, error) {
	if root.Domain != "mqtt" || root.Environment != store.Environment || root.Kind != "root" || root.Status != "active" {
		return nil, mqttRootCRLError("original-mqtt-custody")
	}
	if !environmentPreflightDigest.MatchString(parent) || !environmentBootstrapOwnerUUID.MatchString(root.ID) {
		return nil, mqttRootCRLError("custody-parent")
	}
	paths := []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/mqtt-root-v1-key-passphrase", filepath.Join("deployment/create", parent, "bootstrap/mqtt/root-active.json")}
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

// Only stable signing inputs name the candidate; qualification-only paired
// observations belong to the outer review and never select another signature.
func mqttRootCRLCandidateSelector(parent, source string, root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, material map[string]string) string {
	return deploymentplan.Digest(deploymentplan.Object{"parent": parent, "source": source, "root": root, "snapshot": snapshot, "material": material})
}

func prepareMQTTRootCRLCandidate(ctx context.Context, store secretStore, e *environmentCreateExecution, image string, root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, material map[string]string) (string, createRootCRLRequest, createRootCRLRecord, error) {
	var request createRootCRLRequest
	var signed createRootCRLRecord
	selector := mqttRootCRLCandidateSelector(e.Journal.PlanSHA256, deploymentplan.Digest(e.Journal), root, snapshot, material)
	candidate := filepath.ToSlash(filepath.Join("deployment/repairs/mqtt-root-crl", selector))
	if err := prepareMQTTRootCRLPrivateParent(store, filepath.Join(store.Root, candidate)); err != nil {
		return candidate, request, signed, err
	}
	descriptorPath := filepath.Join(store.Root, candidate, "candidate.json")
	var descriptor mqttRootCRLCandidate
	if _, err := os.Lstat(descriptorPath); os.IsNotExist(err) {
		request, err = buildCreateRootCRLRequestForDomain(root, snapshot, "mqtt", time.Now().UTC())
		if err != nil {
			return candidate, request, signed, err
		}
		descriptor = mqttRootCRLCandidate{createMQTTRootCRLSchema + "/candidate", e.Journal.PlanSHA256, deploymentplan.Digest(e.owner), deploymentplan.Digest(e.Journal), deploymentplan.Digest(snapshot), root, snapshot, material, request}
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(candidate, "candidate.json"), descriptor); err != nil {
			return candidate, request, signed, err
		}
	} else if err != nil || readExecutionJSON(descriptorPath, &descriptor) != nil || descriptor.Schema != createMQTTRootCRLSchema+"/candidate" || descriptor.Parent != e.Journal.PlanSHA256 || descriptor.SourceOwnerSHA != deploymentplan.Digest(e.owner) || descriptor.SourceJournalSHA != deploymentplan.Digest(e.Journal) || descriptor.SnapshotSHA != deploymentplan.Digest(snapshot) || deploymentplan.Digest(descriptor.Root) != deploymentplan.Digest(root) || deploymentplan.Digest(descriptor.Material) != deploymentplan.Digest(material) {
		return candidate, request, signed, mqttRootCRLError("stable-candidate-source")
	}
	request = descriptor.Request
	if request.Number != "2" || request.IssuerID != root.ID || request.Fingerprint != root.RootFingerprint {
		return candidate, request, signed, mqttRootCRLError("stable-request")
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(candidate, "request.json"), request); err != nil {
		return candidate, request, signed, err
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(candidate, "issuer.json"), root.Document); err != nil {
		return candidate, request, signed, err
	}
	signedDir := filepath.Join(store.Root, candidate, "signed")
	if _, err := os.Lstat(signedDir); os.IsNotExist(err) {
		if err := e.Check(ctx); err != nil {
			return candidate, request, signed, err
		}
		if err := runEnvironmentCreatePKICeremony(ctx, store, image, "crl", "--issuer", "/store/"+candidate+"/issuer.json", "--crl-request", "/store/"+candidate+"/request.json", "--key", "/store/pki/custody/roots/"+root.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/mqtt-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(request), "--out", "/store/"+candidate+"/signed"); err != nil {
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
		return candidate, request, signed, mqttRootCRLError("incomplete-signed-candidate-preserve")
	}
	signed, err = validateCreateRootCRLSignedForDomain(root, snapshot, request, raw, "mqtt", time.Now().UTC())
	return candidate, request, signed, err
}

func mqttRootCRLNativeImportPlan(p createMQTTRootCRLPlan) createRootCRLRecoveryPlan {
	return createRootCRLRecoveryPlan{Root: p.Root, Snapshot: p.Snapshot, Request: p.Request, Signed: p.Signed, Targets: []createRootCRLStateTarget{{ID: p.Controller.ID, UID: p.Controller.UID, Container: "pki-controller", PodName: p.ControllerPodName, PodUID: p.ControllerPodUID, Desired: p.Controller.Desired}}}
}

func writeMQTTRootCRLImmutable(store secretStore, relative string, value any) error {
	raw := mustProviderRecoveryJSON(value)
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if err := prepareMQTTRootCRLPrivateParent(store, filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		old, readErr := readBoundedEnvironmentFile(path, true)
		if readErr != nil || !bytes.Equal(old, raw) {
			return mqttRootCRLError("immutable-artifact-conflict")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeEnvironmentCreateDerivedPrivateFile(store, relative, raw)
}

// Create only missing private parents. Existing mode/type mismatches fail;
// qualification never repairs directory permissions or follows a symlink.
func prepareMQTTRootCRLPrivateParent(store secretStore, directory string) error {
	relative, err := filepath.Rel(store.Root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !filepath.IsAbs(store.Root) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) || validateEnvironmentPreflightDirectory(store.ConfigRoot) != nil || validateEnvironmentPreflightDirectory(store.Root) != nil {
		return mqttRootCRLError("canonical-private-parent")
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
			return mqttRootCRLError("private-parent-mode-or-type")
		}
	}
	return nil
}

func loadMQTTRootCRLTargets(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, root environmentCreateRegisteredIssuer, prior createRootCRLRecord) (deploymentplan.Resource, deploymentplan.Resource, []createRootCRLStateTarget, string, string, error) {
	var controller, cm deploymentplan.Resource
	if r == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil || late == nil || late.AcceptedCore == nil {
		return controller, cm, nil, "", "", mqttRootCRLError("source-owner-plans")
	}
	apps := map[string]deploymentplan.Resource{}
	for _, resource := range r.ManagedCorePartial.Apps.Resources {
		resource.UID = r.ManagedCorePartial.AppsJournal.Owners[resource.ID]
		apps[resource.ID] = resource
	}
	for _, resource := range late.AcceptedCore.Resources {
		if resource.ID == "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller" {
			controller = resource
			controller.UID = late.AcceptedCoreJournal.Owners[resource.ID]
		}
	}
	cm = apps["ConfigMap/"+e.Journal.Stack+"-video-cloud/pki-create-mqtt-consumer-trust"]
	if controller.UID == "" || cm.UID == "" {
		return controller, cm, nil, "", "", mqttRootCRLError("accepted-controller/shared-CM")
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
					return "", "", mqttRootCRLError("one-current-owned-Pod")
				}
				name = deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
				uid = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			}
		}
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return "", "", mqttRootCRLError("owned-Pod-UID")
		}
		return name, uid, nil
	}
	cp, cu, err := ownedPod(controller.ID)
	if err != nil {
		return controller, cm, nil, "", "", err
	}
	var targets []createRootCRLStateTarget
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		id := "Deployment/" + e.Journal.Stack + "-video-cloud/" + name
		resource, ok := apps[id]
		if !ok || resource.UID == "" {
			return controller, cm, nil, "", "", mqttRootCRLError("accepted-native-consumer")
		}
		containers, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["containers"])
		if err != nil || containers["app"] == nil {
			return controller, cm, nil, "", "", mqttRootCRLError("native-app-container")
		}
		c := containers["app"]
		file, err := coreMQTTFile(resource.Desired, c, "mqtt-pki-crls", "crls.json")
		if err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-mqtt-consumer-trust" || file.file != mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST") {
			return controller, cm, nil, "", "", mqttRootCRLError("original-CRL-mount")
		}
		volumes, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["volumes"])
		if err != nil {
			return controller, cm, nil, "", "", err
		}
		claimName := deploymentplan.String(deploymentplan.Map(volumes["mqtt-bundle-state"]["persistentVolumeClaim"])["claimName"])
		claimID := "PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/" + claimName
		claim := byID[claimID]
		claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
		// The full strict native observer has independently bound this claim to
		// the original enrollment and volume ledger. Capture its exact UID here.
		if !environmentBootstrapOwnerUUID.MatchString(claimUID) {
			return controller, cm, nil, "", "", mqttRootCRLError("original-consumer-claim")
		}
		pod, puid, err := ownedPod(id)
		if err != nil {
			return controller, cm, nil, "", "", err
		}
		target := createRootCRLStateTarget{ID: id, UID: resource.UID, Desired: resource.Desired, Container: "app", PodName: pod, PodUID: puid, ClaimID: claimID, ClaimUID: claimUID, Paths: []string{"/var/lib/mqtt-trust/root-crl.json"}, StateSHA: map[string]string{}}
		state, err := readMQTTRootCRLState(ctx, cfg, b, target, root, prior)
		if err != nil {
			return controller, cm, nil, "", "", err
		}
		target.StateSHA[target.Paths[0]] = environmentPreflightBytesSHA256(state)
		targets = append(targets, target)
	}
	return controller, cm, targets, cp, cu, nil
}

func readMQTTRootCRLState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, root environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]byte, error) {
	if target.Container != "app" || len(target.Paths) != 1 || target.Paths[0] != "/var/lib/mqtt-trust/root-crl.json" || verifyCreateRootCRLTargetOwners(ctx, cfg, b, target) != nil {
		return nil, mqttRootCRLError("exact-consumer-state-owner")
	}
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", "app", "--", "/bin/sh", "-ec", `test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`, "mqtt-root-crl-native-state", target.Paths[0])
	var state struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		CRL         createRootCRLRecord `json:"crl"`
		Terminal    string              `json:"terminal_issuer_id"`
		Retained    json.RawMessage     `json:"retained_crl"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil || state.Fingerprint != root.RootFingerprint || state.Terminal != "" || len(state.Retained) != 0 || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(record) {
		return nil, mqttRootCRLError("genuine-signed-root-cache")
	}
	if _, err := parseCreateRootCRLForDomain(root, record, "mqtt", false, record.ThisUpdate); err != nil {
		return nil, err
	}
	return raw, nil
}

func verifyMQTTRootCRLInputs(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, plan createMQTTRootCRLPlan, afterImport bool) error {
	if e.Check(ctx) != nil || plan.Parent != e.Journal.PlanSHA256 || plan.SourceOwnerSHA != deploymentplan.Digest(e.owner) || plan.ConfigurationSHA != deploymentplan.Digest(cfg) || deploymentplan.Digest(plan.Configuration) != plan.ConfigurationSHA {
		return mqttRootCRLError("source-owner/configuration")
	}
	material, err := createMQTTRootCRLMaterial(store, plan.Parent, plan.Root)
	if err != nil || deploymentplan.Digest(material) != deploymentplan.Digest(plan.Material) {
		return mqttRootCRLError("unchanged-original-offline-custody")
	}
	for _, resource := range []deploymentplan.Resource{plan.ConfigMap, plan.Controller} {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return mqttRootCRLError("original-owner-" + resource.ID)
		}
	}
	for _, target := range plan.Consumers {
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, target); err != nil {
			return err
		}
	}
	controllerPod, err := b.Read(ctx, "Pod/"+e.Journal.Stack+"-video-cloud/"+plan.ControllerPodName)
	if err != nil || controllerPod == nil || deploymentplan.Map(controllerPod["metadata"])["uid"] != plan.ControllerPodUID {
		return mqttRootCRLError("original-native-importer-Pod")
	}
	var before environmentCreateJournal
	sourcePath := filepath.Join(store.Root, mqttRootCRLBase(deploymentplan.Digest(plan)), "source-journal.json")
	if _, err := os.Lstat(sourcePath); os.IsNotExist(err) {
		// During first qualification the immutable source file is not written
		// yet; only the exact unchanged original snapshot may be used.
		if plan.SourceJournalSHA != deploymentplan.Digest(e.Journal) {
			return mqttRootCRLError("source-snapshot")
		}
		before = e.Journal
	} else if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(sourcePath)) != nil || readExecutionJSON(sourcePath, &before) != nil {
		return mqttRootCRLError("canonical-source-snapshot")
	}
	old := *e
	old.Journal = before
	p := createFoundationRecoveryPlan{Plan: plan.Generation, Materials: map[string]string{}}
	r, late, err := validateMQTTRootCRLSourceAncestry(store, &old, plan, &p)
	if err != nil {
		return err
	}
	// The independently authenticated late descriptor selects current native
	// reads on the real MQTT progress; it never replays an ancestral Core loader.
	r.ManagedLateSession = late
	if !mqttRootCRLPairedEvidenceBound(plan) {
		return mqttRootCRLError("qualified-paired-original-prior-evidence")
	}
	if err := verifyManagedLatePairedCRLPrior(ctx, cfg, store, e, b, r, late, plan.PairedPrior, afterImport); err != nil {
		return err
	}
	if !afterImport {
		if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, &p, late, managedLateSessionMQTTRepair); err != nil {
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

func mqttRootCRLImportIntent(p createMQTTRootCRLPlan) map[string]string {
	return map[string]string{"issuer_id": p.Root.ID, "root_sha256": p.Root.RootFingerprint, "prior_crl_sha256": p.Snapshot.Prior.Digest, "signed_crl_sha256": p.Signed.Digest, "request_sha256": deploymentplan.Digest(p.Request)}
}
func mqttRootCRLRuntimeIntent(p createMQTTRootCRLPlan, target createRootCRLStateTarget) deploymentplan.Object {
	return deploymentplan.Object{"target": target, "native_crl_sha256": p.Signed.Digest}
}

func executeCreateMQTTRootCRL(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createMQTTRootCRLArtifact, source environmentCreateJournal, out io.Writer) (resultErr error) {
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	key := "mqtt-root-crl-renewal-" + review
	if !mqttRootCRLProgressValid(e.Journal, a, source, false) {
		return mqttRootCRLError("exact-admitted-tail")
	}
	admitted := len(e.Journal.Entries) > len(source.Entries) && e.Journal.Entries[len(source.Entries)].Key == key
	if !admitted && !time.Now().Before(a.ValidUntil) {
		return mqttRootCRLError("fresh-go-required")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return err
	}
	signed, err := validateCreateRootCRLSignedForDomain(plan.Root, plan.Snapshot, plan.Request, raw, "mqtt", time.Now().UTC())
	if err != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(plan.Signed) {
		return mqttRootCRLError("qualified-signed-bytes")
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil {
		return err
	}
	imported := deploymentplan.Digest(current.Prior) == deploymentplan.Digest(plan.Signed)
	if !mqttRootCRLSnapshotMatches(plan, current, mqttRootCRLImportAdmitted(e.Journal, a, source)) {
		return mqttRootCRLError("qualified-live-revocations-changed")
	}
	if err := verifyMQTTRootCRLInputs(ctx, cfg, store, e, b, plan, imported); err != nil {
		return err
	}
	// Once all four real native entries completed, a pointer-only publication
	// retry must not append a new Kubernetes lease or alter the immutable final
	// journal. Re-observe the same runtime evidence and publish its exact receipt.
	if mqttRootCRLProgressValid(e.Journal, a, source, true) {
		evidence := map[string]string{}
		for _, target := range plan.Consumers {
			observed, err := observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, a, target)
			if err != nil {
				return err
			}
			for key, value := range observed {
				if old, exists := evidence[key]; exists && old != value {
					return mqttRootCRLError("native-publication-disagrees")
				}
				evidence[key] = value
			}
		}
		return publishCreateMQTTRootCRLCompletion(ctx, store, e, a, source, evidence, out)
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
	if err := rootCRLStepBegin(e, importKey, mqttRootCRLImportIntent(plan)); err != nil {
		return err
	}
	if !imported {
		if err := guard(); err != nil {
			return err
		}
		if err := verifyMQTTRootCRLInputs(ctx, cfg, store, e, b, plan, false); err != nil {
			return err
		}
		result, err := runNativeCreateRootCRLImport(ctx, cfg, b, mqttRootCRLNativeImportPlan(plan), true)
		if err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(plan.Candidate, "native-import.log"), result); err != nil {
			return err
		}
	}
	current, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(plan.Signed) {
		return mqttRootCRLError("actual-native-publication")
	}
	if !mqttRootCRLSnapshotMatches(plan, current, true) {
		return mqttRootCRLError("native-revocation-inventory-changed")
	}
	if err := rootCRLStepComplete(e, importKey, map[string]any{"native_crl": plan.Signed, "request_sha256": deploymentplan.Digest(plan.Request)}); err != nil {
		return err
	}
	mqttRootCRLReportStep(out, "native-import-readback", importStarted)
	nativeEvidence := map[string]string{}
	for _, target := range plan.Consumers {
		consumerStarted := time.Now().UTC()
		name := deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
		step := key + "/native-runtime-" + name
		if err := rootCRLStepBegin(e, step, mqttRootCRLRuntimeIntent(plan, target)); err != nil {
			return err
		}
		var evidence map[string]string
		for {
			if err := guard(); err != nil {
				return err
			}
			evidence, err = observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, a, target)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return mqttRootCRLError("native-runtime-timeout-" + name)
			case <-time.After(2 * time.Second):
			}
		}
		for k, v := range evidence {
			if old, ok := nativeEvidence[k]; ok && old != v {
				return mqttRootCRLError("native-publication-disagrees")
			}
			nativeEvidence[k] = v
		}
		if err := rootCRLStepComplete(e, step, evidence); err != nil {
			return err
		}
		mqttRootCRLReportStep(out, "native-runtime-"+name, consumerStarted)
	}
	if err := guard(); err != nil {
		return err
	}
	if err := verifyMQTTRootCRLInputs(ctx, cfg, store, e, b, plan, true); err != nil {
		return err
	}
	// Re-read both installed caches and the exact native ACK roster immediately
	// before completion. Earlier successful observations grant no stale authority.
	for _, target := range plan.Consumers {
		observed, err := observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, a, target)
		if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(mqttRootCRLEvidenceForTarget(nativeEvidence, target)) {
			return mqttRootCRLError("final-two-current-consumers")
		}
	}
	receipt := createMQTTRootCRLReceipt{createMQTTRootCRLSchema, plan.Parent, review, filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), plan.Signed.Digest, deploymentplan.Digest(nativeEvidence), true}
	if err := rootCRLStepComplete(e, key, receipt); err != nil {
		return err
	}
	return publishCreateMQTTRootCRLCompletion(ctx, store, e, a, source, nativeEvidence, out)
}

func publishCreateMQTTRootCRLCompletion(ctx context.Context, store secretStore, e *environmentCreateExecution, a createMQTTRootCRLArtifact, source environmentCreateJournal, nativeEvidence map[string]string, out io.Writer, completionReview ...string) error {
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	if err := e.Check(ctx); err != nil {
		return err
	}
	if !mqttRootCRLProgressValid(e.Journal, a, source, true) {
		return mqttRootCRLError("actual-four-completed-steps")
	}
	receipt := createMQTTRootCRLReceipt{createMQTTRootCRLSchema, plan.Parent, review, filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), plan.Signed.Digest, deploymentplan.Digest(nativeEvidence), true}
	var actual createMQTTRootCRLReceipt
	if decodeEnvironmentJSON(e.Journal.Entries[len(source.Entries)].Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(receipt) {
		return mqttRootCRLError("completed-exact-top-receipt")
	}
	for i, target := range plan.Consumers {
		var actual map[string]string
		if decodeEnvironmentJSON(e.Journal.Entries[len(source.Entries)+2+i].Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(mqttRootCRLEvidenceForTarget(nativeEvidence, target)) {
			return mqttRootCRLError("completed-exact-runtime-receipt")
		}
	}
	completion := createMQTTRootCRLCompletion{Schema: createMQTTRootCRLSchema + "/completion", Parent: plan.Parent, Review: review, Completed: true, SourceEntryCount: len(source.Entries), FinalEntryCount: len(e.Journal.Entries), FinalEntriesSHA: deploymentplan.Digest(e.Journal.Entries), FinalJournalSHA: deploymentplan.Digest(e.Journal), NativeEvidence: nativeEvidence, Receipt: receipt}
	if len(completionReview) > 1 {
		return mqttRootCRLError("completion-only-review-count")
	}
	if len(completionReview) == 1 {
		completion.CompletionReview = completionReview[0]
		if err := validateMQTTRootCRLCompletionRecoveryProof(store, a, source, completion, e.Journal, e.owner); err != nil {
			return err
		}
	}
	base := mqttRootCRLBase(review)
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, "final-journal.json"), e.Journal); err != nil {
		return err
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, "completed.json"), completion); err != nil {
		return err
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join("deployment/create/material", plan.Parent, "mqtt-root-crl-renewal.json"), completion); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-mqtt-root-crl review-sha256=%s crl-number=2 native-consumers=2 original-identity-preserved=true\n", review)
	return nil
}

func mqttRootCRLReportStep(out io.Writer, stage string, started time.Time) {
	ended := time.Now().UTC()
	fmt.Fprintf(out, "PASS step=%s started=%s ended=%s duration=%.3fs\n", stage, started.Format(time.RFC3339Nano), ended.Format(time.RFC3339Nano), ended.Sub(started).Seconds())
}

func observeMQTTRootCRLConsumer(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createMQTTRootCRLArtifact, target createRootCRLStateTarget) (map[string]string, error) {
	p := a.Plan
	if _, err := parseCreateRootCRLForDomain(p.Root, p.Signed, "mqtt", true, time.Now().UTC()); err != nil {
		return nil, err
	}
	current, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(p.Signed) {
		return nil, mqttRootCRLError("native-current-two")
	}
	current.Prior = p.Snapshot.Prior
	if deploymentplan.Digest(current) != p.SnapshotSHA {
		return nil, mqttRootCRLError("current-genuine-inventory")
	}
	raw, err := readMQTTRootCRLState(ctx, cfg, b, target, p.Root, p.Signed)
	if err != nil {
		return nil, err
	}
	name := deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
	query := "SELECT json_build_object('imports',(SELECT count(*) FROM pki_audit WHERE issuer_id='" + p.Root.ID + "' AND event='crl_imported:" + p.Signed.Digest + "'),'imported_at',(SELECT min(created_at) FROM pki_audit WHERE issuer_id='" + p.Root.ID + "' AND event='crl_imported:" + p.Signed.Digest + "'),'acks',COALESCE((SELECT json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id) FROM pki_crl_acknowledgments WHERE issuer_id='" + p.Root.ID + "' AND digest='" + p.Signed.Digest + "'),'[]'::json))"
	ack, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var publication struct {
		Imports int              `json:"imports"`
		At      time.Time        `json:"imported_at"`
		ACKs    []mqttRootCRLACK `json:"acks"`
	}
	if err != nil || decodeEnvironmentJSON(ack, &publication) != nil || publication.Imports != 1 || publication.At.Before(p.Request.ThisUpdate) || !mqttRootCRLACKsValid(publication.ACKs, p.Root.ID, p.Signed.Digest, publication.At, time.Now().UTC()) || name != "video-cloud-api" && name != "video-cloud-logingester" {
		return nil, mqttRootCRLError("real-latest-consumer-ACK")
	}
	// The native protocol emits this ACK only after actual prepared state and
	// connection sweep. Disk equality alone is never an installation receipt.
	return map[string]string{"native-publication": environmentPreflightBytesSHA256(native), target.ID + ":state": environmentPreflightBytesSHA256(raw), target.ID + ":owner": deploymentplan.Digest(target), target.ID + ":native-ack": deploymentplan.Digest(publication)}, nil
}

func mqttRootCRLACKsValid(rows []mqttRootCRLACK, issuer, digest string, published, now time.Time) bool {
	if published.IsZero() || published.After(now) || len(rows) != 2 {
		return false
	}
	for i, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		row := rows[i]
		if row.IssuerID != issuer || row.Digest != digest || row.Consumer != name || row.At.IsZero() || row.At.Before(published) || row.At.After(now) {
			return false
		}
	}
	return true
}

func mqttRootCRLSnapshotMatches(plan createMQTTRootCRLPlan, current createRootCRLNativeSnapshot, admitted bool) bool {
	if deploymentplan.Digest(current.Prior) == deploymentplan.Digest(plan.Signed) {
		if !admitted {
			return false
		}
		current.Prior = plan.Snapshot.Prior
	}
	return deploymentplan.Digest(current) == plan.SnapshotSHA
}

func mqttRootCRLEvidenceForTarget(all map[string]string, target createRootCRLStateTarget) map[string]string {
	part := map[string]string{}
	for _, key := range []string{"native-publication", target.ID + ":state", target.ID + ":owner", target.ID + ":native-ack"} {
		part[key] = all[key]
	}
	return part
}

func loadCreateMQTTRootCRLArtifact(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, review string) (createMQTTRootCRLArtifact, environmentCreateJournal, error) {
	var a createMQTTRootCRLArtifact
	var source environmentCreateJournal
	var owner environmentCreateOwner
	base := filepath.Join(store.Root, mqttRootCRLBase(review))
	if !environmentPreflightDigest.MatchString(review) || validateEnvironmentPreflightDirectories(store, base) != nil || readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil {
		return a, source, mqttRootCRLError("canonical-artifact")
	}
	if a.Schema != createMQTTRootCRLSchema+"/artifact" || a.Plan.Schema != createMQTTRootCRLSchema || deploymentplan.Digest(a.Plan) != review || a.Plan.Parent != source.PlanSHA256 || a.Plan.SourceJournalSHA != deploymentplan.Digest(source) || a.Plan.SourceOwnerSHA != deploymentplan.Digest(owner) || a.Plan.ConfigurationSHA != deploymentplan.Digest(a.Plan.Configuration) || cfg.Environment != owner.Environment || store.Environment != owner.Environment || owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != a.Plan.Parent || owner.OwnerToken == "" || owner.OwnerToken != source.OwnerToken || owner.Stack != source.Stack || filepath.Clean(owner.Journal) != owner.Journal || filepath.IsAbs(owner.Journal) || !strings.HasPrefix(owner.Journal, "deployment/create/executions/"+a.Plan.Parent+"/execution-") || filepath.Base(owner.Journal) != "journal.json" || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.QualifiedAt.After(time.Now()) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createMQTTRootCRLGates) {
		return a, source, mqttRootCRLError("actual-qualified-source")
	}
	for i, id := range createMQTTRootCRLGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return a, source, mqttRootCRLError("required-scoped-gates")
		}
	}
	if a.Plan.Root.Domain != "mqtt" || a.Plan.Root.Kind != "root" || a.Plan.Snapshot.Prior.Number != "1" || a.Plan.Signed.Number != "2" || a.Plan.SnapshotSHA != deploymentplan.Digest(a.Plan.Snapshot) || len(a.Plan.Consumers) != 2 {
		return a, source, mqttRootCRLError("strict-original-mqtt-two")
	}
	selector := mqttRootCRLCandidateSelector(a.Plan.Parent, a.Plan.SourceJournalSHA, a.Plan.Root, a.Plan.Snapshot, a.Plan.Material)
	if a.Plan.Candidate != filepath.ToSlash(filepath.Join("deployment/repairs/mqtt-root-crl", selector)) || validateEnvironmentPreflightDirectories(store, filepath.Join(store.Root, a.Plan.Candidate)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Join(store.Root, a.Plan.Candidate, "signed")) != nil {
		return a, source, mqttRootCRLError("canonical-signed-candidate")
	}
	generation, configurationSHA, err := loadEnvironmentCreateAdmittedGeneration(store, a.Plan.Parent, source.ArtifactSHA256)
	if err != nil {
		return a, source, mqttRootCRLError("immutable-original-parent-read")
	}
	if !environmentCreateGenerationMatchesAdmitted(generation, a.Plan.Generation) {
		return a, source, mqttRootCRLError("immutable-original-parent-generation")
	}
	if configurationSHA != a.Plan.ConfigurationSHA {
		return a, source, mqttRootCRLError("immutable-original-parent-configuration")
	}
	if e != nil && (e.Journal.PlanSHA256 != a.Plan.Parent || deploymentplan.Digest(e.owner) != deploymentplan.Digest(owner) || e.Store.Root != store.Root) {
		return a, source, mqttRootCRLError("actual-original-fence")
	}
	old := &environmentCreateExecution{Store: store, owner: owner, Journal: source}
	if err := bindManagedLateRedisRecordedVolumeExecution(store, old); err != nil {
		return a, source, err
	}
	if _, _, err := validateMQTTRootCRLSourceAncestry(store, old, a.Plan, nil); err != nil {
		return a, source, err
	}
	return a, source, nil
}

// Resolve original claims from the canonical active18 ledger and the genuine
// restored-Core admission's per-volume aggregate. No later workload recipe is
// allowed to supply an inferred or newly observed original PVC UID.
func readMQTTRootCRLVolumes(e *environmentCreateExecution, targets []createRootCRLStateTarget, materials map[string]string, restoreReview string) (map[string]environmentCreateVolumeOwner, map[string]environmentCreateVolumeOwner, error) {
	ledger, err := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if err != nil || readExecutionJSON(filepath.Join(e.Store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return nil, nil, mqttRootCRLError("original-active-volume-ledger")
	}
	consumers, original, err := readOpenBaoTLSRootCRLVolumes(e, targets, materials, restoreReview)
	if err != nil {
		return nil, nil, mqttRootCRLError("original-accepted-Core-volume-authority")
	}
	return consumers, original, nil
}

func validateMQTTRootCRLSourceAncestry(store secretStore, source *environmentCreateExecution, plan createMQTTRootCRLPlan, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, *createManagedLateSessionRecovery, error) {
	if p == nil {
		p = &createFoundationRecoveryPlan{Plan: plan.Generation, Materials: map[string]string{}}
	}
	if p.Materials == nil {
		p.Materials = map[string]string{}
	}
	// Historical derivation authenticates the admitted Bootstrap DNS policy as
	// well as material pins. Retain the complete derived plan for native readers.
	r, late, err := loadManagedLateSessionSource(plan.Configuration, store, source, p, managedLateSessionMQTTRepair)
	if err != nil {
		return nil, nil, err
	}
	if r.MQTTRoot == nil || deploymentplan.Digest(*r.MQTTRoot) != deploymentplan.Digest(plan.Root) {
		return nil, nil, mqttRootCRLError("independent-original-root")
	}
	if late.SourceArtifactSHA != plan.SourceArtifactSHA || late.SourceSHA != plan.SourceJournalSHA {
		return nil, nil, mqttRootCRLError("independent-source-artifact")
	}
	if late.AcceptedCore == nil || late.AcceptedCore.SHA256() != plan.SourceCoreSHA {
		return nil, nil, mqttRootCRLError("independent-accepted-Core")
	}
	if r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil || r.ManagedCorePartial.Apps.SHA256() != plan.SourceAppsSHA {
		return nil, nil, mqttRootCRLError("independent-accepted-Apps")
	}
	consumers, original, err := readMQTTRootCRLVolumes(source, plan.Consumers, plan.NativeSourceEvidence, late.RestoreReview)
	if err != nil || deploymentplan.Digest(consumers) != deploymentplan.Digest(plan.ConsumerVolumes) || deploymentplan.Digest(original) != deploymentplan.Digest(plan.OriginalVolumes) {
		return nil, nil, mqttRootCRLError("independent-original-consumer-volumes")
	}
	if !mqttRootCRLAcceptedTargetsValid(plan, r, late) {
		return nil, nil, mqttRootCRLError("independent-accepted-consumer-targets")
	}
	if !mqttRootCRLPairedEvidenceBound(plan) || validateManagedLatePairedCRLPriorSource(store, source, r, late, plan.PairedPrior) != nil {
		return nil, nil, mqttRootCRLError("independent-paired-original-prior-ancestry")
	}
	return r, late, nil
}

// The full original-record/source reader and live guard independently validate
// this evidence. These additional cross-pins keep the MQTT signing snapshot,
// custody, consumers and qualified material attached to that same pair.
func mqttRootCRLPairedEvidenceBound(plan createMQTTRootCRLPlan) bool {
	paired := plan.PairedPrior
	if paired == nil || paired.Schema != managedLatePairedCRLPriorSchema || paired.SourceSHA != plan.SourceJournalSHA || !environmentPreflightDigest.MatchString(paired.CoreSourceSHA) || paired.AcceptedCoreSHA != plan.SourceCoreSHA || len(paired.Roots) != 2 || plan.NativeSourceEvidence[managedLatePairedCRLPriorMaterial] != deploymentplan.Digest(paired) {
		return false
	}
	mqtt, ok := paired.Roots[plan.Root.ID]
	return ok && plan.Root.Domain == "mqtt" && mqtt.RootID == plan.Root.ID && mqtt.RootSHA == plan.Root.RootFingerprint && deploymentplan.Digest(mqtt.OriginalRecord) == deploymentplan.Digest(plan.Snapshot.Prior) && mqtt.CustodySHA == deploymentplan.Digest(plan.Material) && mqtt.RevocationsSHA == deploymentplan.Digest(plan.Request.Entries) && deploymentplan.Digest(mqtt.Consumers) == deploymentplan.Digest(plan.Consumers)
}

func mqttRootCRLAcceptedTargetsValid(plan createMQTTRootCRLPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) bool {
	if r == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil || late == nil || late.AcceptedCore == nil || len(plan.Consumers) != 2 || len(plan.ConsumerVolumes) != 2 || len(plan.OriginalVolumes) != 18 || !environmentBootstrapOwnerUUID.MatchString(plan.ControllerPodUID) || plan.ControllerPodName == "" {
		return false
	}
	accepted := map[string]deploymentplan.Resource{}
	for _, resource := range r.ManagedCorePartial.Apps.Resources {
		resource.UID = r.ManagedCorePartial.AppsJournal.Owners[resource.ID]
		accepted[resource.ID] = resource
	}
	for _, resource := range late.AcceptedCore.Resources {
		resource.UID = late.AcceptedCoreJournal.Owners[resource.ID]
		if resource.ID == plan.Controller.ID && deploymentplan.Digest(resource) != deploymentplan.Digest(plan.Controller) {
			return false
		}
		accepted[resource.ID] = resource
	}
	volumeHashes := map[string]string{}
	for id, owner := range plan.OriginalVolumes {
		if id != "PersistentVolumeClaim/"+owner.Namespace+"/"+owner.PVCName || owner.PVName == "" || !environmentBootstrapOwnerUUID.MatchString(owner.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) {
			return false
		}
		if _, err := positiveIntValue("volume_id", owner.NativeID); err != nil {
			return false
		}
		volumeHashes[id] = deploymentplan.Digest(owner)
	}
	if deploymentplan.Digest(volumeHashes) != plan.NativeSourceEvidence["accepted-service-core-managed-complete-current-native-volumes"] {
		return false
	}
	stack := plan.Generation.Stack
	if plan.Controller.ID != "Deployment/"+stack+"-video-cloud/pki-controller" || deploymentplan.Digest(accepted[plan.Controller.ID]) != deploymentplan.Digest(plan.Controller) || plan.ConfigMap.ID != "ConfigMap/"+stack+"-video-cloud/pki-create-mqtt-consumer-trust" || deploymentplan.Digest(accepted[plan.ConfigMap.ID]) != deploymentplan.Digest(plan.ConfigMap) {
		return false
	}
	for i, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		target := plan.Consumers[i]
		resource := accepted["Deployment/"+stack+"-video-cloud/"+name]
		if target.ID != resource.ID || target.UID == "" || target.UID != resource.UID || deploymentplan.Digest(target.Desired) != deploymentplan.Digest(resource.Desired) || target.Container != "app" || !environmentBootstrapOwnerUUID.MatchString(target.PodUID) || target.PodName == "" || !environmentBootstrapOwnerUUID.MatchString(target.ClaimUID) || len(target.Paths) != 1 || target.Paths[0] != "/var/lib/mqtt-trust/root-crl.json" || len(target.StateSHA) != 1 || !environmentPreflightDigest.MatchString(target.StateSHA[target.Paths[0]]) {
			return false
		}
		volumes, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["volumes"])
		claim := deploymentplan.String(deploymentplan.Map(volumes["mqtt-bundle-state"]["persistentVolumeClaim"])["claimName"])
		owner, ok := plan.ConsumerVolumes[target.ClaimID]
		if err != nil || claim == "" || target.ClaimID != "PersistentVolumeClaim/"+stack+"-video-cloud/"+claim || !ok || owner.Namespace != stack+"-video-cloud" || owner.PVCName != claim || owner.PVCUID != target.ClaimUID || deploymentplan.Digest(owner) != deploymentplan.Digest(plan.OriginalVolumes[target.ClaimID]) {
			return false
		}
		// When a later accepted stage does include the claim, it must agree with
		// the original volume receipt rather than overriding its authority.
		if declared, ok := accepted[target.ClaimID]; ok && declared.UID != owner.PVCUID {
			return false
		}
	}
	return true
}

// Strict append-only scoped admission. Completion permits later suffixes but
// pins this exact original source and its four actual entries forever.
func mqttRootCRLProgressValid(current environmentCreateJournal, a createMQTTRootCRLArtifact, source environmentCreateJournal, completed bool) bool {
	return mqttRootCRLProgressFailure(current, a, source, completed) == ""
}

func mqttRootCRLProgressFailure(current environmentCreateJournal, a createMQTTRootCRLArtifact, source environmentCreateJournal, completed bool) string {
	if len(current.Entries) < len(source.Entries) || a.Plan.SourceJournalSHA != deploymentplan.Digest(source) {
		return "source"
	}
	for i, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(current.Entries[i]) {
			return "source-entry"
		}
	}
	normalized := current
	normalized.Entries = source.Entries
	normalized.KubernetesLeases = source.KubernetesLeases
	if deploymentplan.Digest(normalized) != deploymentplan.Digest(source) || len(current.KubernetesLeases) < len(source.KubernetesLeases) {
		return "source-envelope"
	}
	for i, lease := range source.KubernetesLeases {
		if lease != current.KubernetesLeases[i] {
			return "source-lease"
		}
	}
	review := deploymentplan.Digest(a.Plan)
	leaseSHA := rootCRLLeasePlan(a.Plan.Configuration, review).SHA256()
	for _, lease := range current.KubernetesLeases[len(source.KubernetesLeases):] {
		if lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(lease.UID) || !strings.HasPrefix(lease.Holder, leaseSHA+":") {
			return "scoped-lease"
		}
	}
	key := "mqtt-root-crl-renewal-" + review
	keys := []string{key, key + "/native-import", key + "/native-runtime-video-cloud-api", key + "/native-runtime-video-cloud-logingester"}
	tail := current.Entries[len(source.Entries):]
	if len(tail) > 4 || completed && len(tail) != 4 || len(tail) >= 2 && len(current.KubernetesLeases) == len(source.KubernetesLeases) {
		return "tail-shape"
	}
	if len(tail) > 0 && tail[0].State != "pending" {
		if len(tail) != 4 {
			return "parent-state"
		}
		for _, child := range tail[1:] {
			if child.State != "completed" {
				return "parent-child-state"
			}
		}
	}
	intents := []any{a.Plan, mqttRootCRLImportIntent(a.Plan)}
	for _, target := range a.Plan.Consumers {
		intents = append(intents, mqttRootCRLRuntimeIntent(a.Plan, target))
	}
	if len(intents) != 4 {
		return "intent-count"
	}
	for i, entry := range tail {
		if entry.Key != keys[i] || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intents[i]) || entry.StartedAt.Before(a.QualifiedAt) || i == 0 && !entry.StartedAt.Before(a.ValidUntil) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || completed && entry.State != "completed" || i > 1 && tail[i-1].State != "completed" || !entry.CompletedAt.IsZero() && entry.CompletedAt.Before(entry.StartedAt) {
			return "entry"
		}
		if entry.State == "pending" {
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return "pending-entry"
			}
			continue
		}
		if len(entry.Receipt) == 0 || entry.State == "completed" && entry.CompletedAt.IsZero() || entry.State == "allocated" && !entry.CompletedAt.IsZero() {
			return "recorded-entry"
		}
		if i == 0 {
			var receipt createMQTTRootCRLReceipt
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Schema != createMQTTRootCRLSchema || receipt.Parent != a.Plan.Parent || receipt.Review != review || receipt.Digest != a.Plan.Signed.Digest || receipt.File != filepath.ToSlash(filepath.Join(a.Plan.Candidate, "signed/revocations.pem")) || !receipt.Completed || !environmentPreflightDigest.MatchString(receipt.NativeEvidenceSHA) {
				return "parent-receipt"
			}
		}
		if i == 1 && activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(map[string]any{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request)}) {
			return "import-receipt"
		}
		if i > 1 {
			var evidence map[string]string
			if json.Unmarshal(entry.Receipt, &evidence) != nil || len(evidence) != 4 {
				return "consumer-receipt"
			}
			target := a.Plan.Consumers[i-2]
			for _, k := range []string{"native-publication", target.ID + ":state", target.ID + ":owner", target.ID + ":native-ack"} {
				if !environmentPreflightDigest.MatchString(evidence[k]) {
					return "consumer-evidence"
				}
			}
			if evidence[target.ID+":owner"] != deploymentplan.Digest(target) {
				return "consumer-owner"
			}
		}
	}
	return ""
}

func mqttRootCRLImportAdmitted(current environmentCreateJournal, a createMQTTRootCRLArtifact, source environmentCreateJournal) bool {
	return mqttRootCRLProgressValid(current, a, source, false) && len(current.Entries) >= len(source.Entries)+2 && current.Entries[len(source.Entries)+1].Key == "mqtt-root-crl-renewal-"+deploymentplan.Digest(a.Plan)+"/native-import"
}

// Shared native registry classifier hook: add ONLY this strict predicate beside
// existing typed checkpoints, not a generic completed/pending prefix exemption.
func mqttRootCRLNativeSourceCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil {
		return false
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "mqtt-root-crl-renewal-"
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
	var saved createMQTTRootCRLArtifact
	if readExecutionJSON(filepath.Join(store.Root, mqttRootCRLBase(review), "qualification.json"), &saved) != nil {
		return false
	}
	a, source, err := loadCreateMQTTRootCRLArtifact(saved.Plan.Configuration, store, e, review)
	return err == nil && mqttRootCRLProgressValid(e.Journal, a, source, false)
}

func readCreateMQTTRootCRLRecovery(store secretStore, parent string) (*createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion, error) {
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(parent) {
		return nil, nil, mqttRootCRLError("selected-parent")
	}
	pointerRelative := filepath.Join("deployment/create/material", parent, "mqtt-root-crl-renewal.json")
	pointer, err := privateDeploymentStatePath(store.Environment, pointerRelative)
	if err != nil || pointer != filepath.Join(store.Root, pointerRelative) {
		return nil, nil, mqttRootCRLError("canonical-selected-pointer")
	}
	if _, err := os.Lstat(pointer); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	var completion createMQTTRootCRLCompletion
	if createOpenBaoACKCompletionRead(store, pointerRelative, &completion) != nil || completion.Schema != createMQTTRootCRLSchema+"/completion" || completion.Parent != parent || !completion.Completed || !environmentPreflightDigest.MatchString(completion.Review) {
		return nil, nil, mqttRootCRLError("strict-completed-pointer")
	}
	base := mqttRootCRLBase(completion.Review)
	var mirror createMQTTRootCRLCompletion
	var saved createMQTTRootCRLArtifact
	var owner environmentCreateOwner
	var final, current environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "completed.json"), &mirror) != nil || deploymentplan.Digest(mirror) != deploymentplan.Digest(completion) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &saved) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != parent || filepath.Clean(owner.Journal) != owner.Journal || filepath.IsAbs(owner.Journal) || !strings.HasPrefix(owner.Journal, "deployment/create/executions/"+parent+"/execution-") || filepath.Base(owner.Journal) != "journal.json" || createOpenBaoACKCompletionRead(store, filepath.Join(base, "final-journal.json"), &final) != nil || createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil {
		return nil, nil, mqttRootCRLError("completed-authenticated-mirror")
	}
	a, source, err := loadCreateMQTTRootCRLArtifact(saved.Plan.Configuration, store, nil, completion.Review)
	if err != nil {
		return nil, nil, err
	}
	if completion.SourceEntryCount != len(source.Entries) || completion.FinalEntryCount != len(source.Entries)+4 || len(final.Entries) != completion.FinalEntryCount || completion.FinalJournalSHA != deploymentplan.Digest(final) || completion.FinalEntriesSHA != deploymentplan.Digest(final.Entries) || !mqttRootCRLProgressValid(final, a, source, true) || !createOpenBaoACKCompletionMembership(final, current, owner) {
		return nil, nil, mqttRootCRLError("immutable-final-monotonic-membership")
	}
	if completion.CompletionReview != "" {
		if err := validateMQTTRootCRLCompletionRecoveryProof(store, a, source, completion, final, owner); err != nil {
			return nil, nil, err
		}
	}
	ownerPath := "deployment/.execution-active/owner.json"
	if !current.CompletedAt.IsZero() {
		ownerPath = filepath.Join(filepath.Dir(owner.Journal), "owner/owner.json")
	}
	var actualOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, ownerPath, &actualOwner) != nil || deploymentplan.Digest(actualOwner) != deploymentplan.Digest(owner) {
		return nil, nil, mqttRootCRLError("original-owner-active-or-archive")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, a.Plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return nil, nil, err
	}
	// Historical crypto proof is evaluated at its signed epoch; it grants no
	// current-time freshness. The live freshness helper below checks real now.
	signed, err := validateCreateRootCRLSignedForDomain(a.Plan.Root, a.Plan.Snapshot, a.Plan.Request, raw, "mqtt", a.Plan.Signed.ThisUpdate)
	if err != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, nil, mqttRootCRLError("historical-exact-signed-proof")
	}
	material, err := createMQTTRootCRLMaterial(store, parent, a.Plan.Root)
	if err != nil || deploymentplan.Digest(material) != deploymentplan.Digest(a.Plan.Material) {
		return nil, nil, mqttRootCRLError("completed-original-key-custody")
	}
	var top createMQTTRootCRLReceipt
	if json.Unmarshal(final.Entries[completion.SourceEntryCount].Receipt, &top) != nil || deploymentplan.Digest(top) != deploymentplan.Digest(completion.Receipt) || top.NativeEvidenceSHA != deploymentplan.Digest(completion.NativeEvidence) {
		return nil, nil, mqttRootCRLError("completed-native-receipt")
	}
	combined := map[string]string{}
	for _, entry := range final.Entries[completion.SourceEntryCount+2 : completion.FinalEntryCount] {
		var evidence map[string]string
		if json.Unmarshal(entry.Receipt, &evidence) != nil {
			return nil, nil, mqttRootCRLError("completed-native-runtime-shape")
		}
		for k, v := range evidence {
			if old, ok := combined[k]; ok && old != v {
				return nil, nil, mqttRootCRLError("native-publication-disagrees")
			}
			combined[k] = v
		}
	}
	if deploymentplan.Digest(combined) != deploymentplan.Digest(completion.NativeEvidence) {
		return nil, nil, mqttRootCRLError("actual-two-runtime-evidence")
	}
	return &a, &completion, nil
}

func mqttRootCRLRenewalOverrides(store secretStore, parent string) (map[string]deploymentplan.Resource, error) {
	_, _, err := readCreateMQTTRootCRLRecovery(store, parent)
	if err != nil {
		return nil, err
	}
	return map[string]deploymentplan.Resource{}, nil
}

func verifyCreateMQTTRootCRLFreshness(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, owners map[string]deploymentplan.Resource) (map[string]string, error) {
	a, completion, err := readCreateMQTTRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil || a == nil || completion == nil {
		return nil, mqttRootCRLError("genuine-completed-root-two")
	}
	if _, err := parseCreateRootCRLForDomain(a.Plan.Root, a.Plan.Signed, "mqtt", true, time.Now().UTC()); err != nil {
		return nil, err
	}
	for _, resource := range []deploymentplan.Resource{a.Plan.ConfigMap, a.Plan.Controller} {
		original, ok := owners[resource.ID]
		if !ok || original.UID != resource.UID || deploymentplan.Digest(original.Desired) != deploymentplan.Digest(resource.Desired) {
			return nil, mqttRootCRLError("completed-same-owner-body")
		}
	}
	current, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, mqttRootCRLError("current-root-two-publication")
	}
	current.Prior = a.Plan.Snapshot.Prior
	if deploymentplan.Digest(current) != a.Plan.SnapshotSHA {
		return nil, mqttRootCRLError("genuine-revocation-inventory")
	}
	evidence := map[string]string{"mqtt-root-crl-proof": deploymentplan.Digest(completion), "mqtt-root-crl-native-publication": environmentPreflightBytesSHA256(native)}
	for _, target := range a.Plan.Consumers {
		original, ok := owners[target.ID]
		if !ok || original.UID != target.UID || deploymentplan.Digest(original.Desired) != deploymentplan.Digest(target.Desired) {
			return nil, mqttRootCRLError("accepted-consumer-owner")
		}
		observed, err := observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, *a, target)
		if err != nil {
			return nil, err
		}
		for k, v := range observed {
			evidence[k] = v
		}
	}
	return evidence, e.Check(ctx)
}
