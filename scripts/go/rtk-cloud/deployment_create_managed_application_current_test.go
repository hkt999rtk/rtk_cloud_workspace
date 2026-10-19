package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationCurrentFixture() *createManagedApplicationPartialRecovery {
	stack := "video-cloud-dev"
	ids := managedApplicationPartialResourceIDs(stack + "-video-cloud")
	actions := []string{"create", "create", "create", "create", "update", "update", "create", "update", "create", "create", "create", "create", "retain", "retain", "retain", "retain", "create", "retain", "create", "create", "create", "update", "update", "update", "update", "update"}
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: stack}}
	journal := deploymentplan.Journal{Owners: map[string]string{}, Failed: ids[22]}
	for index, id := range ids {
		parts := strings.Split(id, "/")
		body := deploymentplan.Object{"apiVersion": "v1", "kind": parts[0], "metadata": deploymentplan.Object{"name": parts[2], "namespace": parts[1]}}
		if parts[0] == "Deployment" {
			body["spec"] = deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "formal-normal", "env": []any{deploymentplan.Object{"name": "EMPTY", "value": ""}}, "volumeMounts": []any{deploymentplan.Object{"name": "identity", "mountPath": "/var/lib/original", "readOnly": false}}}}}}}
		} else if parts[0] == "PersistentVolumeClaim" {
			body["spec"] = deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "1Gi"}}}
		}
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		resource := deploymentplan.Resource{ID: id, Phase: phase, Action: actions[index], Desired: body}
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", index+1)
		if resource.Action != "create" {
			resource.UID = uid
			resource.Baseline = deploymentplan.Clone(body)
			deploymentplan.Map(resource.Baseline["metadata"])["uid"] = uid
			if index >= 22 {
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(resource.Baseline)["containers"])[0])["image"] = "original-bootstrap"
			}
		}
		plan.Resources = append(plan.Resources, resource)
		if index < 23 {
			journal.Owners[id] = uid
		}
		if index < 22 {
			journal.Completed = append(journal.Completed, id)
		}
	}
	journal.PlanSHA256 = plan.SHA256()
	return &createManagedApplicationPartialRecovery{Plan: plan, Journal: journal, NewTrustVolumes: map[string]environmentCreateVolumeOwner{}}
}

func TestManagedApplicationCurrentOwnerPreservesCompletedWrittenAndUntouchedRecipes(t *testing.T) {
	for _, index := range []int{0, 20, 21, 22, 23, 24, 25} {
		for _, mode := range []string{"original", "replacement", "terminating", "body-drift", "server-metadata", "premature-desired", "old-failed-baseline", "api-defaults"} {
			t.Run(fmt.Sprintf("%d/%s", index, mode), func(t *testing.T) {
				partial := managedApplicationCurrentFixture()
				resource := partial.Plan.Resources[index]
				expected, uid := resource.Desired, partial.Journal.Owners[resource.ID]
				if index > 22 {
					expected, uid = resource.Baseline, resource.UID
				}
				actual := deploymentplan.Clone(expected)
				deploymentplan.Map(actual["metadata"])["uid"] = uid
				pass := true
				switch mode {
				case "replacement":
					deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
					pass = false
				case "terminating":
					deploymentplan.Map(actual["metadata"])["deletionTimestamp"] = time.Now().Format(time.RFC3339)
					pass = false
				case "body-drift":
					if index == 0 {
						deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(actual["spec"])["resources"])["requests"])["storage"] = "2Gi"
					} else {
						deploymentplan.Map(certIssuerObjectList(environmentPKIPod(actual)["containers"])[0])["image"] = "unreviewed-image"
					}
					pass = false
				case "server-metadata":
					deploymentplan.Map(actual["metadata"])["resourceVersion"] = "latest"
					actual["status"] = deploymentplan.Object{"readyReplicas": float64(0)}
				case "premature-desired":
					if index > 22 {
						actual = deploymentplan.Clone(resource.Desired)
						deploymentplan.Map(actual["metadata"])["uid"] = uid
						pass = false
					}
				case "old-failed-baseline":
					if index == 22 {
						actual = deploymentplan.Clone(resource.Baseline)
						pass = false
					}
				case "api-defaults":
					if index >= 20 && index <= 22 {
						c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(actual)["containers"])[0])
						c["imagePullPolicy"] = "IfNotPresent"
						delete(deploymentplan.Map(certIssuerObjectList(c["env"])[0]), "value")
						delete(deploymentplan.Map(certIssuerObjectList(c["volumeMounts"])[0]), "readOnly")
					}
				}
				before := deploymentplan.Digest(partial)
				if (validateManagedApplicationCurrentOwner(partial, index, actual) == nil) != pass {
					t.Fatal("current owner fence differs", index, mode)
				}
				if deploymentplan.Digest(partial) != before {
					t.Fatal("current verification changed authenticated history")
				}
			})
		}
	}
}

func TestManagedApplicationNewTrustVolumeRequiresGenuineChildAndNativeBinding(t *testing.T) {
	for _, mode := range []string{"uncaptured", "captured", "replacement-claim", "replacement-pv", "foreign-csi", "foreign-claimref", "unbound", "wrong-recipe", "other-resource", "changed-ledger", "duplicate-ledger", "reused-native-allocation"} {
		t.Run(mode, func(t *testing.T) {
			partial := managedApplicationCurrentFixture()
			resource := partial.Plan.Resources[0]
			claim := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(claim["metadata"])["uid"] = partial.Journal.Owners[resource.ID]
			deploymentplan.Map(claim["spec"])["volumeName"] = "original-new-pv"
			claim["status"] = deploymentplan.Object{"phase": "Bound"}
			owner := environmentCreateVolumeOwner{NativeID: "12345", PVName: "original-new-pv", PVUID: "99999999-9999-4999-8999-999999999999", PVCName: deploymentplan.String(deploymentplan.Map(claim["metadata"])["name"]), PVCUID: partial.Journal.Owners[resource.ID], Namespace: partial.Plan.Stack + "-video-cloud"}
			volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": owner.PVName, "uid": owner.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": owner.PVCName, "namespace": owner.Namespace, "uid": owner.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "12345-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
			ledger := environmentCreateResourceLedger{Volumes: []environmentCreateVolumeOwner{}}
			pass := mode == "uncaptured" || mode == "captured"
			switch mode {
			case "captured":
				ledger.Volumes = append(ledger.Volumes, owner)
			case "replacement-claim":
				deploymentplan.Map(claim["metadata"])["uid"] = "replaced"
			case "replacement-pv":
				deploymentplan.Map(volume["metadata"])["uid"] = "" // unknown identity must not be adopted
			case "foreign-csi":
				deploymentplan.Map(deploymentplan.Map(volume["spec"])["csi"])["driver"] = "foreign"
			case "foreign-claimref":
				deploymentplan.Map(deploymentplan.Map(volume["spec"])["claimRef"])["uid"] = "foreign"
			case "unbound":
				deploymentplan.Map(claim["status"])["phase"] = "Pending"
			case "wrong-recipe":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(claim["spec"])["resources"])["requests"])["storage"] = "2Gi"
			case "other-resource":
				resource = partial.Plan.Resources[2]
			case "changed-ledger":
				changed := owner
				changed.NativeID = "54321"
				ledger.Volumes = append(ledger.Volumes, changed)
			case "duplicate-ledger":
				ledger.Volumes = []environmentCreateVolumeOwner{owner, owner}
			case "reused-native-allocation":
				changed := owner
				changed.PVCName = "another-private-owner"
				ledger.Volumes = append(ledger.Volumes, changed)
			}
			before := deploymentplan.Digest(ledger)
			got, err := validateManagedApplicationNewTrustVolume(ledger, partial, resource, claim, volume)
			if (err == nil) != pass || pass && got != owner {
				t.Fatal("unproven new allocation accepted", mode, err)
			}
			if before != deploymentplan.Digest(ledger) {
				t.Fatal("native inspection fabricated a ledger record or timestamp")
			}
		})
	}
}

func TestManagedApplicationCurrentSessionRequiresOriginalLiveSeedAndAllowsGenuineACKProgress(t *testing.T) {
	now := time.Now().UTC()
	subjects := []string{"service:account-manager", "service:openbao"}
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "original-issuer"}}
	for _, mode := range []string{"original", "ack-progress", "expired", "sealed", "short-life", "new-session", "new-issuer", "new-root", "changed-key", "competing", "changed-allowlist", "foreign-ack", "duplicate-ack", "changed-observation"} {
		t.Run(mode, func(t *testing.T) {
			state := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(3 * time.Hour), Subjects: subjects}
			saved := state
			switch mode {
			case "ack-progress":
				state.Installed = []string{subjects[0]}
			case "expired":
				state.Status = "expired"
			case "sealed":
				state.Status = "sealed"
			case "short-life":
				state.Expires = now.Add(2 * time.Hour)
				saved.Expires = state.Expires
			case "new-session":
				state.Session = "22222222-2222-4222-8222-222222222222"
			case "new-issuer":
				state.Issuer = "another-issuer"
			case "new-root":
				state.Root = strings.Repeat("c", 64)
			case "changed-key":
				state.Fingerprint = strings.Repeat("c", 64)
			case "competing":
				state.OtherActive = 1
			case "changed-allowlist":
				state.Subjects = []string{subjects[0]}
			case "foreign-ack":
				state.Installed = []string{"foreign"}
			case "duplicate-ack":
				state.Installed = []string{subjects[0], subjects[0]}
			case "changed-observation":
				saved.Expires = now.Add(4 * time.Hour)
			}
			if managedApplicationSessionCurrent(state, saved, service, subjects, now) != (mode == "original" || mode == "ack-progress") {
				t.Fatal("changed seed lineage accepted", mode)
			}
		})
	}
}

// Use the real planner's reviewed sibling-projection recipe, without the later
// standalone Controller CRL setting. The interrupted child never reported Failed.
func managedApplicationInterruptedCurrentFixture(t *testing.T) *createManagedApplicationPartialRecovery {
	t.Helper()
	partial, _ := managedApplicationForwardActionsFixture(t)
	removeLaterSetting := func(body deploymentplan.Object) {
		for _, raw := range certIssuerObjectList(environmentPKIPod(body)["containers"]) {
			container := deploymentplan.Map(raw)
			var retained []any
			for _, setting := range certIssuerObjectList(container["env"]) {
				if deploymentplan.Map(setting)["name"] != "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST" {
					retained = append(retained, setting)
				}
			}
			if container["env"] != nil {
				container["env"] = retained
			}
		}
	}
	for index := range partial.Plan.Resources {
		removeLaterSetting(partial.Plan.Resources[index].Desired)
		removeLaterSetting(partial.Plan.Resources[index].Baseline)
	}
	partial.Journal.PlanSHA256 = partial.Plan.SHA256()
	definitions, err := managedApplicationPartialDefinitionsWithRepairs(partial, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for index, resource := range partial.Plan.Resources {
		body, uid := resource.Desired, partial.Journal.Owners[resource.ID]
		if index > 22 {
			body, uid = resource.Baseline, resource.UID
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	inputs := partial.Plan.Inputs
	inputs.Operation = "scoped"
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)
	candidate, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: candidate.SHA256(), Owners: map[string]string{}}
	for index, resource := range candidate.Resources {
		if index < 23 {
			journal.Owners[resource.ID] = resource.UID
		}
		if index < 22 {
			journal.Completed = append(journal.Completed, resource.ID)
		}
	}
	partial.Interrupted = &createManagedApplicationInterruptedForward{Review: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 64), ArtifactSHA: strings.Repeat("c", 64), Plan: candidate, Journal: journal}
	if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
		t.Fatal("genuine interrupted recipe refused", err)
	}
	return partial
}

func TestManagedApplicationCurrentOwnerUsesInterruptedWrittenRecipeWithoutInventingFailure(t *testing.T) {
	for _, mode := range []string{"completed", "written-api", "untouched", "old-written-api", "unwritten-future", "replacement", "changed-body"} {
		t.Run(mode, func(t *testing.T) {
			partial := managedApplicationInterruptedCurrentFixture(t)
			index := 22
			if mode == "completed" {
				index = 21
			}
			if mode == "untouched" || mode == "unwritten-future" {
				index = 25
			}
			resource := partial.Interrupted.Plan.Resources[index]
			expected, owner := resource.Desired, partial.Interrupted.Journal.Owners[resource.ID]
			if index > 22 {
				expected, owner = resource.Baseline, resource.UID
			}
			actual := deploymentplan.Clone(expected)
			deploymentplan.Map(actual["metadata"])["uid"] = owner
			pass := mode == "completed" || mode == "written-api" || mode == "untouched"
			switch mode {
			case "old-written-api":
				actual = deploymentplan.Clone(partial.Plan.Resources[index].Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = owner
			case "unwritten-future":
				actual = deploymentplan.Clone(resource.Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = owner
			case "replacement":
				deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "changed-body":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(actual)["containers"])[0])["image"] = "unqualified-image"
			}
			before := deploymentplan.Digest(partial)
			if (validateManagedApplicationCurrentOwner(partial, index, actual) == nil) != pass {
				t.Fatal("interrupted owner selection differs", mode)
			}
			if before != deploymentplan.Digest(partial) || partial.Journal.Failed == "" || partial.Interrupted.Journal.Failed != "" {
				t.Fatal("verification fabricated or changed failure history")
			}
		})
	}
}

func managedApplicationLatestCurrentFixture(t *testing.T) *createManagedApplicationPartialRecovery {
	t.Helper()
	partial := managedApplicationInterruptedCurrentFixture(t)
	previous := partial.Interrupted
	previous.LeaseSHA = deploymentplan.Digest("original-qualified-native-lease")
	definitions, err := managedApplicationPartialDefinitionsWithRepairs(partial, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for index, resource := range previous.Plan.Resources {
		body, uid := resource.Desired, previous.Journal.Owners[resource.ID]
		if index > 22 {
			body, uid = resource.Baseline, resource.UID
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	inputs := previous.Plan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)
	candidate, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: candidate.SHA256(), Owners: map[string]string{}, Failed: candidate.Resources[23].ID}
	for index, resource := range candidate.Resources {
		if index < 24 {
			journal.Owners[resource.ID] = resource.UID
		}
		if index < 23 {
			journal.Completed = append(journal.Completed, resource.ID)
		}
	}
	partial.Interrupted = &createManagedApplicationInterruptedForward{Previous: previous, Review: strings.Repeat("d", 64), SourceSHA: strings.Repeat("e", 64), ArtifactSHA: strings.Repeat("f", 64), Plan: candidate, Journal: journal}
	if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
		t.Fatal("genuine latest boundary refused", err)
	}
	return partial
}

func TestManagedApplicationCurrentOwnerUsesExactLatestCompletedWrittenAndUntouchedBoundary(t *testing.T) {
	fixture := managedApplicationLatestCurrentFixture(t)
	for _, mode := range []string{"completed-main-api", "written-app-api", "untouched-device", "untouched-log", "old-completed-api", "old-app-baseline", "future-device", "future-log", "replaced-app", "extra-completion", "cleared-real-failure", "extra-previous"} {
		t.Run(mode, func(t *testing.T) {
			var partial createManagedApplicationPartialRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(fixture), &partial) != nil {
				t.Fatal("fixture cloning failed")
			}
			index := 23
			if mode == "completed-main-api" || mode == "old-completed-api" {
				index = 22
			}
			if mode == "untouched-device" || mode == "future-device" {
				index = 24
			}
			if mode == "untouched-log" || mode == "future-log" {
				index = 25
			}
			current := partial.Interrupted
			resource := current.Plan.Resources[index]
			body, uid := resource.Desired, current.Journal.Owners[resource.ID]
			if index > 23 {
				body, uid = resource.Baseline, resource.UID
			}
			actual := deploymentplan.Clone(body)
			deploymentplan.Map(actual["metadata"])["uid"] = uid
			switch mode {
			case "old-completed-api":
				actual = deploymentplan.Clone(current.Previous.Plan.Resources[index].Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = uid
			case "old-app-baseline":
				actual = deploymentplan.Clone(resource.Baseline)
			case "future-device", "future-log":
				actual = deploymentplan.Clone(resource.Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = uid
			case "replaced-app":
				deploymentplan.Map(actual["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "extra-completion":
				current.Journal.Completed = append(current.Journal.Completed, resource.ID)
			case "cleared-real-failure":
				current.Journal.Failed = ""
			case "extra-previous":
				current.Previous.Previous = current.Previous
			}
			// The invalid depth uses a finite copy for its rejection check.
			if mode == "extra-previous" {
				extra := *current.Previous
				extra.Previous = nil
				current.Previous.Previous = &extra
			}
			before := deploymentplan.Digest(partial)
			pass := strings.HasPrefix(mode, "untouched-") || mode == "completed-main-api" || mode == "written-app-api"
			if (validateManagedApplicationCurrentOwner(&partial, index, actual) == nil) != pass {
				t.Fatal("latest boundary adopted stale, unwritten or unqualified owner", mode)
			}
			if before != deploymentplan.Digest(partial) {
				t.Fatal("current verification rewrote bounded history")
			}
		})
	}
}

func TestManagedApplicationNewTrustVolumeUsesOriginalCreationAfterInterruptedRetain(t *testing.T) {
	partial := managedApplicationInterruptedCurrentFixture(t)
	resource := partial.Plan.Resources[0]
	if partial.Interrupted.Plan.Resources[0].Action != "retain" || resource.Action != "create" {
		t.Fatal("fixture lost genuine allocation history")
	}
	claim := deploymentplan.Clone(resource.Desired)
	deploymentplan.Map(claim["metadata"])["uid"] = partial.Journal.Owners[resource.ID]
	if claim["spec"] == nil {
		claim["spec"] = deploymentplan.Object{}
	}
	deploymentplan.Map(claim["spec"])["volumeName"] = "original-new-pv"
	claim["status"] = deploymentplan.Object{"phase": "Bound"}
	volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "original-new-pv", "uid": "99999999-9999-4999-8999-999999999999"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": deploymentplan.Map(claim["metadata"])["name"], "namespace": deploymentplan.Map(claim["metadata"])["namespace"], "uid": deploymentplan.Map(claim["metadata"])["uid"]}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "12345-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	before := deploymentplan.Digest(partial)
	owner, err := validateManagedApplicationNewTrustVolume(environmentCreateResourceLedger{}, partial, resource, claim, volume)
	if err != nil || owner.PVCUID != partial.Journal.Owners[resource.ID] {
		t.Fatal("original allocation proof refused", err)
	}
	if _, err := validateManagedApplicationNewTrustVolume(environmentCreateResourceLedger{}, partial, partial.Interrupted.Plan.Resources[0], claim, volume); err == nil {
		t.Fatal("retained update adopted as new allocation authority")
	}
	if before != deploymentplan.Digest(partial) {
		t.Fatal("allocation verification changed original or interrupted journal")
	}
}

func TestManagedApplicationCurrentSessionLifetimeRequiresAuthenticatedInterruptedProfile(t *testing.T) {
	partial := managedApplicationCurrentFixture()
	if profile, err := managedApplicationCurrentSessionLifetime(partial); err != nil || profile.minimumLife() != 2*time.Hour {
		t.Fatal("default life changed", err)
	}
	partial = managedApplicationInterruptedCurrentFixture(t)
	if profile, err := managedApplicationCurrentSessionLifetime(partial); err != nil || profile.minimumLife() != 50*time.Minute {
		t.Fatal("bounded continuation margin differs", err)
	}
	partial.Interrupted.Journal.Failed = partial.Interrupted.Plan.Resources[22].ID
	if _, err := managedApplicationCurrentSessionLifetime(partial); err == nil {
		t.Fatal("unqualified profile relaxed lifetime")
	}
}

func TestManagedApplicationCurrentSessionBoundedLifetimeRetainsAllIdentityGuards(t *testing.T) {
	now := time.Now().UTC()
	subjects := []string{"service:account-manager", "service:openbao"}
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "original-issuer"}}
	for _, mode := range []string{"bounded", "default", "exact-margin", "short-margin", "unknown-profile", "new-session", "changed-key", "foreign-ack", "competing", "changed-allowlist", "changed-observation"} {
		t.Run(mode, func(t *testing.T) {
			state := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(time.Hour), Subjects: subjects, Installed: []string{subjects[0]}}
			saved := state
			profile := managedApplicationInterruptedSessionLifetime
			switch mode {
			case "default":
				profile = managedApplicationOriginalSessionLifetime
			case "exact-margin":
				state.Expires = now.Add(50 * time.Minute)
				saved.Expires = state.Expires
			case "short-margin":
				state.Expires = now.Add(49 * time.Minute)
				saved.Expires = state.Expires
			case "unknown-profile":
				profile = managedApplicationSessionLifetime(255)
			case "new-session":
				state.Session = "22222222-2222-4222-8222-222222222222"
			case "changed-key":
				state.Fingerprint = strings.Repeat("c", 64)
			case "foreign-ack":
				state.Installed = []string{"foreign"}
			case "competing":
				state.OtherActive = 1
			case "changed-allowlist":
				state.Subjects = subjects[:1]
			case "changed-observation":
				saved.Expires = now.Add(2 * time.Hour)
			}
			if managedApplicationSessionCurrentWithLifetime(state, saved, service, subjects, now, profile) != (mode == "bounded") {
				t.Fatal("bounded profile accepted changed lineage or lost rollout margin", mode)
			}
		})
	}
}

func managedApplicationAPIServerFixture(t *testing.T) (environmentCreateServiceCeremony, *createManagedApplicationPartialRecovery, []managedTransportServerEvidence) {
	t.Helper()
	now := time.Now()
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original-service-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
	authorityKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "original-service-authority"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, root, &authorityKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: environmentPreflightBytesSHA256(rootDER), Certificate: rootPEM}, Intermediate: environmentCreateRegisteredIssuer{ID: "original-service-issuer", Certificate: caPEM}}
	partial := &createManagedApplicationPartialRecovery{Exports: map[string]*createManagedTransportExportRetire{}, AcceptedPlans: map[string]*deploymentplan.Plan{}}
	var rows []managedTransportServerEvidence
	for index, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		dns := name + ".video-cloud-dev-video-cloud.svc"
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}}, key)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(index + 2)), Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, authority, &key.PublicKey, authorityKey)
		if err != nil {
			t.Fatal(err)
		}
		leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		chain := leafPEM + caPEM + rootPEM
		partial.Exports[name] = &createManagedTransportExportRetire{ServerName: dns, RootSHA: service.Root.RootFingerprint, CertificateSHA: environmentPreflightBytesSHA256([]byte(chain))}
		partial.AcceptedPlans["kubernetes/server-secret-"+name] = &deploymentplan.Plan{Resources: []deploymentplan.Resource{{Desired: deploymentplan.Object{"data": deploymentplan.Object{"tls.crt": base64.StdEncoding.EncodeToString([]byte(chain))}}}}}
		rows = append(rows, managedTransportServerEvidence{Domain: "service", Caller: "service:video-cloud-api", Request: service.BootstrapSessionID + ":" + name + ":server", Issuer: service.Intermediate.ID, Status: "succeeded", Fingerprint: environmentPreflightBytesSHA256(der), CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), Certificate: leafPEM, DNS: []string{dns}})
	}
	return service, partial, rows
}

func TestManagedApplicationNativeAPIServersRequireOriginalSignedCSRAndExportedChain(t *testing.T) {
	for _, mode := range []string{"original", "pem-whitespace", "missing", "extra", "duplicate", "request", "caller", "domain", "issuer", "status", "revoked", "dns", "fingerprint", "other-csr-key", "unsigned-csr", "changed-chain", "extra-native-chain", "changed-export", "changed-root", "changed-signing-authority", "expired"} {
		t.Run(mode, func(t *testing.T) {
			service, partial, rows := managedApplicationAPIServerFixture(t)
			now := time.Now()
			switch mode {
			case "pem-whitespace":
				rows[0].Certificate = "\n" + rows[0].Certificate + "\n"
			case "missing":
				rows = rows[:1]
			case "extra":
				rows = append(rows, rows[0])
			case "duplicate":
				rows[1] = rows[0]
			case "request":
				rows[0].Request = "new-issuance"
			case "caller":
				rows[0].Caller = "service:foreign"
			case "domain":
				rows[0].Domain = "mqtt"
			case "issuer":
				rows[0].Issuer = "new-authority"
			case "status":
				rows[0].Status = "pending"
			case "revoked":
				rows[0].Revoked = &now
			case "dns":
				rows[0].DNS = []string{"foreign.example"}
			case "fingerprint":
				rows[0].Fingerprint = strings.Repeat("f", 64)
			case "other-csr-key":
				rows[0].CSR = rows[1].CSR
			case "unsigned-csr":
				rows[0].CSR = "unproven"
			case "changed-chain":
				rows[0].Certificate = rows[1].Certificate
			case "extra-native-chain":
				rows[0].Certificate += service.Intermediate.Certificate
			case "changed-export":
				partial.Exports["video-cloud-api-pki"].CertificateSHA = strings.Repeat("f", 64)
			case "changed-root":
				partial.Exports["video-cloud-api-pki"].RootSHA = strings.Repeat("f", 64)
			case "changed-signing-authority":
				service.Intermediate.Certificate = service.Root.Certificate
			case "expired":
				now = now.Add(24 * time.Hour)
			}
			before := deploymentplan.Digest(partial)
			digest, err := validateManagedApplicationAPIServers(rows, service, partial, now)
			pass := mode == "original" || mode == "pem-whitespace"
			if (err == nil) != pass || pass && !environmentPreflightDigest.MatchString(digest) {
				t.Fatal("nonoriginal registered API server admitted", mode, err)
			}
			if deploymentplan.Digest(partial) != before {
				t.Fatal("native public verification rewrote original export proof")
			}
		})
	}
}

func TestManagedApplicationThirdSessionLifetimeAndExecutorRequireBoundedAuthenticatedSource(t *testing.T) {
	_, _, partial, forward := managedApplicationThirdInterruptedFixture(t)
	partial.Interrupted = forward
	profile, err := managedApplicationCurrentSessionLifetime(partial)
	if err != nil || profile != managedApplicationTrustACKSessionLifetime || profile.minimumLife() != 25*time.Minute {
		t.Fatal("strict third source did not select fixed25m", err)
	}
	for _, mode := range []string{"30m", "29m", "31m", "unbounded", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			switch mode {
			case "30m":
				ctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
			case "29m":
				ctx, cancel = context.WithTimeout(ctx, 29*time.Minute)
			case "31m":
				ctx, cancel = context.WithTimeout(ctx, 31*time.Minute)
			case "cancelled":
				ctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
				cancel()
			}
			defer cancel()
			materials := map[string]string{}
			err := verifyManagedApplicationCurrentExecutionBound(ctx, partial, materials)
			pass := mode == "30m" || mode == "29m"
			if (err == nil) != pass {
				t.Fatal("executor bound differs", mode, err)
			}
			if pass && (materials["accepted-service-core-managed-application-execution-limit"] != deploymentplan.Digest((30*time.Minute).String()) || materials["accepted-service-core-managed-application-native-bootstrap-limit"] != deploymentplan.Digest((20*time.Minute).String())) {
				t.Fatal("clock-dependent deadline entered material")
			}
		})
	}
	extra := *forward.Previous.Previous
	forward.Previous.Previous.Previous = &extra
	if _, err := managedApplicationCurrentSessionLifetime(partial); err == nil {
		t.Fatal("unqualified fourth source relaxed life")
	}
}

func TestManagedApplicationSessionTrustACKAndFinalizationMarginsRetainExactNativeProofs(t *testing.T) {
	now := time.Now().UTC()
	subjects := []string{"service:account-manager", "service:certissuer", "service:emqx-pki", "service:factory-enroll", "service:openbao", "service:pki-controller", "service:pkibroker", "service:video-cloud-api", "service:video-cloud-logingester"}
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "original-issuer"}}
	for _, mode := range []string{"trust26m", "trust25m", "trust24m", "old50m", "default2h", "final21m", "final20m", "final19m", "final-incomplete", "final-duplicate", "final-foreign", "unknown-profile"} {
		t.Run(mode, func(t *testing.T) {
			state := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(26 * time.Minute), Subjects: subjects, Installed: slices.Clone(subjects)}
			profile := managedApplicationTrustACKSessionLifetime
			switch mode {
			case "trust25m":
				state.Expires = now.Add(25 * time.Minute)
			case "trust24m":
				state.Expires = now.Add(24 * time.Minute)
			case "old50m":
				profile = managedApplicationInterruptedSessionLifetime
			case "default2h":
				profile = managedApplicationOriginalSessionLifetime
			case "final21m":
				profile = managedApplicationFinalizationSessionLifetime
				state.Expires = now.Add(21 * time.Minute)
			case "final20m":
				profile = managedApplicationFinalizationSessionLifetime
				state.Expires = now.Add(20 * time.Minute)
			case "final19m":
				profile = managedApplicationFinalizationSessionLifetime
				state.Expires = now.Add(19 * time.Minute)
			case "final-incomplete":
				profile = managedApplicationFinalizationSessionLifetime
				state.Installed = state.Installed[:8]
			case "final-duplicate":
				profile = managedApplicationFinalizationSessionLifetime
				state.Installed[0] = state.Installed[1]
			case "final-foreign":
				profile = managedApplicationFinalizationSessionLifetime
				state.Installed[0] = "service:foreign"
			case "unknown-profile":
				profile = managedApplicationSessionLifetime(255)
			}
			saved := state
			if managedApplicationSessionCurrentWithLifetime(state, saved, service, subjects, now, profile) != (mode == "trust26m" || mode == "final21m") {
				t.Fatal("phase life or native ACK guard differs", mode)
			}
		})
	}
}
