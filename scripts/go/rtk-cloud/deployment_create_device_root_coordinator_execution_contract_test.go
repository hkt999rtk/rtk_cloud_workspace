package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the actual Device coordinator at its selected bootstrap boundary.
// The original Core, policy/role writers and child executor remain real. Only
// native policy capability, Device signing/registry and consumer processes are
// simulated; this fixture makes no public preflight or full-ancestry claim.
type deviceRootCoordinatorContract struct {
	f                            *acceptedCoreExecutionContract
	path, cache, mode            string
	root                         environmentCreateRegisteredIssuer
	ready, active, distributions int
}

func newDeviceRootCoordinatorContract(t *testing.T, mode string) *deviceRootCoordinatorContract {
	t.Helper()
	f := newCoreExecutionContractAtBoundary(t, false, true, false, historicalCompleteDomainPolicy(t))
	f.runtime.Backend = f.backend
	f.runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
	f.runtime.ServiceSessionRecovery = nil
	// Original application enrollment allocates the actual durable claims
	// before either Device consumer starts; never invent them during ACK capture.
	enrollment, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "device-coordinator-original-application-enrollment", enrollment)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
	d := &deviceRootCoordinatorContract{f: f, path: filepath.Join(t.TempDir(), "device-native.json"), cache: t.TempDir(), mode: mode}
	if err := os.WriteFile(d.path, mustProviderRecoveryJSON(map[string]any{"mode": mode, "root": nil, "acks": []string{}, "retirements": 0}), 0600); err != nil {
		t.Fatal(err)
	}
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	protocol := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys
original=%q
path=%q
bao=%q
with open(path) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
capabilities={
 'render-device-bootstrap-policy':'path "pki-issuers/device/+/+/root/generate/existing" { capabilities = ["update"] }\n',
 'render-automatic-device-policy':'path "pki-issuers/device/+/+/intermediate/generate/internal" { capabilities = ["update"] }\n',
 'render-automatic-device-signer-policy':'path "pki-issuers/device/+/+/sign/device" { capabilities = ["update"] }\n'}
if a and a[-1] in capabilities:
 required=['--namespace',%q,'exec','deployment/pki-controller','--container','pki-controller','--','/app/pkicontroller',a[-1]]
 if a[-len(required):]!=required:sys.exit('wrong native capability owner')
 result=capabilities[a[-1]]
 if d['mode']=='unsafe-policy' and a[-1]=='render-automatic-device-policy':result+=capabilities['render-device-bootstrap-policy']
 sys.stdout.write(result);sys.exit()
if 'registry-read' in a and 'FROM pki_bundle_acknowledgments' in joined:
 root=d['root']
 if root is None:sys.exit('Device root has not been generated')
 expected="SELECT COALESCE(json_agg(consumer_id ORDER BY consumer_id),'[]'::json) FROM pki_bundle_acknowledgments WHERE issuer_id='"+root['issuer_id']+"' AND bundle_version='"+root['trust_bundle_version']+"'"
 if a[-2:]!=['registry-read',expected]:sys.exit('foreign Device receipt query')
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:core=json.load(source)
 for receipt in d.get('receipts',[]):
  owner=core['objects'].get(receipt['owner_id']);claim=core['objects'].get(receipt['claim_id']);config=core['objects'].get(receipt['config_id'])
  if owner is None or owner['metadata']['uid']!=receipt['owner_uid'] or owner['spec']!=receipt['owner_spec'] or claim is None or claim['metadata']['uid']!=receipt['claim_uid'] or config is None or config['metadata']['uid']!=receipt['config_uid'] or config['data']!=receipt['config_data']:sys.exit('Device receipt native owner or mounted bundle changed')
  with open(receipt['cache']) as source:body=source.read()
  if body!=root['chain_pem']:sys.exit('Device consumer cache changed')
 print(json.dumps(d['acks']));sys.exit()
if 'bao delete auth/kubernetes/role/' in joined:
 expected=%q
 if expected not in a:sys.exit('unexpected native retirement command')
 token=sys.stdin.read().strip()
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:core=json.load(source)
 if token!=core['historical_bao_root_token']:sys.exit('wrong native retirement credential')
 if d['root'] is None or d['root']['status']!='active' or sorted(d['acks'])!=['pkibroker','video-cloud-api']:sys.exit('retirement precedes actual activation and both consumers')
 with open(bao) as source:state=json.load(source)
 role=%q;policy='create-device-root-bootstrap'
 if role not in state['roles'] or policy not in state['policies']:sys.exit('original Device bootstrap capability is absent')
 if d['mode']!='retirement-readback':
  del state['roles'][role];del state['policies'][policy]
 with open(bao,'w') as target:json.dump(state,target)
 d['retirements']+=1
 with open(path,'w') as target:json.dump(d,target)
 print(json.dumps({'roles':sorted(state['roles']),'policies':sorted(state['policies'])}));sys.exit()
os.execv(original,[original]+a)
`, original, d.path, f.protocol["historical_session_bao_path"], f.p.Plan.Stack+"-video-cloud", deviceRootRetirementScript(f.cfg.Environment), "pki-device-bootstrap-"+f.cfg.Environment)
	dir := t.TempDir()
	command := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(command, []byte(protocol), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", command)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login" {
			var got map[string]string
			if json.NewDecoder(r.Body).Decode(&got) != nil || got["email"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || got["password"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "wrong original operator", 403)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": f.operator["PKI_OPERATOR_USER_ID"], "email": got["email"]}, "tokens": map[string]string{"access_token": "isolated-device-operator"}})
			return
		}
		state := map[string]any{}
		if readExecutionJSON(d.path, &state) != nil {
			http.Error(w, "native registry unavailable", 500)
			return
		}
		root := deploymentplan.Map(state["root"])
		if r.Method != http.MethodGet || root == nil || r.URL.Path != "/v1/platform/pki/issuers/"+deploymentplan.String(root["issuer_id"]) || r.Header.Get("Authorization") != "Bearer isolated-device-operator" {
			http.Error(w, "unselected native issuer", 403)
			return
		}
		json.NewEncoder(w).Encode(root)
	}))
	t.Cleanup(server.Close)
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), f.cfg.Environment, f.operator["PKI_OPERATOR_USER_ID"])
	if err != nil {
		t.Fatal(err)
	}
	client.Execution = f.e
	if _, err := client.Authenticate(t.Context(), f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal(err)
	}
	f.runtime.PKIClient = client
	return d
}

func deviceRootRetirementScript(environment string) string {
	return "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\nIFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao delete auth/kubernetes/role/pki-device-bootstrap-" + environment + " >/dev/null\nbao policy delete create-device-root-bootstrap >/dev/null\nprintf '{\"roles\":'\nbao list -format=json auth/kubernetes/role\nprintf ',\"policies\":'\nbao policy list -format=json\nprintf '}'\n"
}

func (d *deviceRootCoordinatorContract) state(t *testing.T) map[string]any {
	t.Helper()
	var s map[string]any
	if err := readExecutionJSON(d.path, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func (d *deviceRootCoordinatorContract) save(t *testing.T, s map[string]any) {
	t.Helper()
	if err := os.WriteFile(d.path, mustProviderRecoveryJSON(s), 0600); err != nil {
		t.Fatal(err)
	}
}

func (d *deviceRootCoordinatorContract) apply(t *testing.T) environmentCreateBootstrapApply {
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage != "device-root-ready" && stage != "device-root-active" {
			return errors.New("unexpected Device stage")
		}
		var id string
		jobs := 0
		for _, def := range defs {
			if def.Object["kind"] == "Job" {
				jobs++
				containers := certIssuerObjectList(environmentPKIPod(def.Object)["containers"])
				if len(containers) != 1 {
					return errors.New("ambiguous native Device Job")
				}
				args := deploymentplan.Map(containers[0])["args"].([]any)
				if len(args) != 2 || args[0] != "bootstrap-device-root" {
					return errors.New("wrong native Device command")
				}
				id = deploymentplan.String(args[1])
			}
		}
		if jobs != 1 || !environmentBootstrapOwnerUUID.MatchString(id) {
			return errors.New("native Device UUID missing")
		}
		if stage == "device-root-ready" && d.ready != 0 || stage == "device-root-active" && (d.ready != 1 || d.active != 0 || id != d.root.ID) {
			return errors.New("native generation repeated or activation changed UUID")
		}
		if d.mode == "before-ready" {
			return errors.New("isolated Device Job interruption")
		}
		state := d.state(t)
		if stage == "device-root-active" && deploymentplan.Digest(state["acks"]) != deploymentplan.Digest([]string{"pkibroker", "video-cloud-api"}) {
			return errors.New("Device activation preceded both native ACKs")
		}
		var native map[string]any
		if readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native) != nil {
			return errors.New("native Device capability unavailable")
		}
		policies, roles := deploymentplan.Map(native["policies"]), deploymentplan.Map(native["roles"])
		role := deploymentplan.Map(roles["pki-device-bootstrap-"+d.f.cfg.Environment])
		if !strings.Contains(deploymentplan.String(policies["create-device-root-bootstrap"]), "root/generate/existing") || policies["create-device-controller"] == nil || policies["create-device-signer"] == nil || deploymentplan.Digest(role["bound_service_account_names"]) != deploymentplan.Digest([]string{"pki-device-bootstrap"}) || deploymentplan.Digest(role["bound_service_account_namespaces"]) != deploymentplan.Digest([]string{d.f.p.Plan.Stack + "-video-cloud"}) || deploymentplan.Digest(role["token_policies"]) != deploymentplan.Digest([]string{"create-device-root-bootstrap"}) || role["token_no_default_policy"] != true {
			return errors.New("Device native generation requires its exact installed exclusive capability")
		}
		d.f.acceptedHistoricalStage(t, stage, defs)
		if stage == "device-root-ready" {
			certificate, _, encoded := coreContractCertificate(t, "isolated-native-device-root", true, nil, nil, 24*time.Hour)
			doc := deploymentplan.Object{"issuer_id": id, "issuer_version": float64(1), "environment": d.f.cfg.Environment, "trust_domain": "device", "kind": "root", "status": "ready", "signer_provider": "openbao", "signer_reference": "pki-issuers/device/" + id + "/v1", "certificate_pem": encoded, "chain_pem": encoded, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(certificate.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(encoded))}
			var err error
			d.root, err = environmentCreateRegisteredIssuerFromMap(doc, "")
			if err != nil {
				return err
			}
			d.ready++
			state["root"] = doc
		} else {
			doc := deploymentplan.Clone(d.root.Document)
			doc["status"] = "active"
			if d.mode == "active-identity-drift" {
				cert, _, encoded := coreContractCertificate(t, "unexpected-device-root", true, nil, nil, 24*time.Hour)
				doc["certificate_pem"], doc["chain_pem"] = encoded, encoded
				doc["certificate_fingerprint_sha256"], doc["trust_bundle_version"] = environmentPreflightBytesSHA256(cert.Raw), environmentPreflightBytesSHA256([]byte(encoded))
			}
			d.active++
			state["root"] = doc
		}
		d.save(t, state)
		return nil
	}
}

func (d *deviceRootCoordinatorContract) distribute(t *testing.T) func(environmentCreateRegisteredIssuer) error {
	return func(root environmentCreateRegisteredIssuer) error {
		if d.ready != 1 || d.active != 0 || deploymentplan.Digest(root) != deploymentplan.Digest(d.root) {
			return errors.New("distribution before native READY identity")
		}
		if d.mode == "before-consumers" {
			return errors.New("isolated consumer interruption")
		}
		f := d.f
		defs, err := renderEnvironmentCreateApplicationOwners(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
		if err != nil {
			return err
		}
		defs, err = configureEnvironmentCreateDeviceConsumers(f.cfg, f.p.Plan.Bootstrap, root, defs)
		if err != nil {
			return err
		}
		defs = environmentCreateApplicationDefinitionsFor(defs, "video-cloud-api-pki", "mqtt-pki")
		f.acceptedHistoricalStage(t, "device-root-trust", defs)
		d.distributions++
		state := d.state(t)
		receipts := []map[string]any{}
		acks := []string{}
		for _, target := range historicalBundleConsumers(f.p.Plan.Stack) {
			if target.domain != "device" {
				continue
			}
			// The bootstrap-trust processes persist under their original enrolled
			// identity claims. The later managed Apps stage introduces pki-state
			// for API trust; that later-volume role is not an initial prerequisite.
			target.stateVolume = "service-identity"
			config, claim, err := historicalBundleMountedManifest(f.native.objects, target)
			if err != nil {
				return err
			}
			var refs []struct {
				ID      string `json:"issuer_id"`
				Version string `json:"trust_bundle_version"`
			}
			data := deploymentplan.Map(config["data"])
			if json.Unmarshal([]byte(deploymentplan.String(data["bundles.json"])), &refs) != nil || len(refs) != 1 || refs[0].ID != root.ID || refs[0].Version != root.BundleVersion || data["roots.pem"] != root.Certificate || deploymentplan.Map(claim["metadata"])["uid"] == nil {
				return errors.New("consumer did not mount original READY bundle")
			}
			cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
			if err != nil || cert.CheckSignatureFrom(cert) != nil || environmentPreflightBytesSHA256(cert.Raw) != root.RootFingerprint {
				return errors.New("consumer rejected Device root signature")
			}
			cache := filepath.Join(d.cache, target.consumer+".pem")
			if err := os.WriteFile(cache, []byte(root.ChainPEM), 0600); err != nil {
				return err
			}
			if string(readTestFile(t, cache)) != root.ChainPEM {
				return errors.New("consumer cache readback differs")
			}
			if d.mode == "missing-ack" && target.consumer == "video-cloud-api" {
				continue
			}
			acks = append(acks, target.consumer)
			receipts = append(receipts, map[string]any{"cache": cache, "owner_id": target.owner, "owner_uid": deploymentplan.String(deploymentplan.Map(f.native.objects[target.owner]["metadata"])["uid"]), "owner_spec": f.native.objects[target.owner]["spec"], "claim_id": deploymentplan.ID(claim), "claim_uid": deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), "config_id": deploymentplan.ID(config), "config_uid": deploymentplan.Map(config["metadata"])["uid"], "config_data": config["data"]})
		}
		slices.Sort(acks)
		state["acks"], state["receipts"] = acks, receipts
		d.save(t, state)
		return nil
	}
}

func TestDeviceRootCoordinatorUsesOneNativeAuthorityAndRetiresOnlyBootstrapCapability(t *testing.T) {
	for _, mode := range []string{"complete", "before-ready", "before-consumers", "missing-ack", "active-identity-drift", "unsafe-policy", "retirement-readback"} {
		t.Run(mode, func(t *testing.T) {
			d := newDeviceRootCoordinatorContract(t, mode)
			f := d.f
			prefix := deploymentplan.Digest(f.e.Journal.Entries)
			prefixLength := len(f.e.Journal.Entries)
			identity := createBootstrapStateSnapshot(t, filepath.Join(f.store.Root, "pki"))
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			root, err := executeEnvironmentCreateDeviceRoot(ctx, f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, f.runtime, d.apply(t), d.distribute(t))
			want := map[string]string{"before-ready": "isolated Device Job interruption", "before-consumers": "isolated consumer interruption", "missing-ack": "timeout ended", "active-identity-drift": "activation changed", "unsafe-policy": "exposes bootstrap Root generation", "retirement-readback": "role removal cannot be verified"}
			if mode == "complete" {
				if err != nil || root.ID != d.root.ID || root.Status != "active" || root.Certificate != d.root.Certificate {
					t.Fatal("actual Device coordinator failed", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want[mode]) {
				t.Fatalf("expected precise %s refusal, got %v", mode, err)
			}
			if deploymentplan.Digest(f.e.Journal.Entries[:prefixLength]) != prefix || deploymentplan.Digest(createBootstrapStateSnapshot(t, filepath.Join(f.store.Root, "pki"))) != deploymentplan.Digest(identity) {
				t.Fatal("Device lifecycle replaced earlier history or Service custody")
			}
			state := d.state(t)
			wantCounts := map[string][]int{
				"complete": {1, 1, 1, 1}, "before-ready": {0, 0, 0, 0},
				"before-consumers": {1, 0, 0, 0}, "missing-ack": {1, 0, 1, 0},
				"active-identity-drift": {1, 1, 1, 0}, "unsafe-policy": {0, 0, 0, 0},
				"retirement-readback": {1, 1, 1, 1},
			}
			if got := []int{d.ready, d.active, d.distributions, int(state["retirements"].(float64))}; !slices.Equal(got, wantCounts[mode]) {
				t.Fatalf("refusal did not reach its intended native boundary: got=%v want=%v", got, wantCounts[mode])
			}
			if mode == "complete" {
				if d.ready != 1 || d.active != 1 || d.distributions != 1 || state["retirements"] != float64(1) {
					t.Fatal("native lifecycle replay or missing retirement")
				}
				for _, key := range []string{"device-root-bootstrap", "device-root-bootstrap-retire"} {
					entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
					if entry == nil || entry.State != "completed" {
						t.Fatal("original lifecycle did not close", key)
					}
				}
				var native map[string]any
				if readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native) != nil {
					t.Fatal("native policy state unavailable")
				}
				policies, roles := deploymentplan.Map(native["policies"]), deploymentplan.Map(native["roles"])
				if policies["create-device-root-bootstrap"] != nil || roles["pki-device-bootstrap-dev"] != nil || policies["create-device-controller"] == nil || policies["create-device-signer"] == nil || roles["pki-controller-dev"] == nil {
					t.Fatal("bootstrap capability survived or runtime policy was lost")
				}
			} else if entry := createOpenBaoACKCompletionEntry(f.e.Journal, "device-root-bootstrap"); entry == nil || entry.State == "completed" {
				t.Fatal("interrupted lifecycle falsely completed")
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			nativeBefore := readTestFile(t, os.Getenv("RTK_HISTORICAL_SESSION_BAO"))
			counts := []int{d.ready, d.active, d.distributions}
			if _, err := executeEnvironmentCreateDeviceRoot(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, f.runtime, d.apply(t), d.distribute(t)); err == nil {
				t.Fatal("fresh automatic retry regenerated original authority")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if nativeBefore != readTestFile(t, os.Getenv("RTK_HISTORICAL_SESSION_BAO")) || !slices.Equal(counts, []int{d.ready, d.active, d.distributions}) {
				t.Fatal("unqualified retry repeated native writes")
			}
		})
	}
}
