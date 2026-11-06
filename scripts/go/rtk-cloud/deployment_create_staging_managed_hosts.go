package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingManagedHostsStage = "transport-managed-hosts"

type stagingManagedHostsForward struct {
	Original             string
	Definitions          []deploymentplan.Definition
	Roots                map[string]environmentCreateRegisteredIssuer
	Intermediates        map[string]environmentCreateRegisteredIssuer
	AlreadyAccepted      bool
	ServerForward        *stagingServerEnrollmentForward
	ApplicationsForward  *stagingApplicationsForward
	ApplicationsAccepted bool
}

func readStagingManagedHostsTransport(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (environmentCreateTransport, string, error) {
	var empty environmentCreateTransport
	bad := createProviderRecoveryBlock("original renewed staging temporary transport is unavailable")
	parent := e.Journal.PlanSHA256
	base := filepath.Join("deployment/create", parent, "service-authorization-recovery/temporary-transport")
	var renewed map[string]environmentCreateTransport
	if readExecutionJSON(filepath.Join(store.Root, base, "candidate.json"), &renewed) != nil {
		return empty, "", bad
	}
	path := filepath.Join(store.Root, base, "openbao-ca.crt")
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || string(raw) != renewed["openbao"].CACert {
		return empty, "", bad
	}
	p, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stagingTransportMaterialStage)
	if err != nil || len(p.Resources) != 10 {
		return empty, "", bad
	}
	for _, r := range p.Resources {
		desired, ok := initialTransportOverride(r.Desired, renewed)
		if !ok || deploymentplan.Digest(desired) != deploymentplan.Digest(r.Desired) {
			return empty, "", bad
		}
		current, err := b.Read(ctx, r.ID)
		if err != nil || deploymentplan.Map(current["metadata"])["uid"] != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, current) {
			return empty, "", bad
		}
	}
	if _, _, err := acceptedCreateStage(store, e, "kubernetes/"+stagingTransportReloadStage); err != nil {
		return empty, "", err
	}
	if !stagingManagedHostsAccepted(e.Journal) {
		if err := verifyActiveTransportMounted(ctx, cfg, b, renewed["openbao"], renewed["openbao"], false); err != nil {
			return empty, "", err
		}
	}
	return renewed["management"], path, nil
}

func qualifyStagingManagedHostsForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime) (*stagingManagedHostsForward, error) {
	bad := createProviderRecoveryBlock("original staging managed hosts require their exact unwritten admission and accepted enrollments")
	entries := e.Journal.Entries
	last := len(entries) - 1
	managedAccepted := stagingManagedHostsAccepted(e.Journal)
	expected := "kubernetes/" + stagingManagedHostsStage
	if managedAccepted {
		expected = "kubernetes/server-enrollment-video-cloud-api-pki"
		if last >= 0 && entries[last].Key == "kubernetes/"+stagingApplicationsStage {
			expected = entries[last].Key
		}
	}
	applications := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingApplicationsStage)
	applicationsAccepted := applications != nil && applications.State == "completed"
	if applicationsAccepted {
		if !managedAccepted {
			return nil, bad
		}
		if err := verifyStagingAcceptedApplications(store, e); err != nil {
			return nil, err
		}
	} else {
		if last < 2 || entries[last].Key != expected || entries[last].State != "pending" {
			return nil, bad
		}
		for _, entry := range entries[:last] {
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return nil, bad
			}
		}
	}
	original, err := readEnvironmentCreateStageInput(store, e, stagingManagedHostsStage)
	if err != nil || original == nil || len(original.Resources) != 7 || original.ConfigurationSHA256 != parent.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return nil, bad
	}
	f := &stagingManagedHostsForward{Original: original.SHA256(), AlreadyAccepted: managedAccepted, ApplicationsAccepted: applicationsAccepted, Roots: map[string]environmentCreateRegisteredIssuer{}, Intermediates: map[string]environmentCreateRegisteredIssuer{}}
	if !applicationsAccepted && expected == "kubernetes/"+stagingApplicationsStage {
		f.ApplicationsForward, err = qualifyStagingApplicationsForward(ctx, cfg, store, parent, e, runtime)
		if err != nil {
			return nil, err
		}
	} else if !applicationsAccepted && managedAccepted {
		f.ServerForward, err = qualifyStagingServerEnrollmentForward(ctx, cfg, store, parent, e, runtime)
		if err != nil {
			return nil, err
		}
	} else if !applicationsAccepted {
		if entries[last-1].Key != "kubernetes/transport-server-enrollment" || entries[last-2].Key != "kubernetes/transport-enrollment-issuer" {
			return nil, bad
		}
		if _, written, err := readEnvironmentCreateStageProgress(store, e, stagingManagedHostsStage, original); err != nil || written {
			return nil, bad
		}
		servers, serverJournal, err := acceptedCreateStage(store, e, "kubernetes/transport-server-enrollment")
		if err != nil {
			return nil, err
		}
		if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, runtime.Backend, servers, serverJournal); err != nil {
			return nil, err
		}
		id := "StatefulSet/" + parent.Stack + "-secrets/openbao"
		live, err := runtime.Backend.Read(ctx, id)
		foundation, owners, foundationErr := acceptedCreateStage(store, e, "kubernetes/foundation")
		if err != nil || foundationErr != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != owners.Owners[id] {
			return nil, bad
		}
		retained := false
		for _, resource := range foundation.Resources {
			if resource.ID == id && deploymentplan.Matches(resource.Desired, live) {
				retained = true
			}
		}
		if !retained {
			return nil, bad
		}
		defs := environmentCreateDefinitions(original)
		if err := bindOpenBaoAllocations(defs, []deploymentplan.Object{live}); err != nil {
			return nil, err
		}
		for _, d := range defs {
			if deploymentplan.ID(d.Object) == id {
				deploymentplan.Map(d.Object["metadata"])["uid"] = owners.Owners[id]
			}
		}
		f.Definitions = defs
	}
	for _, domain := range managedPKIDomains {
		var doc map[string]map[string]any
		if readExecutionJSON(filepath.Join(store.Root, "pki", domain, "issuer.json"), &doc) != nil {
			return nil, bad
		}
		root, err := environmentCreateRegisteredIssuerFromMap(doc["root"], "")
		if err != nil {
			return nil, err
		}
		actual, err := readStagingResumeIssuer(ctx, cfg, runtime, root.ID)
		if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(root.Document) {
			return nil, bad
		}
		f.Roots[domain] = root
		if domain == "device" {
			continue
		}
		issuer, err := environmentCreateRegisteredIssuerFromMap(doc["intermediate"], root.RootFingerprint)
		if err != nil {
			return nil, err
		}
		actual, err = readStagingResumeIssuer(ctx, cfg, runtime, issuer.ID)
		if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(issuer.Document) {
			return nil, bad
		}
		f.Intermediates[domain] = issuer
	}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, f.Roots, f.Intermediates, nil); err != nil {
		return nil, err
	}
	return f, e.Check(ctx)
}

func executeStagingManagedHostsForward(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, recovery *createServiceAuthorizationRecovery, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	var empty environmentIdentityBindings
	f := recovery.ManagedHosts
	service := *recovery.Enrollment
	if deploymentplan.Digest(service.Root.Document) != deploymentplan.Digest(f.Roots["service"].Document) ||
		deploymentplan.Digest(service.Intermediate.Document) != deploymentplan.Digest(f.Intermediates["service"].Document) {
		return empty, createProviderRecoveryBlock("original Service ceremony differs from the active published lineage")
	}
	var err error
	runtime.InitialListeners, err = configureCreateBrokerServiceConsumer(p, runtime.InitialListeners)
	if err != nil {
		return empty, err
	}
	runtime.ServiceCeremony = &service
	if f.ApplicationsForward != nil {
		if err := executeStagingApplicationsForward(ctx, runtime, f.ApplicationsForward, apply); err != nil {
			return empty, err
		}
	}
	if f.ApplicationsAccepted {
		bindings, restored, err := restoreStagingBoundBootstrap(ctx, cfg, p, release, store, runtime)
		if err != nil || restored {
			return bindings, err
		}
	}
	if f.ApplicationsForward != nil || f.ApplicationsAccepted {
		return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, f.Roots, f.Intermediates, apply)
	}
	applications, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, p, release, store, operator, service)
	if err != nil {
		return empty, err
	}
	applications, err = configureEnvironmentCreateMQTTConsumers(p, service, f.Roots["mqtt"], []environmentCreateRegisteredIssuer{f.Intermediates["mqtt"]}, applications)
	if err != nil {
		return empty, err
	}
	applications, err = configureEnvironmentCreateDeviceConsumers(cfg, p, f.Roots["device"], applications)
	if err != nil {
		return empty, err
	}
	applications, err = configureEnvironmentCreateAppConsumers(p, f.Roots["app"], []environmentCreateRegisteredIssuer{f.Intermediates["app"]}, applications)
	if err != nil {
		return empty, err
	}
	if !f.AlreadyAccepted {
		stage := stagingManagedHostsStage + "-retained-claims-" + f.Original[:12]
		if err := apply(ctx, stage, f.Definitions); err != nil {
			return empty, err
		}
		key := "kubernetes/" + stagingManagedHostsStage
		receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": f.Original})
		if err := runtime.Execution.RecordReceipt(key, receipt); err != nil {
			return empty, err
		}
		if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return empty, err
		}
		fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.managed-hosts original-claims-retained")
	}
	return finishEnvironmentCreateAfterManagedTransports(ctx, cfg, p, release, store, operator, runtime, service, f.Roots, f.Intermediates, applications, apply)
}

func stagingManagedHostsAccepted(j environmentCreateJournal) bool {
	entry := createOpenBaoACKCompletionEntry(j, "kubernetes/"+stagingManagedHostsStage)
	return entry != nil && entry.State == "completed" && !entry.CompletedAt.IsZero()
}

func verifyStagingManagedOpenBao(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) error {
	bad := createProviderRecoveryBlock("original managed OpenBao owner, admitted cutover or actual health differs")
	original, err := readEnvironmentCreateStageInput(store, e, stagingManagedHostsStage)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingManagedHostsStage)
	if err != nil || original == nil || entry == nil {
		return bad
	}
	stage := stagingManagedHostsStage + "-retained-claims-" + original.SHA256()[:12]
	var receipt map[string]string
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt["actual_qualified_forward_stage"] != "kubernetes/"+stage || receipt["original_failed_plan_sha256"] != original.SHA256() {
		return bad
	}
	accepted, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil {
		return err
	}
	foundation, foundationJournal, err := acceptedCreateStage(store, e, "kubernetes/foundation")
	if err != nil {
		return err
	}
	ns := foundation.Stack + "-secrets"
	id := "StatefulSet/" + ns + "/openbao"
	current, err := b.Read(ctx, id)
	pod, podErr := b.Read(ctx, "Pod/"+ns+"/openbao-0")
	uid := foundationJournal.Owners[id]
	if err != nil || podErr != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || uid != journal.Owners[id] ||
		deploymentplan.Map(current["metadata"])["uid"] != uid || validateOpenBaoPodOwner(current, pod) != nil {
		return bad
	}
	matches := false
	for _, resource := range accepted.Resources {
		if resource.ID == id && deploymentplan.Matches(resource.Desired, current) {
			matches = true
		}
	}
	if !matches {
		forward, j, err := readStagingWorkloadAllocationCheckpoint(store, e)
		if err != nil || forward == nil || j.Owners[id] != uid {
			return bad
		}
		for _, resource := range forward.Resources {
			if resource.ID == id && deploymentplan.Matches(resource.Desired, current) {
				matches = true
			}
		}
	}
	if matches {
		status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, b)
		if err == nil && status.Initialized && !status.Sealed {
			return e.Check(ctx)
		}
	}
	return bad
}

type stagingServerEnrollmentForward struct {
	Stage, Original, FailedUID string
	Definition                 deploymentplan.Definition
}

func qualifyStagingServerEnrollmentForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime) (*stagingServerEnrollmentForward, error) {
	const owner = "video-cloud-api-pki"
	stage := "server-enrollment-" + owner
	bad := createProviderRecoveryBlock("original staging API enrollment lacks verified unstarted volume failure")
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
		return nil, bad
	}
	original, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || original == nil || len(original.Resources) != 1 || original.ConfigurationSHA256 != parent.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return nil, bad
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, original)
	resource := original.Resources[0]
	uid := journal.Owners[resource.ID]
	if err != nil || !exists || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != resource.ID || len(journal.Completed) != 0 || len(journal.Owners) != 1 || !environmentBootstrapOwnerUUID.MatchString(uid) || resource.Desired["kind"] != "Job" {
		return nil, bad
	}
	ns := parent.Stack + "-video-cloud"
	meta := deploymentplan.Map(resource.Desired["metadata"])
	name := deploymentplan.String(meta["name"])
	job, err := runtime.Backend.Read(ctx, resource.ID)
	if err != nil || meta["namespace"] != ns || !strings.HasPrefix(name, "pki-enroll-"+owner+"-server-") ||
		deploymentplan.Map(job["metadata"])["uid"] != uid || !deploymentplan.Matches(resource.Desired, job) || !stagingEnrollmentJobDeadlineExceeded(job) {
		return nil, bad
	}
	claimName := "video-cloud-api-pki-controller-identity"
	claimID := "PersistentVolumeClaim/" + ns + "/" + claimName
	_, claimJournal, err := acceptedCreateStage(store, e, "kubernetes/application-client-enrollment")
	claim, claimErr := runtime.Backend.Read(ctx, claimID)
	volume := deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"])
	claimUID := claimJournal.Owners[claimID]
	if err != nil || claimErr != nil || volume == "" || !environmentBootstrapOwnerUUID.MatchString(claimUID) || deploymentplan.Map(claim["metadata"])["uid"] != claimUID {
		return nil, bad
	}
	if err := verifyStagingEnrollmentAttachmentFailure(ctx, runtime.Backend, ns, name, uid, volume, owner); err != nil {
		return nil, err
	}
	object := deploymentplan.Clone(resource.Desired)
	pod := environmentPKIPod(object)
	volumes, err := environmentPKINamedFields(pod["volumes"])
	containers := certIssuerObjectList(pod["containers"])
	if err != nil || deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"] != claimName || pod["affinity"] != nil || len(containers) != 1 {
		return nil, bad
	}
	request := mqttFoundationEnv(deploymentplan.Map(containers[0]), "PKI_SERVER_BOOTSTRAP_REQUEST_ID")
	session := strings.TrimSuffix(request, ":"+owner+":server")
	if !environmentBootstrapOwnerUUID.MatchString(session) || request != session+":"+owner+":server" {
		return nil, bad
	}
	query := "SELECT json_build_object('count',count(*)) FROM pki_server_issuances WHERE environment='staging' AND request_id='" + request + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var result struct {
		Count *int `json:"count"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &result) != nil || result.Count == nil || *result.Count != 0 {
		return nil, bad
	}
	deploymentplan.Map(object["metadata"])["name"] = name + "-rwo"
	bindCreateSameNodePlacement(pod, ns, owner)
	return &stagingServerEnrollmentForward{Stage: stage, Original: original.SHA256(), FailedUID: uid, Definition: deploymentplan.Definition{Object: object, Phase: resource.Phase}}, nil
}

func executeStagingServerEnrollmentForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, f *stagingServerEnrollmentForward) error {
	stage := f.Stage + "-rwo-" + f.Original[:12]
	if err := applyEnvironmentCreateStage(ctx, cfg, store, parent, e, stage, []deploymentplan.Definition{f.Definition}, os.Stdout); err != nil {
		return err
	}
	key := "kubernetes/" + f.Stage
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": f.Original, "original_failed_job_uid": f.FailedUID})
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
