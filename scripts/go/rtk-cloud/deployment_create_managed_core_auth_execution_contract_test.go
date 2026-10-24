package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newManagedCoreNativeAuthenticationExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *createManagedCorePartialRecovery) {
	t.Helper()
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	configure := hooks.Configure
	hooks.Configure = func(cfg *deploymentConfig, p *environmentCreatePlan, bootstrap *environmentCreateBootstrapPlan) {
		configure(cfg, p, bootstrap)
		cfg.Values[deploymentRolloutTimeoutKey] = "300"
		cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "3600"
	}
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t, hooks)
	// Select the session-scoped cleanup carrier before its actual native writes.
	f.service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + f.session[:12]
	rollout, err := environmentRolloutTimeout(f.cfg.Values)
	if err != nil {
		t.Fatal(err)
	}
	f.backend.RolloutTimeout = rollout
	prepared, err := renderEnvironmentCreatePreparedOpenBaoConsumers(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, roots["openbao_tls"], intermediates["openbao_tls"])
	if err != nil {
		t.Fatal("original prepared Core native owners", err)
	}
	f.acceptedHistoricalStage(t, "isolated-original-core-native-policy", prepared)
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err = applyEnvironmentCreateManagedOpenBaoConsumers(f.p.Plan.Bootstrap, f.service, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err = configureEnvironmentCreateFinalIssuer(f.p.Plan.Bootstrap, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	// The original serial producer selected the documented rejected setting
	// before writing its immutable child. The future repair toggles only this
	// public flag; no original owner or recorded source is rewritten.
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == "Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller" {
			c := mqttFoundationPrimary(d.Object)
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_FIXED_DEVICE_ROOT_TRUST": "false"}))
		}
	}
	plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/managed-complete-issuer", definitions, "Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller")
	if err := validateManagedCorePartialJournal(plan, journal); err != nil {
		for _, resource := range plan.Resources {
			t.Log(resource.ID, resource.Action, resource.Phase)
		}
		t.Fatal("real stopped Core16 native cohort", err)
	}
	core := &createManagedCorePartialRecovery{Action: managedCoreRestoreOnly, Plan: plan, Journal: journal}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	installManagedCoreNativeAuthenticationProtocol(t, f)
	return f, core
}

func installManagedCoreNativeAuthenticationProtocol(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:]
if 'rollout' in a:
 i=a.index('rollout');kind,name=a[i+2].split('/',1);ns=a[a.index('--namespace')+1]
 obj=d['objects'].get(kind+'/'+ns+'/'+name)
 if not obj:sys.exit('native rollout owner absent')
 desired=obj['spec'].get('replicas',1);s=obj.get('status',{})
 if s.get('observedGeneration')!=obj['metadata'].get('generation') or any(s.get(k)!=desired for k in ['replicas','readyReplicas','updatedReplicas','availableReplicas']):sys.exit('native rollout incomplete')
 sys.exit()
if 'original-native-read-policy' in a:
 script=a[a.index('-ec')+1]
 if script!='export PKI_DATABASE_URL="$CERT_ISSUER_DB_DSN"\nexport PGOPTIONS=\'-c default_transaction_read_only=on\'\nexec /app/pkicontroller render-openbao-policy "$1"':sys.exit('unqualified policy command')
 if a[-1]!=d['policy']['issuer_id'] and not d.get('allow_foreign_policy_response'):sys.exit('wrong issuer query')
 if d.get('after_policy'):
  change=d['after_policy'];obj=d['objects'][change['id']]
  if change['field']=='uid':obj['metadata']['uid']=change['value']
  elif change['field']=='data':obj['data']['url']=change['value']
  with open(os.environ['RTK_CORE_CONTRACT_NATIVE'],'w') as output:json.dump(d,output)
 print(json.dumps(d['policy']));sys.exit()
os.execv(os.environ['RTK_CORE_AUTH_PARENT'],[os.environ['RTK_CORE_AUTH_PARENT']]+a)
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CORE_AUTH_PARENT", original)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestManagedCoreNativeAuthenticationPreservesOriginalDatabaseAndControllerCustody(t *testing.T) {
	f, core := newManagedCoreNativeAuthenticationExecutionContract(t)
	read := func() (string, string, string, error) {
		return observeManagedCoreRecordedAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, core, f.service, f.session)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	name, role, policy, err := read()
	if err != nil || name != "rtk-dev-service-bootstrap-"+f.session || role != "pki-service-bootstrap-dev" || strings.TrimSpace(policy) == "" {
		t.Fatal("original native certissuer policy could not authenticate", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("readonly authentication changed native owners")
	}
	if _, _, _, err := observeManagedCoreRecordedAuthentication(nil, f.cfg, f.store, f.e, f.backend, core, f.service, f.session); err == nil {
		t.Fatal("native reader accepted a missing execution context")
	}
	// This cohort deliberately tests the internal native boundary. The public
	// entrypoint must still refuse its missing complete application ancestry.
	full := &createServiceSessionRecovery{Intent: createServiceSessionIntent{SessionID: f.session}, Ceremony: f.service, ManagedCorePartial: core, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{}}}
	if _, _, _, err := managedCoreRecordedAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, full); err == nil {
		t.Fatal("public authentication bypassed its original historical source guard")
	}
}

func TestManagedCoreNativeAuthenticationRejectsOriginalOwnerAndReadonlyPolicyDrift(t *testing.T) {
	f, core := newManagedCoreNativeAuthenticationExecutionContract(t)
	original := mustProviderRecoveryJSON(f.protocol)
	issuer := "Deployment/" + f.p.Plan.Stack + "-video-cloud/certissuer"
	database := "Secret/" + f.p.Plan.Stack + "-video-cloud/pki-issuer-database"
	pod := "Pod/" + f.p.Plan.Stack + "-video-cloud/certissuer-pod"
	rs := "ReplicaSet/" + f.p.Plan.Stack + "-video-cloud/certissuer-isolated"
	for _, mode := range []string{"valid", "issuer-uid", "issuer-body", "database-uid", "database-body", "pod-uid", "rs-uid", "rs-controller", "rs-template", "extra-pod", "policy-issuer", "policy-environment", "policy-empty", "policy-scope", "issuer-readback", "database-readback", "pod-readback"} {
		t.Run(mode, func(t *testing.T) {
			if err := json.Unmarshal(original, &f.protocol); err != nil {
				t.Fatal(err)
			}
			objects := deploymentplan.Map(f.protocol["objects"])
			object := func(id string) deploymentplan.Object { return deploymentplan.Map(objects[id]) }
			foreign := "99999999-9999-4999-8999-999999999999"
			switch mode {
			case "issuer-uid":
				deploymentplan.Map(object(issuer)["metadata"])["uid"] = foreign
			case "issuer-body":
				mqttFoundationPrimary(object(issuer))["image"] = "isolated:foreign"
			case "database-uid":
				deploymentplan.Map(object(database)["metadata"])["uid"] = foreign
			case "database-body":
				deploymentplan.Map(object(database)["data"])["url"] = "Zm9yZWlnbg=="
			case "pod-uid":
				deploymentplan.Map(object(pod)["metadata"])["uid"] = "not-a-native-uuid"
			case "rs-uid":
				deploymentplan.Map(object(rs)["metadata"])["uid"] = foreign
			case "rs-controller":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(object(rs)["metadata"])["ownerReferences"])[0])["uid"] = foreign
			case "rs-template":
				mqttFoundationPrimary(deploymentplan.Object{"spec": deploymentplan.Map(object(rs)["spec"])})["image"] = "isolated:foreign"
			case "extra-pod":
				other := deploymentplan.Clone(object(pod))
				deploymentplan.Map(other["metadata"])["name"] = "certissuer-foreign"
				deploymentplan.Map(other["metadata"])["uid"] = foreign
				objects[deploymentplan.ID(other)] = other
			case "policy-issuer":
				deploymentplan.Map(f.protocol["policy"])["issuer_id"] = foreign
				f.protocol["allow_foreign_policy_response"] = true
			case "policy-environment":
				deploymentplan.Map(f.protocol["policy"])["environment"] = "staging"
			case "policy-empty":
				deploymentplan.Map(f.protocol["policy"])["service_client_signer_policy"] = ""
			case "policy-scope":
				deploymentplan.Map(f.protocol["policy"])["service_client_signer_policy"] = "path \"foreign/*\" { capabilities=[\"update\"] }"
			case "issuer-readback", "database-readback", "pod-readback":
				id := map[string]string{"issuer-readback": issuer, "database-readback": database, "pod-readback": pod}[mode]
				f.protocol["after_policy"] = map[string]string{"id": id, "field": "uid", "value": foreign}
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			writes := deploymentplan.Digest(f.native.writes)
			_, _, _, err := observeManagedCoreRecordedAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, core, f.service, f.session)
			if (err == nil) != (mode == "valid") {
				t.Fatal("unqualified native authentication accepted", mode, err)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused native observation wrote original owners")
			}
		})
	}
}
