package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateAppRootCRLSourceOwners(r *createServiceSessionRecovery, plan *createAppRootCRLPlan) (map[string]deploymentplan.Resource, error) {
	if r == nil || r.ManagedLateSession == nil || r.ManagedLatePostMint == nil || r.ManagedCorePartial == nil || plan == nil || plan.PostSource.SourceSHA != plan.SourceJournalSHA || plan.PostSource.SourceOwnerSHA != plan.SourceOwnerSHA || managedLatePostMintDigest(&plan.PostSource) != managedLatePostMintDigest(r.ManagedLatePostMint) {
		return nil, appRootCRLError("authenticated-post-mint-owners")
	}
	late := r.ManagedLateSession
	owners := map[string]deploymentplan.Resource{}
	for _, source := range []struct {
		plan    *deploymentplan.Plan
		journal deploymentplan.Journal
	}{{r.ManagedCorePartial.Apps, r.ManagedCorePartial.AppsJournal}, {late.AcceptedCore, late.AcceptedCoreJournal}} {
		if !createOpenBaoACKCompletedChild(source.plan, source.journal) {
			return nil, appRootCRLError("accepted-original-plans")
		}
		for _, resource := range source.plan.Resources {
			resource.UID = source.journal.Owners[resource.ID]
			owners[resource.ID] = resource
		}
	}
	for id, resource := range late.Owners {
		owners[id] = resource
	}
	return owners, nil
}

func managedLateAppRootCRLTargetBinding(root, ica environmentCreateRegisteredIssuer, cm deploymentplan.Resource, resource deploymentplan.Resource) (createRootCRLStateTarget, error) {
	var target createRootCRLStateTarget
	stack := root.Environment
	if stack != "dev" || root.ID != managedLateAppRootID || root.Domain != "app" || root.Kind != "root" || ica.Domain != "app" || ica.Kind != "intermediate" || ica.RootFingerprint != root.RootFingerprint {
		return target, appRootCRLError("original-app-authorities")
	}
	ns := "video-cloud-" + stack + "-video-cloud"
	name := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])
	container, key, path, claim := "app", "api-crls.json", "/run/pki-state/app-root-crl.json", "video-cloud-api-app-pki-trust"
	if name == "mqtt-pki" {
		container, key, path, claim = "pkibroker", "broker-crls.json", "/run/pki-state/app-crl.json", "mqtt-pki-trust"
	} else if name != "video-cloud-api-app-pki" {
		return target, appRootCRLError("exact-two-consumers")
	}
	if resource.ID != "Deployment/"+ns+"/"+name || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || cm.ID != "ConfigMap/"+ns+"/pki-create-app-trust" || !environmentBootstrapOwnerUUID.MatchString(cm.UID) {
		return target, appRootCRLError("original-consumer-and-CM-owner")
	}
	c := managedNamedField(environmentPKIPod(resource.Desired)["containers"], container)
	if c == nil {
		return target, appRootCRLError("original-consumer-container")
	}
	file, err := coreMQTTFile(resource.Desired, c, "pki-app", key)
	setting := "VIDEO_CLOUD_AUTH_CRL_MANIFEST"
	if container == "pkibroker" {
		setting = "PKI_BROKER_APP_CRL_MANIFEST"
	}
	if err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-app-trust" || file.file != "/run/pki-app/"+key || mqttFoundationEnv(c, setting) != file.file {
		return target, appRootCRLError("unchanged-app-manifest-mount")
	}
	volumes, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["volumes"])
	mounts, mountErr := environmentPKINamedFields(c["volumeMounts"])
	if err != nil || mountErr != nil || deploymentplan.Map(volumes["pki-state"]["persistentVolumeClaim"])["claimName"] != claim || mounts["pki-state"]["mountPath"] != "/run/pki-state" || mounts["pki-state"]["subPath"] != nil || mounts["pki-state"]["readOnly"] == true {
		return target, appRootCRLError("original-writable-app-cache-claim")
	}
	paths := []string{path, strings.TrimSuffix(path, ".json") + "-" + ica.ID + ".json"}
	var entries []struct {
		Issuer deploymentplan.Object `json:"issuer"`
		Path   string                `json:"state_path"`
	}
	raw := deploymentplan.String(deploymentplan.Map(cm.Desired["data"])[key])
	if decodeEnvironmentJSON([]byte(raw), &entries) != nil || len(entries) != 2 {
		return target, appRootCRLError("exact-reviewed-app-manifest")
	}
	for index, expected := range []environmentCreateRegisteredIssuer{root, ica} {
		issuer, err := environmentCreateRegisteredIssuerFromMap(entries[index].Issuer, root.RootFingerprint)
		if err != nil || entries[index].Path != paths[index] || issuer.ID != expected.ID || issuer.Environment != expected.Environment || issuer.Domain != expected.Domain || issuer.Kind != expected.Kind || issuer.Certificate != expected.Certificate || issuer.ChainPEM != expected.ChainPEM || issuer.RootFingerprint != expected.RootFingerprint || issuer.BundleVersion != expected.BundleVersion || issuer.Status != "ready" && issuer.Status != "active" {
			return target, appRootCRLError("immutable-app-manifest-authority")
		}
	}
	if strings.TrimSpace(deploymentplan.String(deploymentplan.Map(cm.Desired["data"])["roots.pem"])) != strings.TrimSpace(root.Certificate) {
		return target, appRootCRLError("original-app-trust-root")
	}
	return createRootCRLStateTarget{ID: resource.ID, UID: resource.UID, Desired: deploymentplan.Clone(resource.Desired), Container: container, ClaimID: "PersistentVolumeClaim/" + ns + "/" + claim, Paths: paths, StateSHA: map[string]string{}}, nil
}

func managedLateAppRootCRLInventoryTarget(objects []deploymentplan.Object, target *createRootCRLStateTarget, requireReady bool) error {
	byID := map[string]deploymentplan.Object{}
	for _, object := range objects {
		id := deploymentplan.ID(object)
		if byID[id] != nil {
			return appRootCRLError("duplicate-native-object")
		}
		byID[id] = object
	}
	actual, claim := byID[target.ID], byID[target.ClaimID]
	if actual == nil || claim == nil || deploymentplan.Map(actual["metadata"])["uid"] != target.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(target.Desired, actual) || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(claim["status"])["phase"] != "Bound" {
		return appRootCRLError("current-original-consumer-and-claim")
	}
	claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
	if !environmentBootstrapOwnerUUID.MatchString(claimUID) || target.ClaimUID != "" && target.ClaimUID != claimUID {
		return appRootCRLError("original-claim-UID")
	}
	var selected deploymentplan.Object
	for _, object := range objects {
		if object["kind"] != "Pod" {
			continue
		}
		owner, err := environmentPodController(object, byID)
		if err != nil || owner != target.ID {
			continue
		}
		if selected != nil || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			return appRootCRLError("sole-original-owned-Pod")
		}
		selected = object
	}
	if selected == nil {
		return appRootCRLError("sole-original-owned-Pod")
	}
	meta := deploymentplan.Map(selected["metadata"])
	uid, name := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["name"])
	if !environmentBootstrapOwnerUUID.MatchString(uid) || name == "" || target.PodUID != "" && (uid != target.PodUID || name != target.PodName) {
		return appRootCRLError("original-Pod-UID")
	}
	expected := environmentPKIPod(target.Desired)
	if !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": expected["containers"], "initContainers": expected["initContainers"]}}, selected) {
		return appRootCRLError("original-Pod-containers")
	}
	if requireReady {
		if !initialAccountPodReady(selected) {
			return appRootCRLError("current-consumer-ready")
		}
	}
	target.ClaimUID, target.PodUID, target.PodName = claimUID, uid, name
	return nil
}

// A stale App Root may make the genuine process unready. It still cannot
// authorize a replacement Pod, image, missing status or unverified imageID.
func verifyManagedLateAppRootCRLPodImages(ctx context.Context, target createRootCRLStateTarget, objects []deploymentplan.Object, imageDigests func(context.Context, string) (map[string]bool, error)) error {
	var pod deploymentplan.Object
	for _, object := range objects {
		if object["kind"] == "Pod" && deploymentplan.Map(object["metadata"])["uid"] == target.PodUID {
			if pod != nil {
				return appRootCRLError("duplicate-consumer-Pod")
			}
			pod = object
		}
	}
	if pod == nil || imageDigests == nil {
		return appRootCRLError("original-consumer-runtime-images")
	}
	cache := map[string]map[string]bool{}
	for _, field := range []string{"containers", "initContainers"} {
		declared := certIssuerObjectList(environmentPKIPod(target.Desired)[field])
		statuses := certIssuerObjectList(deploymentplan.Map(pod["status"])[map[string]string{"containers": "containerStatuses", "initContainers": "initContainerStatuses"}[field]])
		if len(declared) != len(statuses) || field == "containers" && len(declared) == 0 {
			return appRootCRLError("complete-runtime-image-status")
		}
		seen := map[string]bool{}
		for _, value := range declared {
			c := deploymentplan.Map(value)
			name, image := deploymentplan.String(c["name"]), deploymentplan.String(c["image"])
			if name == "" || seen[name] || !immutableEnvironmentImage.MatchString(image) {
				return appRootCRLError("original-immutable-consumer-images")
			}
			seen[name] = true
			var status deploymentplan.Object
			for _, value := range statuses {
				current := deploymentplan.Map(value)
				if current["name"] == name {
					if status != nil {
						return appRootCRLError("duplicate-runtime-status")
					}
					status = current
				}
			}
			if status == nil || deploymentplan.String(status["containerID"]) == "" {
				return appRootCRLError("original-running-image-status")
			}
			if cache[image] == nil {
				accepted, err := imageDigests(ctx, image)
				if err != nil || len(accepted) == 0 {
					return appRootCRLError("approved-runtime-image-proof")
				}
				cache[image] = accepted
			}
			imageID := deploymentplan.String(status["imageID"])
			_, digest, found := strings.Cut(imageID, "@")
			if !found {
				_, digest, found = strings.Cut(imageID, "://")
				if !found {
					digest = imageID
				}
			}
			if !deploymentImageDigestPattern.MatchString(digest) || !cache[image][digest] {
				return appRootCRLError("original-runtime-imageID")
			}
		}
	}
	return ctx.Err()
}

func loadManagedLateAppRootCRLTargets(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, plan *createAppRootCRLPlan) error {
	owners, err := managedLateAppRootCRLSourceOwners(r, plan)
	if err != nil {
		return err
	}
	ns := e.Journal.Stack + "-video-cloud"
	plan.Controller, plan.ConfigMap = owners["Deployment/"+ns+"/pki-controller"], owners["ConfigMap/"+ns+"/pki-create-app-trust"]
	if !environmentBootstrapOwnerUUID.MatchString(plan.Controller.UID) || !environmentBootstrapOwnerUUID.MatchString(plan.ConfigMap.UID) {
		return appRootCRLError("accepted-controller-and-trust-CM")
	}
	controller := managedNamedField(environmentPKIPod(plan.Controller.Desired)["containers"], "pki-controller")
	consumers := strings.Split(mqttFoundationEnv(controller, "PKI_REQUIRED_CONSUMERS_APP"), ",")
	slices.Sort(consumers)
	if !slices.Equal(consumers, []string{"pkibroker", "video-cloud-api"}) {
		return appRootCRLError("original-two-App-consumer-policy")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	for _, resource := range []deploymentplan.Resource{plan.Controller, plan.ConfigMap} {
		found := false
		for _, actual := range objects {
			if deploymentplan.ID(actual) == resource.ID {
				if found || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
					return appRootCRLError("unchanged-controller-and-trust-CM")
				}
				found = true
			}
		}
		if !found {
			return appRootCRLError("unchanged-controller-and-trust-CM")
		}
	}
	var targets []createRootCRLStateTarget
	for _, name := range []string{"video-cloud-api-app-pki", "mqtt-pki"} {
		target, err := managedLateAppRootCRLTargetBinding(plan.Root, plan.Intermediate, plan.ConfigMap, owners["Deployment/"+ns+"/"+name])
		if err != nil {
			return err
		}
		if err := managedLateAppRootCRLInventoryTarget(objects, &target, false); err != nil {
			return err
		}
		if err := verifyManagedLateAppRootCRLPodImages(ctx, target, objects, func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		}); err != nil {
			return err
		}
		for i, issuer := range []environmentCreateRegisteredIssuer{plan.Root, plan.Intermediate} {
			record := plan.Snapshot.Prior
			if i == 1 {
				record = plan.IntermediateCRL
			}
			raw, err := readManagedLateAppRootCRLCache(ctx, cfg, b, target, target.Paths[i], issuer, record, i == 0)
			if err != nil {
				return err
			}
			target.StateSHA[target.Paths[i]] = environmentPreflightBytesSHA256(raw)
		}
		targets = append(targets, target)
	}
	plan.Consumers = targets
	return nil
}

func readManagedLateAppRootCRLCache(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, path string, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord, historical bool) ([]byte, error) {
	if len(target.Paths) != 2 || !slices.Contains(target.Paths, path) || verifyCreateRootCRLTargetOwners(ctx, cfg, b, target) != nil {
		return nil, appRootCRLError("exact-native-cache-owner")
	}
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", `test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`, "app-root-crl-native-state", path)
	if err != nil || validateManagedLateAppRootCRLCache(raw, issuer, record, historical, time.Now().UTC()) != nil {
		return nil, appRootCRLError("signed-native-app-cache")
	}
	return raw, nil
}

func validateManagedLateAppRootCRLCache(raw []byte, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord, historical bool, now time.Time) error {
	var state struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		CRL         createRootCRLRecord `json:"crl"`
		Terminal    string              `json:"terminal_issuer_id"`
		Retained    json.RawMessage     `json:"retained_crl"`
	}
	if len(raw) > 1<<20 || decodeEnvironmentJSON(raw, &state) != nil || state.Fingerprint != issuer.RootFingerprint && issuer.Kind == "root" || state.Terminal != "" || len(state.Retained) != 0 || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(record) {
		return appRootCRLError("exact-original-app-cache")
	}
	cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil || cert == nil || state.Fingerprint != environmentPreflightBytesSHA256(cert.Raw) {
		return appRootCRLError("app-cache-certificate-pin")
	}
	clock := now
	if historical {
		if issuer.Kind != "root" || issuer.ID != managedLateAppRootID || record.Number != "1" || record.Digest != managedLateAppRootPriorSHA {
			return appRootCRLError("only-original-AppRoot1-prior")
		}
		clock = record.ThisUpdate
	}
	_, err = validateManagedLateSignedCRL("app", issuer.ID, state.Fingerprint, issuer.Status, issuer.ChainPEM, managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}, managedLateObserveACKReady, clock)
	return err
}

func observeManagedLateAppRootCRLConsumer(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createAppRootCRLArtifact, target createRootCRLStateTarget) (map[string]string, error) {
	plan := a.Plan
	if plan.Root.ID != managedLateAppRootID || plan.Signed.Number != "2" || parseAppRecordFresh(plan.Root, plan.Signed) != nil || len(plan.Consumers) != 2 || e.Check(ctx) != nil {
		return nil, appRootCRLError("current-root2-authority")
	}
	bound := false
	for _, expected := range plan.Consumers {
		if deploymentplan.Digest(expected) == deploymentplan.Digest(target) {
			bound = true
		}
	}
	if !bound {
		return nil, appRootCRLError("admitted-original-consumer")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil || managedLateAppRootCRLInventoryTarget(objects, &target, true) != nil {
		return nil, appRootCRLError("fresh-original-consumer-Pod")
	}
	owner, ok := plan.ConsumerVolumes[target.ClaimID]
	if !ok || owner.PVCUID != target.ClaimUID || deploymentplan.Digest(owner) != deploymentplan.Digest(plan.OriginalVolumes[target.ClaimID]) {
		return nil, appRootCRLError("original-consumer-volume")
	}
	byID := map[string]deploymentplan.Object{}
	for _, object := range objects {
		byID[deploymentplan.ID(object)] = object
	}
	for _, resource := range []deploymentplan.Resource{plan.Controller, plan.ConfigMap} {
		actual := byID[resource.ID]
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return nil, appRootCRLError("unchanged-controller-and-trust-CM")
		}
	}
	volume, readErr := b.Read(ctx, "PersistentVolume//"+owner.PVName)
	if readErr != nil || validateManagedTransportHostVolume(owner, byID[target.ClaimID], volume) != nil {
		return nil, appRootCRLError("original-volume-native-binding")
	}
	resource := deploymentplan.Resource{ID: target.ID, UID: target.UID, Desired: target.Desired, Action: "retain"}
	readyPlan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped"}, Resources: []deploymentplan.Resource{resource}}
	if err := verifyOriginalMigrationCheckpointInventory(ctx, store, readyPlan, deploymentplan.Journal{Owners: map[string]string{target.ID: target.UID}}, objects); err != nil {
		return nil, err
	}
	evidence := map[string]string{"consumer-owner-" + target.ID: deploymentplan.Digest(target.Desired), "consumer-Pod-" + target.ID: deploymentplan.Digest(target.PodUID), "consumer-volume-" + target.ClaimID: deploymentplan.Digest(owner)}
	for i, issuer := range []environmentCreateRegisteredIssuer{plan.Root, plan.Intermediate} {
		record := plan.Signed
		if i == 1 {
			record = plan.IntermediateCRL
		}
		latestNative, _, nativeErr := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, issuer.ID)
		if nativeErr != nil || deploymentplan.Digest(latestNative.Prior) != deploymentplan.Digest(record) {
			return nil, appRootCRLError("current-native-App-CRL-record")
		}
		raw, err := readManagedLateAppRootCRLCache(ctx, cfg, b, target, target.Paths[i], issuer, record, false)
		if err != nil {
			return nil, err
		}
		evidence["cache-"+target.ID+"-"+filepath.Base(target.Paths[i])] = environmentPreflightBytesSHA256(raw)
		acks, err := readManagedLateAppRootCRLACKs(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, a, issuer, record)
		latest := managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}
		if err != nil || validateManagedLatePriorACKRoster(acks, issuer.ID, latest, []string{"pkibroker", "video-cloud-api"}, time.Now().UTC()) != nil {
			return nil, appRootCRLError("genuine-two-native-CRL-ACKs")
		}
		evidence["native-ACKs-"+issuer.ID] = deploymentplan.Digest(acks)
	}
	return evidence, e.Check(ctx)
}

const managedLateAppIntermediateID = "8e26211b-5080-4c03-9b56-dccf6061b4a1"

// This App-only reader binds the genuine native ACKs to the admitted repair.
// The historical MQTT/TLS readers retain their original domain allowlists.
func readManagedLateAppRootCRLACKs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, a createAppRootCRLArtifact, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]managedLatePriorACK, error) {
	p := a.Plan
	if cfg.Environment != "dev" || runtime.Execution == nil || p.Parent != runtime.Execution.Journal.PlanSHA256 || a.Schema != createAppRootCRLSchema+"/artifact" || p.Schema != createAppRootCRLSchema || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != deploymentplan.Digest(p) || p.Root.ID != managedLateAppRootID || p.Root.Environment != "dev" || p.Root.Domain != "app" || p.Root.Kind != "root" || p.Intermediate.ID != managedLateAppIntermediateID || p.Intermediate.Environment != "dev" || p.Intermediate.Domain != "app" || p.Intermediate.Kind != "intermediate" || p.Signed.Number != "2" || p.IntermediateCRL.Number != "3" {
		return nil, appRootCRLError("exact-admitted-App-ACK-query")
	}
	expectedIssuer, expectedRecord := p.Root, p.Signed
	if issuer.ID == managedLateAppIntermediateID {
		expectedIssuer, expectedRecord = p.Intermediate, p.IntermediateCRL
	} else if issuer.ID != managedLateAppRootID {
		return nil, appRootCRLError("exact-admitted-App-ACK-query")
	}
	if deploymentplan.Digest(issuer) != deploymentplan.Digest(expectedIssuer) || deploymentplan.Digest(record) != deploymentplan.Digest(expectedRecord) || record.IssuerID != issuer.ID || !environmentPreflightDigest.MatchString(record.Digest) {
		return nil, appRootCRLError("exact-admitted-App-ACK-query")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + issuer.ID + "' AND digest='" + record.Digest + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var rows []managedLatePriorACK
	latest := managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil || validateManagedLatePriorACKRoster(rows, issuer.ID, latest, []string{"pkibroker", "video-cloud-api"}, time.Now().UTC()) != nil {
		return nil, appRootCRLError("genuine-two-native-App-ACK-query")
	}
	return rows, nil
}

func parseAppRecordFresh(root environmentCreateRegisteredIssuer, record createRootCRLRecord) error {
	_, err := parseManagedLateAppRootCRL(root, record, true, time.Now().UTC())
	return err
}
