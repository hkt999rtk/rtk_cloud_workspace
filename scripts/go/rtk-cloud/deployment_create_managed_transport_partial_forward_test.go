package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedTransportInterruptedForwardFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedTransportPartialRecovery, createFoundationRecoveryArtifact) {
	t.Helper()
	store, e, original, oldJournal := managedTransportPartialPlanFixture(t)
	for name, value := range map[string]any{"plan.json": original, "journal.json": oldJournal} {
		if err := store.write(filepath.Join("deployment/rollback", original.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	base := e.Journal
	base.Entries = slices.Clone(e.Journal.Entries)
	base.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	var defs []deploymentplan.Definition
	var live []deploymentplan.Object
	for _, resource := range original.Resources {
		body := deploymentplan.Clone(resource.Desired)
		actual := deploymentplan.Clone(resource.Desired)
		if strings.HasPrefix(resource.ID, "StatefulSet/") {
			actual = deploymentplan.Clone(resource.Baseline)
		}
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
		if strings.HasPrefix(resource.ID, "Deployment/") {
			deploymentplan.Map(body["spec"])["replicas"] = float64(3)
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body})
	}
	for _, kind := range []string{"ConfigMap", "Secret"} {
		name := "mqtt-pki-config"
		if kind == "Secret" {
			name = "mqtt-pki-runtime"
		}
		defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": original.Stack + "-video-cloud"}, "data": deploymentplan.Object{"existing": "qualified"}}})
	}
	inputs := original.Inputs
	inputs.BindingsSHA256 = strings.Repeat("d", 64)
	preview, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: e.Journal.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(base), Materials: map[string]string{"accepted-service-core-managed-partial-original-plan": original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256()}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	plan := *preview
	plan.BindingsSHA256 = strings.Repeat("f", 64)
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: "Deployment/" + original.Stack + "-video-cloud/mqtt-pki", Owners: map[string]string{}}
	for _, resource := range plan.Resources {
		if resource.Desired["kind"] == "ConfigMap" || resource.Desired["kind"] == "Secret" {
			journal.Completed = append(journal.Completed, resource.ID)
		}
		if resource.Desired["kind"] != "StatefulSet" {
			uid := resource.UID
			if uid == "" {
				uid = "11111111-1111-4111-8111-111111111111"
				if resource.Desired["kind"] == "Secret" {
					uid = "22222222-2222-4222-8222-222222222222"
				}
			}
			journal.Owners[resource.ID] = uid
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "kubernetes/transport-managed-hosts-forward-" + review, State: "pending", IntentSHA256: hash})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + original.Stack + "-video-cloud/rtk-deployment-execution", UID: "recorded-native-lease", Holder: plan.SHA256() + ":native-owned"})
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(path, "qualification.json"), a)
	write(filepath.Join(path, "source-journal.json"), base)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), preview)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), &plan)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	return store, e, &createManagedTransportPartialRecovery{Original: original, Journal: oldJournal, PriorForward: &createManagedTransportInterruptedForward{Review: review, Plan: &plan, Journal: journal}, AdditionalOwners: map[string]deploymentplan.Resource{}}, a
}

func TestManagedTransportPartialInterruptedForwardAuthenticatesExactSourceAndOneRecordedLease(t *testing.T) {
	for _, mode := range []string{"original", "changed-admission", "changed-prefix", "changed-metadata", "extra-stage", "extra-lease", "empty-lease-uid", "wrong-lease-holder", "wrong-forward-intent"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, _ := managedTransportInterruptedForwardFixture(t)
			switch mode {
			case "changed-admission":
				e.Journal.Entries[len(e.Journal.Entries)-2].IntentSHA256 = strings.Repeat("c", 64)
			case "changed-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("c", 64)
			case "changed-metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("c", 64)
			case "extra-stage":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unrelated", State: "pending", IntentSHA256: strings.Repeat("c", 64)})
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "empty-lease-uid":
				e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].UID = ""
			case "wrong-lease-holder":
				e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder = strings.Repeat("c", 64) + ":foreign"
			case "wrong-forward-intent":
				e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = strings.Repeat("c", 64)
			}
			base, forward, err := loadManagedTransportPartialBase(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified interrupted forward accepted", mode, err)
			}
			if mode == "original" {
				if base == nil || forward == nil || !isManagedTransportBaseCheckpoint(base.Journal) || !isManagedTransportPartialCheckpoint(e.Journal) {
					t.Fatal("original partial checkpoint lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-transports-interrupted-forward" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong public continuation selector", profile, flag)
				}
				if _, _, _, err := loadManagedTransportPartialSource(store, e); err != nil {
					t.Fatal("current seed admitted lineage lost", err)
				}
			}
		})
	}
}

func TestManagedTransportPartialInterruptedForwardRejectsUnrecordedWritesAndChangedBaselines(t *testing.T) {
	for _, mode := range []string{"original", "wrong-mqtt-owner", "unwritten-openbao-owner", "changed-mqtt-baseline", "changed-openbao-baseline", "changed-openbao-desired", "missing-created-owner", "created-owner-not-uuid", "created-object-existing", "completed-mqtt", "in-flight", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial, _ := managedTransportInterruptedForwardFixture(t)
			plan, journal := partial.PriorForward.Plan, partial.PriorForward.Journal
			mqtt := "Deployment/" + plan.Stack + "-video-cloud/mqtt-pki"
			bao := "StatefulSet/" + plan.Stack + "-secrets/openbao"
			config := "ConfigMap/" + plan.Stack + "-video-cloud/mqtt-pki-config"
			switch mode {
			case "wrong-mqtt-owner":
				journal.Owners[mqtt] = "replacement"
			case "unwritten-openbao-owner":
				journal.Owners[bao] = "unqualified-write"
			case "missing-created-owner":
				delete(journal.Owners, config)
			case "created-owner-not-uuid":
				journal.Owners[config] = "fabricated"
			case "completed-mqtt":
				journal.Completed = append(journal.Completed, mqtt)
			case "in-flight":
				journal.InFlight = mqtt
			case "accepted":
				journal.Accepted = true
			}
			for index := range plan.Resources {
				resource := &plan.Resources[index]
				if mode == "changed-mqtt-baseline" && resource.ID == mqtt {
					deploymentplan.Map(resource.Baseline["spec"])["replicas"] = float64(10)
				}
				if mode == "changed-openbao-baseline" && resource.ID == bao {
					deploymentplan.Map(resource.Baseline["spec"])["replicas"] = float64(10)
				}
				if mode == "changed-openbao-desired" && resource.ID == bao {
					deploymentplan.Map(resource.Desired["spec"])["replicas"] = float64(10)
				}
				if mode == "created-object-existing" && resource.ID == config {
					resource.Action = "update"
					resource.UID = "replacement"
				}
			}
			journal.PlanSHA256 = plan.SHA256()
			if err := validateManagedTransportInterruptedForward(partial.Original, plan, journal); (err == nil) != (mode == "original") {
				t.Fatal("unrecorded prior forward accepted", mode, err)
			}
		})
	}
}

func managedTransportSevenForwardFixture(t *testing.T) (*createManagedTransportPartialRecovery, []deploymentplan.Definition, *deploymentplan.Plan) {
	t.Helper()
	_, _, partial, _ := managedTransportInterruptedForwardFixture(t)
	stack := partial.Original.Stack
	additional := []deploymentplan.Definition{
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": stack + "-secrets"}, "data": deploymentplan.Object{"client-roots.pem": "original-service-roots"}}},
		{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "image": "original-image", "env": environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,api,certissuer,controller,factory,logingester"})}}}}}}},
	}
	canonical, err := deploymentplan.Compile(partial.Original.Inputs, additional, nil)
	if err != nil {
		t.Fatal(err)
	}
	var defs []deploymentplan.Definition
	var live []deploymentplan.Object
	for _, resource := range partial.PriorForward.Plan.Resources {
		actual := deploymentplan.Clone(resource.Desired)
		uid := partial.PriorForward.Journal.Owners[resource.ID]
		if strings.HasPrefix(resource.ID, "StatefulSet/") {
			actual = deploymentplan.Clone(resource.Baseline)
			uid = resource.UID
		}
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired)})
	}
	for _, resource := range canonical.Resources {
		resource.UID = "original-extra-" + resource.ID
		partial.AdditionalOwners[resource.ID] = resource
		body := deploymentplan.Clone(resource.Desired)
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
		if body["kind"] == "ConfigMap" {
			deploymentplan.Map(body["data"])["outbound-crls.json"] = "qualified-public-crl-manifest"
		} else {
			controller := managedNamedField(environmentPKIPod(body)["containers"], "pki-controller")
			controller["env"] = mergeEnvironmentNamedFields(controller["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,api,certissuer,controller,emqx-pki,factory,logingester,openbao,pkibroker"}))
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body})
	}
	candidate, err := deploymentplan.Compile(partial.Original.Inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	return partial, defs, candidate
}

func TestManagedTransportPartialSevenForwardProhibitsNewObjectsOrReplacedOriginalOwners(t *testing.T) {
	for _, mode := range []string{"original", "created-existing-config", "created-controller", "changed-mqtt-uid", "changed-storage-uid", "missing-baseline", "unrelated-resource", "missing-original-owner"} {
		t.Run(mode, func(t *testing.T) {
			partial, _, candidate := managedTransportSevenForwardFixture(t)
			for index := range candidate.Resources {
				r := &candidate.Resources[index]
				if mode == "created-existing-config" && strings.HasSuffix(r.ID, "/mqtt-pki-config") {
					r.Action = "create"
					r.UID = ""
				}
				if mode == "created-controller" && strings.HasSuffix(r.ID, "/pki-controller") {
					r.Action = "create"
					r.UID = ""
				}
				if mode == "changed-mqtt-uid" && strings.HasSuffix(r.ID, "/mqtt-pki") {
					r.UID = "replacement"
				}
				if mode == "changed-storage-uid" && strings.HasSuffix(r.ID, "/openbao") {
					r.UID = "replacement"
				}
				if mode == "missing-baseline" && strings.HasSuffix(r.ID, "/pki-create-service-trust") {
					r.Baseline = nil
				}
				if mode == "unrelated-resource" && strings.HasSuffix(r.ID, "/pki-create-service-trust") {
					r.ID = "ConfigMap/foreign/unrelated"
				}
			}
			if mode == "missing-original-owner" {
				delete(partial.AdditionalOwners, "Deployment/"+partial.Original.Stack+"-video-cloud/pki-controller")
			}
			if err := validateManagedTransportPartialForwardActions(partial, candidate); (err == nil) != (mode == "original") {
				t.Fatal("seven-resource forward replaced or created original owner", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialSevenForwardRetainsPrivateRuntimeAndAddsOnlyPublicCRLManifest(t *testing.T) {
	for _, mode := range []string{"original", "changed-mqtt-config", "changed-mqtt-runtime", "changed-openbao-file", "changed-service-roots", "empty-crl-manifest", "unrelated-object", "missing-policy-owner"} {
		t.Run(mode, func(t *testing.T) {
			partial, defs, _ := managedTransportSevenForwardFixture(t)
			for index := range defs {
				body := defs[index].Object
				name := deploymentplan.Map(body["metadata"])["name"]
				if mode == "changed-mqtt-config" && name == "mqtt-pki-config" || mode == "changed-mqtt-runtime" && name == "mqtt-pki-runtime" || mode == "changed-openbao-file" && name == "openbao-file" {
					body["data"] = deploymentplan.Object{"foreign": "replacement"}
				}
				if name == "pki-create-service-trust" {
					if mode == "changed-service-roots" {
						deploymentplan.Map(body["data"])["client-roots.pem"] = "replacement"
					}
					if mode == "empty-crl-manifest" {
						deploymentplan.Map(body["data"])["outbound-crls.json"] = ""
					}
					if mode == "unrelated-object" {
						deploymentplan.Map(body["metadata"])["name"] = "unrelated"
					}
				}
			}
			if mode == "missing-policy-owner" {
				delete(partial.AdditionalOwners, "Deployment/"+partial.Original.Stack+"-video-cloud/pki-controller")
			}
			if err := validateManagedTransportPartialForwardDefinitions(partial, defs); (err == nil) != (mode == "original") {
				t.Fatal("seven-resource forward changed retained runtime or public trust identity", mode, err)
			}
		})
	}
}

func managedTransportHostPolicyFixture(mqtt bool) (deploymentplan.Object, deploymentplan.Object) {
	stack := "video-cloud-dev"
	kind, name := "StatefulSet", "openbao"
	if mqtt {
		kind, name = "Deployment", "mqtt-pki"
	}
	pod := deploymentplan.Object{"volumes": []any{}, "containers": []any{}, "initContainers": []any{}}
	p := environmentCreateBootstrapPlan{Stack: stack}
	roles := []string{"openbao-pki"}
	if mqtt {
		roles = []string{"mqtt", "pkibroker"}
	}
	for _, role := range roles {
		prefixes := []string{"OPENBAO_PKI_HOST_RENEWAL"}
		if role == "mqtt" {
			prefixes = []string{"EMQX_PKI_HOST_RENEWAL"}
		}
		if role == "pkibroker" {
			prefixes = []string{"PKI_BROKER_IDENTITY", "PKI_BROKER_IDENTITY_RENEWAL"}
		}
		settings := map[string]string{"ORIGINAL_IDENTITY_STATE": "retained-private-state"}
		for _, prefix := range prefixes {
			environmentCreateServiceHTTPSettings(settings, prefix, "controller.original", strings.Repeat("f", 64), false, p)
		}
		container := deploymentplan.Object{"name": role, "image": "original-native-image", "command": []any{"original-native-command"}, "env": environmentCreateEnv(settings), "volumeMounts": []any{environmentCreateServiceMount("original-state", "/original/identity", false)}}
		pod["containers"] = append(certIssuerObjectList(pod["containers"]), container)
		if !mqtt {
			init := deploymentplan.Clone(container)
			init["name"] = "openbao-pki-install"
			init["command"] = []any{"/usr/local/bin/openbaopkihost", "install"}
			pod["initContainers"] = []any{init}
		}
	}
	if mqtt {
		for _, role := range []string{"prepare-pki-state", "prepare-mqtt-host-state"} {
			pod["initContainers"] = append(certIssuerObjectList(pod["initContainers"]), deploymentplan.Object{"name": role, "image": "original-init-image", "command": []any{"/bin/sh", "-ec", "original-safe-preparation"}, "volumeMounts": []any{environmentCreateServiceMount("original-state", "/original/identity", false)}})
		}
	}
	pod["volumes"] = []any{environmentCreateServiceVolume("original-state", "persistentVolumeClaim", "original-private-claim")}
	original := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": stack + map[bool]string{true: "-video-cloud", false: "-secrets"}[mqtt]}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": pod}}}
	desired := deploymentplan.Clone(original)
	next := environmentPKIPod(desired)
	for _, group := range []string{"containers", "initContainers"} {
		for _, entry := range certIssuerObjectList(next[group]) {
			container := deploymentplan.Map(entry)
			role := deploymentplan.String(container["name"])
			prefixes := []string{}
			alias, claim := "service-state", "openbao-pki-identity"
			if role == "mqtt" {
				prefixes = []string{"EMQX_PKI_HOST_RENEWAL"}
				alias, claim = "mqtt-service-state", "mqtt-pki-host-identity"
			}
			if role == "pkibroker" {
				prefixes = []string{"PKI_BROKER_IDENTITY", "PKI_BROKER_IDENTITY_RENEWAL"}
				claim = "mqtt-pki-trust"
			}
			if role == "openbao-pki" || role == "openbao-pki-install" {
				prefixes = []string{"OPENBAO_PKI_HOST_RENEWAL"}
			}
			for _, prefix := range prefixes {
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{prefix + "_SERVER_CRL_MANIFEST": environmentCreateServiceTrust + "/outbound-crls.json", prefix + "_PKI_CONTROLLER_URL": "https://pki-controller." + stack + "-video-cloud.svc:18446", prefix + "_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}))
			}
			if len(prefixes) > 0 {
				applyCreateTransportServiceCache(next, container, alias, claim)
			}
			if role == "prepare-pki-state" || role == "prepare-mqtt-host-state" {
				root := "/run/pki-state"
				if role == "prepare-mqtt-host-state" {
					root = "/var/lib/emqx-pki"
				}
				container["command"] = []any{"/bin/sh", "-ec", "original-safe-preparation; umask 077; mkdir -p " + root + "/private/crl; chmod 700 " + root + "/private " + root + "/private/crl"}
			}
			if role == "openbao-pki-install" {
				container["command"] = []any{"/bin/sh", "-ec", "umask 077; mkdir -p /var/lib/pki-service/private/crl; chmod 700 /var/lib/pki-service/private /var/lib/pki-service/private/crl; exec /usr/local/bin/openbaopkihost install"}
			}
		}
	}
	return original, desired
}

func TestManagedTransportPartialServiceCRLHostDeltaPreservesOriginalPrivateIdentity(t *testing.T) {
	for _, mqtt := range []bool{true, false} {
		t.Run(map[bool]string{true: "mqtt", false: "openbao"}[mqtt], func(t *testing.T) {
			for _, mode := range []string{"original", "changed-image", "changed-private-env", "changed-root-pin", "extra-env", "changed-private-mount", "foreign-cache-claim", "foreign-cache-path", "wrong-crl-url", "unsafe-preparation", "changed-strategy"} {
				t.Run(mode, func(t *testing.T) {
					before, after := managedTransportHostPolicyFixture(mqtt)
					pod := environmentPKIPod(after)
					container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
					prefix := "OPENBAO_PKI_HOST_RENEWAL"
					if mqtt {
						prefix = "EMQX_PKI_HOST_RENEWAL"
					}
					switch mode {
					case "changed-image":
						container["image"] = "replacement-native-image"
					case "changed-private-env":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"ORIGINAL_IDENTITY_STATE": "replacement-private-state"}))
					case "changed-root-pin":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{prefix + "_SERVER_PKI_ROOT_SHA256": strings.Repeat("a", 64)}))
					case "extra-env":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"UNRELATED_SETTING": "foreign"}))
					case "changed-private-mount":
						managedNamedField(container["volumeMounts"], "original-state")["mountPath"] = "/replacement/identity"
					case "foreign-cache-claim":
						deploymentplan.Map(managedNamedField(pod["volumes"], "service-state")["persistentVolumeClaim"])["claimName"] = "foreign-claim"
					case "foreign-cache-path":
						alias := "service-state"
						if mqtt {
							alias = "mqtt-service-state"
						}
						managedNamedField(container["volumeMounts"], alias)["mountPath"] = "/foreign/cache"
					case "wrong-crl-url":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{prefix + "_PKI_CONTROLLER_URL": "https://foreign.example"}))
					case "unsafe-preparation":
						deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])["command"] = []any{"/bin/sh", "-ec", "rm -rf /original/identity"}
					case "changed-strategy":
						deploymentplan.Map(after["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
					}
					if err := validateManagedTransportServiceHostPolicyBody(before, after, "video-cloud-dev"); (err == nil) != (mode == "original") {
						t.Fatal("host policy changed original private identity recipe", mode, err)
					}
				})
			}
		})
	}
}

func TestManagedTransportPartialControllerDeltaAllowsOnlyExactNineConsumerPolicy(t *testing.T) {
	for _, mode := range []string{"original", "wrong-consumer-roster", "changed-image", "extra-env", "changed-private-claim", "changed-private-state", "changed-root-pin"} {
		t.Run(mode, func(t *testing.T) {
			partial, defs, _ := managedTransportSevenForwardFixture(t)
			id := "Deployment/" + partial.Original.Stack + "-video-cloud/pki-controller"
			before := partial.AdditionalOwners[id].Desired
			var after deploymentplan.Object
			for _, d := range defs {
				if deploymentplan.ID(d.Object) == id {
					after = deploymentplan.Clone(d.Object)
				}
			}
			container := managedNamedField(environmentPKIPod(after)["containers"], "pki-controller")
			switch mode {
			case "wrong-consumer-roster":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,api,certissuer,controller,factory,logingester,foreign"}))
			case "changed-image":
				container["image"] = "replacement-image"
			case "extra-env":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"FOREIGN_ENV": "unrelated"}))
			case "changed-private-claim":
				environmentPKIPod(after)["volumes"] = []any{environmentCreateServiceVolume("state", "persistentVolumeClaim", "replacement-private-claim")}
			case "changed-private-state":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_CLIENT_STATE_PATH": "replacement-private-state"}))
			case "changed-root-pin":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_SERVICE_ROOT_SHA256": strings.Repeat("f", 64)}))
			}
			if err := validateManagedTransportControllerPolicyBody(before, after); (err == nil) != (mode == "original") {
				t.Fatal("Controller policy changed original identity recipe", mode, err)
			}
		})
	}
}

func managedTransportWrittenOpenBaoFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedTransportPartialRecovery) {
	t.Helper()
	store, e, previous, first := managedTransportInterruptedForwardFixture(t)
	seven, _, preview := managedTransportSevenForwardFixture(t)
	if seven.PriorForward.Plan.SHA256() != previous.PriorForward.Plan.SHA256() {
		t.Fatal("synthetic original five-object generation diverged")
	}
	base := e.Journal
	base.Entries = slices.Clone(base.Entries[:len(base.Entries)-2])
	base.KubernetesLeases = slices.Clone(base.KubernetesLeases[:len(base.KubernetesLeases)-1])
	seedReview := ""
	for _, entry := range base.Entries {
		const prefix = "kubernetes/service-session-original-key-renewal-"
		if strings.HasPrefix(entry.Key, prefix) {
			seedReview = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []struct {
		key, id string
		size    int
	}{{"kubernetes/service-credential-secrets-renewal-" + seedReview, "ConfigMap/" + base.Stack + "-secrets/pki-create-service-trust", 7}, {"kubernetes/transport-enrollment-issuer", "Deployment/" + base.Stack + "-video-cloud/pki-controller", 16}} {
		target := seven.AdditionalOwners[stage.id]
		defs := []deploymentplan.Definition{{Phase: target.Phase, Object: target.Desired}}
		for index := 1; index < stage.size; index++ {
			defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "original-source-" + string(rune('a'+index)), "namespace": base.Stack + "-video-cloud"}}})
		}
		actual := deploymentplan.Clone(target.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = target.UID
		plan, err := deploymentplan.Compile(previous.Original.Inputs, defs, []deploymentplan.Object{actual})
		if err != nil {
			t.Fatal(err)
		}
		journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, Owners: map[string]string{}}
		for _, resource := range plan.Resources {
			journal.Completed = append(journal.Completed, resource.ID)
			journal.Owners[resource.ID] = "original-source-owner-" + resource.ID
			if resource.ID == stage.id {
				journal.Owners[resource.ID] = target.UID
			}
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := environmentCreatePlanSHA256(intent)
		if err != nil {
			t.Fatal(err)
		}
		for index := range base.Entries {
			if base.Entries[index].Key == stage.key {
				base.Entries[index].IntentSHA256 = hash
				base.Entries[index].Receipt = mustProviderRecoveryJSON(map[string]string{"plan_sha256": plan.SHA256()})
			}
		}
		write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
		write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	}
	oldReview := previous.PriorForward.Review
	var oldPreview deploymentplan.Plan
	if err := readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", oldReview, "openbao-root-trust-plan.json"), &oldPreview); err != nil {
		t.Fatal(err)
	}
	first.Plan.ParentJournalSHA = deploymentplan.Digest(base)
	review := deploymentplan.Digest(first.Plan)
	first.Qualification.PlanSHA256 = review
	for index := range first.Qualification.Gates {
		first.Qualification.Gates[index].EvidenceSHA256 = review
	}
	path := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(path, "qualification.json"), first)
	write(filepath.Join(path, "source-journal.json"), base)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), &oldPreview)
	copy(e.Journal.Entries, base.Entries)
	admission := &e.Journal.Entries[len(e.Journal.Entries)-2]
	admission.Key = "openbao-recovery-admission-" + review
	admission.IntentSHA256 = deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(first), "original_parent_journal_sha256": first.Plan.ParentJournalSHA})
	admission.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})
	e.Journal.Entries[len(e.Journal.Entries)-1].Key = "kubernetes/transport-managed-hosts-forward-" + review
	previous.PriorForward.Review = review
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{"accepted-service-core-managed-partial-original-plan": previous.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256()}}}
	newReview := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: newReview, Decision: "GO"}
	for _, gate := range first.Qualification.Gates {
		gate.EvidenceSHA256 = newReview
		a.Qualification.Gates = append(a.Qualification.Gates, gate)
	}
	plan := *preview
	plan.BindingsSHA256 = strings.Repeat("e", 64)
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{}, Failed: "StatefulSet/" + base.Stack + "-secrets/openbao"}
	for index, resource := range plan.Resources {
		journal.Owners[resource.ID] = resource.UID
		if index < 6 {
			journal.Completed = append(journal.Completed, resource.ID)
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + newReview, State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": newReview, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": newReview})}, environmentCreateJournalEntry{Key: "kubernetes/transport-managed-hosts-forward-" + newReview, State: "pending", IntentSHA256: hash})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + base.Stack + "-video-cloud/rtk-deployment-execution", UID: "second-recorded-native-lease", Holder: plan.SHA256() + ":native-owned"})
	path = filepath.Join("deployment/create/openbao-recovery", newReview)
	write(filepath.Join(path, "qualification.json"), a)
	write(filepath.Join(path, "source-journal.json"), source)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), preview)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), &plan)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	seven.PriorForward = &createManagedTransportInterruptedForward{Review: newReview, Plan: &plan, Journal: journal, Previous: previous.PriorForward}
	for _, resource := range plan.Resources {
		if _, exists := seven.AdditionalOwners[resource.ID]; exists {
			seven.AdditionalOwners[resource.ID] = resource
		}
	}
	return store, e, seven
}

func TestManagedTransportPartialWrittenOpenBaoAuthenticatesBothInterruptedGenerations(t *testing.T) {
	for _, mode := range []string{"original", "changed-first-admission", "changed-second-admission", "changed-original-prefix", "extra-lease", "third-unqualified-forward", "accepted-latest-stage"} {
		t.Run(mode, func(t *testing.T) {
			store, e, partial := managedTransportWrittenOpenBaoFixture(t)
			switch mode {
			case "changed-first-admission":
				e.Journal.Entries[len(e.Journal.Entries)-4].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-second-admission":
				e.Journal.Entries[len(e.Journal.Entries)-2].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-original-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "third-unqualified-forward":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-2:]...)
			case "accepted-latest-stage":
				last := &e.Journal.Entries[len(e.Journal.Entries)-1]
				last.State = "completed"
				last.CompletedAt = time.Now()
				last.Receipt = mustProviderRecoveryJSON(map[string]string{"plan_sha256": partial.PriorForward.Plan.SHA256()})
			}
			base, latest, err := loadManagedTransportPartialBase(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified written OpenBao source accepted", mode, err)
			}
			if mode == "original" {
				if base == nil || latest == nil || latest.Previous == nil || latest.Previous.Previous != nil || latest.Plan.SHA256() != partial.PriorForward.Plan.SHA256() {
					t.Fatal("exact two-generation source lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-transports-written-openbao" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong written OpenBao selector", profile, flag)
				}
			}
		})
	}
}

func TestManagedTransportPartialWrittenOpenBaoRejectsUncertainOrIncompleteOriginalWrites(t *testing.T) {
	for _, mode := range []string{"original", "missing-owner", "replacement-owner", "openbao-not-written", "controller-incomplete", "wrong-failure", "in-flight", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportWrittenOpenBaoFixture(t)
			plan, journal := partial.PriorForward.Plan, partial.PriorForward.Journal
			switch mode {
			case "missing-owner":
				delete(journal.Owners, plan.Resources[0].ID)
			case "replacement-owner":
				journal.Owners[plan.Resources[0].ID] = "replacement"
			case "openbao-not-written":
				delete(journal.Owners, journal.Failed)
			case "controller-incomplete":
				journal.Completed = journal.Completed[:5]
			case "wrong-failure":
				journal.Failed = plan.Resources[5].ID
			case "in-flight":
				journal.InFlight = journal.Failed
			case "accepted":
				journal.Accepted = true
			}
			if err := validateManagedTransportWrittenOpenBaoJournal(plan, journal); (err == nil) != (mode == "original") {
				t.Fatal("unqualified written OpenBao journal accepted", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialOpenBaoPullDeltaRetainsSixCompletedRecipes(t *testing.T) {
	for _, mode := range []string{"original", "other-secret", "multiple-secrets", "changed-image", "changed-identity-path", "changed-private-claim", "changed-ready-mqtt", "changed-ready-controller"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportWrittenOpenBaoFixture(t)
			defs := []deploymentplan.Definition{}
			for _, resource := range partial.PriorForward.Plan.Resources {
				body := deploymentplan.Clone(resource.Desired)
				if strings.HasPrefix(resource.ID, "StatefulSet/") {
					pod := environmentPKIPod(body)
					pod["imagePullSecrets"] = []any{deploymentplan.Object{"name": "ghcr-pull"}}
					switch mode {
					case "other-secret":
						pod["imagePullSecrets"] = []any{deploymentplan.Object{"name": "foreign"}}
					case "multiple-secrets":
						pod["imagePullSecrets"] = append(certIssuerObjectList(pod["imagePullSecrets"]), deploymentplan.Object{"name": "foreign"})
					case "changed-image":
						deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["image"] = "replacement"
					case "changed-identity-path":
						deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["env"] = environmentCreateEnv(map[string]string{"IDENTITY_PATH": "replacement"})
					case "changed-private-claim":
						pod["volumes"] = []any{environmentCreateServiceVolume("foreign", "persistentVolumeClaim", "foreign")}
					}
				}
				if mode == "changed-ready-mqtt" && strings.HasSuffix(resource.ID, "/mqtt-pki") || mode == "changed-ready-controller" && strings.HasSuffix(resource.ID, "/pki-controller") {
					deploymentplan.Map(body["spec"])["replicas"] = float64(9)
				}
				defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body})
			}
			if err := validateManagedTransportPartialForwardDefinitions(partial, defs); (err == nil) != (mode == "original") {
				t.Fatal("pull recovery changed completed or private state recipe", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialWrittenOpenBaoFoundationUsesOnlyExactRecordedBaseline(t *testing.T) {
	for _, mode := range []string{"original", "wrong-owner", "wrong-foundation-body", "unknown-write", "not-written"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportWrittenOpenBaoFixture(t)
			id := "StatefulSet/" + partial.Original.Stack + "-secrets/openbao"
			var source deploymentplan.Resource
			for _, resource := range partial.PriorForward.Plan.Resources {
				if resource.ID == id {
					source = resource
				}
			}
			owner, expected := source.UID, deploymentplan.Clone(source.Baseline)
			switch mode {
			case "wrong-owner":
				owner = "replacement"
			case "wrong-foundation-body":
				deploymentplan.Map(expected["spec"])["replicas"] = float64(10)
			case "unknown-write":
				partial.PriorForward.Journal.InFlight = id
			case "not-written":
				delete(partial.PriorForward.Journal.Owners, id)
			}
			actual, changed, err := managedTransportPartialFoundationOverride(partial, id, owner, expected)
			if (err == nil) != (mode == "original") || changed != (mode == "original") {
				t.Fatal("unqualified foundation OpenBao override", mode, changed, err)
			}
			if mode == "original" && deploymentplan.Digest(actual) != deploymentplan.Digest(source.Desired) {
				t.Fatal("latest written recipe not retained")
			}
		})
	}
}
