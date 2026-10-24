package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These original owners can have genuine native installation ACKs predating
// the later five/eight-ACK recovery profiles. Accepted intent proves only the
// permitted subject; the fresh native row proves which subjects actually ACKed.
func managedLateResidualHistoricalResourceSubject(resource deploymentplan.Resource, session, stack, environment, root string) string {
	if !environmentBootstrapOwnerUUID.MatchString(session) || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || deploymentplan.ID(resource.Desired) != resource.ID {
		return ""
	}
	pod := environmentPKIPod(resource.Desired)
	containers := certIssuerObjectList(pod["containers"])
	var container deploymentplan.Object
	if resource.ID == "Deployment/"+stack+"-account-manager/account-manager" {
		fields, err := environmentPKINamedFields(pod["containers"])
		if err != nil || fields["pkimanagement"] == nil {
			return ""
		}
		container = fields["pkimanagement"]
	} else if len(containers) == 1 {
		container = deploymentplan.Map(containers[0])
	} else {
		return ""
	}
	fields, err := environmentPKINamedFields(container["env"])
	if err != nil || deploymentplan.String(fields["PKI_ENVIRONMENT"]["value"]) != environment {
		return ""
	}
	if resource.Desired["kind"] == "Deployment" && deploymentplan.String(fields["PKI_BOOTSTRAP_SESSION_ID"]["value"]) == session && fields["PKI_BOOTSTRAP_SESSION_ID"]["valueFrom"] == nil {
		for _, owner := range []struct{ namespace, name string }{{stack + "-account-manager", "account-manager"}, {stack + "-video-cloud", "certissuer"}, {stack + "-video-cloud", "pki-controller"}} {
			if resource.ID == "Deployment/"+owner.namespace+"/"+owner.name {
				return "service:" + owner.name
			}
		}
	}
	if resource.Desired["kind"] != "Job" || deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["namespace"]) != stack+"-video-cloud" || deploymentplan.String(fields["PKI_CLIENT_ROOT_SHA256"]["value"]) != root || fields["PKI_CLIENT_ROOT_SHA256"]["valueFrom"] != nil || deploymentplan.String(fields["PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT"]["value"]) != "/run/pki-bootstrap/tls.crt" || deploymentplan.String(fields["PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY"]["value"]) != "/run/pki-bootstrap/tls.key" {
		return ""
	}
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Name != "video-cloud-api" && owner.Name != "video-cloud-logingester" || resource.ID != "Job/"+stack+"-video-cloud/pki-enroll-"+owner.Name+"-client-"+strings.ReplaceAll(session, "-", "")[:12] || deploymentplan.String(fields["PKI_CLIENT_IDENTITY_STATE"]["value"]) != owner.ClientState || len(certIssuerObjectList(container["args"])) != 0 {
			continue
		}
		direct := []any{"/app/serviceidentity-bootstrap", "service:" + owner.Role}
		guarded := []any{"/bin/sh", "-ec", "test -s " + owner.ClientState + " && grep -q '\"current\"' " + owner.ClientState + " && ! grep -q '\"pending\"' " + owner.ClientState + " && /app/serviceidentity-bootstrap service:" + owner.Role}
		if deploymentplan.Digest(container["command"]) != deploymentplan.Digest(direct) && deploymentplan.Digest(container["command"]) != deploymentplan.Digest(guarded) {
			continue
		}
		volumes, volumeErr := environmentPKINamedFields(pod["volumes"])
		mounts, mountErr := environmentPKINamedFields(container["volumeMounts"])
		if volumeErr == nil && mountErr == nil && deploymentplan.Map(volumes["bootstrap"]["secret"])["secretName"] == "service-bootstrap-"+session && mounts["bootstrap"]["mountPath"] == "/run/pki-bootstrap" && mounts["bootstrap"]["readOnly"] == true && deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"] == owner.Claim {
			return "service:" + owner.Role
		}
	}
	return ""
}

func managedLateResidualAcceptedHistoricalSubjects(store secretStore, e *environmentCreateExecution, session string, current environmentCreateServiceCeremony) (map[string]bool, error) {
	if e == nil || len(e.Journal.Entries) < 400 || !environmentBootstrapOwnerUUID.MatchString(session) {
		return nil, managedHistoricalSeedBlockAt("residual-history-runtime")
	}
	out := map[string]bool{}
	for _, entry := range e.Journal.Entries[:400] {
		if !strings.HasPrefix(entry.Key, "kubernetes/") || entry.State != "completed" {
			continue
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil {
			continue
		}
		candidate := false
		for id := range receipt.Owners {
			if id == "Deployment/"+e.Journal.Stack+"-account-manager/account-manager" || id == "Deployment/"+e.Journal.Stack+"-video-cloud/certissuer" || id == "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller" || strings.HasPrefix(id, "Job/"+e.Journal.Stack+"-video-cloud/pki-enroll-") && strings.HasSuffix(id, "-"+strings.ReplaceAll(session, "-", "")[:12]) {
				candidate = true
			}
		}
		if !candidate {
			continue
		}
		plan, journal, err := managedTransportPostAcceptedStage(store, e, entry.Key)
		if err != nil {
			return nil, err
		}
		for _, resource := range plan.Resources {
			if receipt.Owners[resource.ID] != journal.Owners[resource.ID] {
				return nil, managedHistoricalSeedBlockAt("residual-history-owner")
			}
			resource.UID = journal.Owners[resource.ID]
			if subject := managedLateResidualHistoricalResourceSubject(resource, session, e.Journal.Stack, e.Journal.Environment, current.Root.RootFingerprint); subject != "" {
				out[subject] = true
			}
		}
	}
	return out, nil
}

func managedLateResidualInstalledHistoryMatches(r *createServiceSessionRecovery, observed createServiceSessionObserved, current environmentCreateServiceCeremony, state managedSessionState, subjects []string, accepted map[string]bool, now time.Time) bool {
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil || r.ManagedLateRemaining.StaleSeedsSHA != deploymentplan.Digest(r.StaleSeeds) || !managedLateResidualCleanupApprovalValid(r.ManagedLateRemaining.ResidualCleanup.Approval) || state.Session == current.BootstrapSessionID || state.Session != observed.Cleanup.SessionID || state.Status != "expired" || state.Expires.IsZero() || state.Expires.After(now) || state.OtherActive != 0 || state.Issuer != current.Intermediate.ID || state.Root != current.Root.RootFingerprint || !environmentPreflightDigest.MatchString(state.Fingerprint) || len(subjects) != 9 || !slices.Equal(state.Subjects, subjects) || len(state.Installed) == 0 || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, current.Root.Environment) != nil {
		return false
	}
	var saved managedSessionState
	if decodeEnvironmentJSON(observed.Session, &saved) != nil || saved.Session != state.Session || saved.Issuer != state.Issuer || saved.Root != state.Root || saved.Fingerprint != state.Fingerprint || !saved.Expires.Equal(state.Expires) || !slices.Equal(saved.Subjects, subjects) || saved.Status != "active" && saved.Status != "expired" {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(subjects, subject) || !accepted[subject] {
			return false
		}
		seen[subject] = true
	}
	for _, subject := range saved.Installed {
		if !seen[subject] {
			return false
		}
	}
	count := 0
	for _, source := range r.StaleSeeds {
		if source.Cleanup.SessionID == state.Session {
			if deploymentplan.Digest(source) != deploymentplan.Digest(observed) {
				return false
			}
			count++
		}
	}
	if count != 1 || len(observed.UIDs) != 3 {
		return false
	}
	count = 0
	for _, object := range r.ManagedLateRemaining.ResidualCleanup.Approval.Objects {
		if object.SessionID == state.Session {
			if observed.UIDs[object.Namespace] != object.UID || object.ResourceID != "Secret/"+object.Namespace+"/service-bootstrap-"+state.Session {
				return false
			}
			count++
		}
	}
	return count == 3
}

// Called only after the ordinary Late verifier has freshly verified native
// finalization and all eleven preserved clients, then declined this old SID.
func verifyManagedLateResidualInstalledHistory(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony, observed createServiceSessionObserved) (bool, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil {
		return false, nil
	}
	if err := verifyManagedLateResidualCleanupRuntime(ctx, cfg, store, runtime); err != nil {
		return false, err
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return false, err
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, observed.Cleanup.SessionID)
	if err != nil {
		return false, err
	}
	accepted, err := managedLateResidualAcceptedHistoricalSubjects(store, runtime.Execution, state.Session, current)
	if err != nil {
		return false, err
	}
	if !managedLateResidualInstalledHistoryMatches(r, observed, current, state, subjects, accepted, time.Now().UTC()) {
		return false, managedHistoricalSeedBlockAt("residual-history-match")
	}
	return true, runtime.Execution.Check(ctx)
}
