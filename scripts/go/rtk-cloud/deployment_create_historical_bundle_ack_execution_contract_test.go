package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These are native executable roles, independently of the requested policy
// roster. A role earns a receipt only by resolving its actual mounted manifest,
// original CA bytes, retained owner and durable claim and reading its cache back.
type historicalBundleConsumer struct {
	domain, consumer, owner, container, manifestEnv, stateVolume string
}

func historicalBundleConsumers(stack string) []historicalBundleConsumer {
	v, a := "Deployment/"+stack+"-video-cloud/", "Deployment/"+stack+"-account-manager/"
	return []historicalBundleConsumer{
		{"service", "account-manager", a + "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST", "service-state"},
		{"service", "certissuer", v + "certissuer", "certissuer", "CERT_ISSUER_SERVICE_CLIENT_BUNDLE_MANIFEST", "service-state"},
		{"service", "pki-controller", v + "pki-controller", "pki-controller", "PKI_SERVICE_CLIENT_BUNDLE_MANIFEST", "service-state"},
		{"mqtt", "video-cloud-api", v + "video-cloud-api", "app", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST", "mqtt-bundle-state"},
		{"mqtt", "video-cloud-logingester", v + "video-cloud-logingester", "app", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST", "mqtt-bundle-state"},
		{"openbao_tls", "certissuer", v + "certissuer", "certissuer", "OPENBAO_SERVER_BUNDLE_MANIFEST", "service-state"},
		{"openbao_tls", "pki-controller", v + "pki-controller", "pki-controller", "OPENBAO_SERVER_BUNDLE_MANIFEST", "service-state"},
		{"device", "pkibroker", v + "mqtt-pki", "pkibroker", "", "pki-state"},
		{"device", "video-cloud-api", v + "video-cloud-api-pki", "app", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST", "pki-state"},
		{"app", "pkibroker", v + "mqtt-pki", "pkibroker", "", "pki-state"},
		{"app", "video-cloud-api", v + "video-cloud-api-app-pki", "app", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST", "pki-state"},
	}
}

type historicalBundleReceipt struct {
	target                                                                           historicalBundleConsumer
	issuer                                                                           environmentCreateRegisteredIssuer
	ownerUID, configID, configUID, configSHA, claimID, claimUID, cache, selectionSHA string
}

type historicalBundleACKProtocol struct {
	fixture   *acceptedCoreExecutionContract
	receipts  []historicalBundleReceipt
	cacheRoot string
}

func historicalBundleMountedManifest(objects map[string]deploymentplan.Object, target historicalBundleConsumer) (deploymentplan.Object, deploymentplan.Object, error) {
	owner := objects[target.owner]
	if owner == nil {
		return nil, nil, errors.New("native bundle owner absent")
	}
	pod := environmentPKIPod(owner)
	c := managedNamedField(pod["containers"], target.container)
	if c == nil {
		return nil, nil, errors.New("native bundle consumer absent")
	}
	path := mqttFoundationEnv(c, target.manifestEnv)
	if target.manifestEnv == "" {
		prefix := "PKI_BROKER_" + strings.ToUpper(target.domain)
		if mqttFoundationEnv(c, prefix+"_BUNDLE_ACK_ENABLED") != "true" || mqttFoundationEnv(c, prefix+"_ROOT_ID") == "" {
			return nil, nil, errors.New("native broker bundle acknowledgment disabled")
		}
		path = filepath.Join(filepath.Dir(mqttFoundationEnv(c, prefix+"_ROOTS")), "bundles.json")
	}
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("native bundle manifest path missing")
	}
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return nil, nil, err
	}
	var config deploymentplan.Object
	for _, value := range certIssuerObjectList(c["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		base := deploymentplan.String(mount["mountPath"])
		file := ""
		if sub := deploymentplan.String(mount["subPath"]); sub != "" && path == base {
			file = sub
		} else if mount["subPath"] == nil && strings.HasPrefix(path, strings.TrimSuffix(base, "/")+"/") {
			file = strings.TrimPrefix(path, strings.TrimSuffix(base, "/")+"/")
		}
		if file == "" {
			continue
		}
		source := deploymentplan.Map(volumes[deploymentplan.String(mount["name"])]["configMap"])
		if source == nil || mount["readOnly"] != true {
			return nil, nil, errors.New("native bundle manifest is not a read-only ConfigMap projection")
		}
		key := file
		if source["items"] != nil {
			key = ""
			for _, item := range certIssuerObjectList(source["items"]) {
				row := deploymentplan.Map(item)
				if row["path"] == file {
					if key != "" {
						return nil, nil, errors.New("duplicate native bundle projection")
					}
					key = deploymentplan.String(row["key"])
				}
			}
		}
		if key != "bundles.json" || config != nil {
			return nil, nil, errors.New("native bundle projection is missing or ambiguous")
		}
		ns := deploymentplan.String(deploymentplan.Map(owner["metadata"])["namespace"])
		config = objects["ConfigMap/"+ns+"/"+deploymentplan.String(source["name"])]
	}
	if config == nil {
		return nil, nil, errors.New("native mounted bundle ConfigMap absent")
	}
	var claim deploymentplan.Object
	for _, value := range certIssuerObjectList(c["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		if mount["name"] != target.stateVolume {
			continue
		}
		if mount["readOnly"] == true || mount["subPath"] != nil || !filepath.IsAbs(deploymentplan.String(mount["mountPath"])) {
			return nil, nil, errors.New("native bundle cache has no writable original durable mount")
		}
		volume := volumes[target.stateVolume]
		name := deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
		ns := deploymentplan.String(deploymentplan.Map(owner["metadata"])["namespace"])
		claim = objects["PersistentVolumeClaim/"+ns+"/"+name]
	}
	if claim == nil {
		return nil, nil, errors.New("native bundle cache original claim absent")
	}
	return config, claim, nil
}

func historicalBundleRootSetting(target historicalBundleConsumer) (string, bool) {
	switch target.domain {
	case "service":
		if target.consumer == "account-manager" {
			return "PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256", false
		}
		if target.consumer == "certissuer" {
			return "CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256", false
		}
		return "PKI_SERVICE_CLIENT_ROOT_SHA256", false
	case "mqtt":
		return "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256", false
	case "openbao_tls":
		return "OPENBAO_SERVER_BUNDLE_ROOT_SHA256", false
	default:
		if target.consumer == "pkibroker" {
			return "PKI_BROKER_" + strings.ToUpper(target.domain) + "_ROOT_ID", true
		}
		return "VIDEO_CLOUD_AUTH_" + strings.ToUpper(target.domain) + "_ROOT_TRUST_ROOT_ID", true
	}
}

func historicalBundleSelection(owner deploymentplan.Object, target historicalBundleConsumer) string {
	container := managedNamedField(environmentPKIPod(owner)["containers"], target.container)
	settings := deploymentplan.Object{}
	for _, raw := range certIssuerObjectList(container["env"]) {
		setting := deploymentplan.Map(raw)
		name := deploymentplan.String(setting["name"])
		if strings.Contains(name, "BUNDLE") || strings.Contains(name, "ROOT_ID") || strings.Contains(name, "ROOT_SHA256") || strings.Contains(name, "ROOTS") || strings.Contains(name, "ROOT_TRUST") || name == "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST" {
			settings[name] = setting
		}
	}
	return deploymentplan.Digest(settings)
}

func historicalBundleAuthority(f *acceptedCoreExecutionContract, id, version, domain string, config deploymentplan.Object) (environmentCreateRegisteredIssuer, error) {
	issuers, ok := f.protocol["issuers"].(map[string]any)
	if !ok {
		return environmentCreateRegisteredIssuer{}, errors.New("native authority registry absent")
	}
	doc := deploymentplan.Map(issuers[id])
	rootFingerprint := ""
	chainBytes := []byte(deploymentplan.String(doc["chain_pem"]))
	for len(chainBytes) > 0 {
		block, rest := pem.Decode(chainBytes)
		if block == nil {
			break
		}
		rootFingerprint = environmentPreflightBytesSHA256(block.Bytes)
		chainBytes = rest
	}
	registered, err := environmentCreateRegisteredIssuerFromMap(doc, rootFingerprint)
	if err != nil || registered.Environment != f.cfg.Environment || registered.Domain != domain || registered.ID != id || registered.Status != "active" || registered.BundleVersion != version || environmentPreflightBytesSHA256([]byte(registered.ChainPEM)) != version {
		return registered, errors.New("native bundle differs from original active authority")
	}
	var chain []*x509.Certificate
	remaining := []byte(registered.ChainPEM)
	for len(strings.TrimSpace(string(remaining))) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return registered, errors.New("native issuer chain malformed")
		}
		cert, e := x509.ParseCertificate(block.Bytes)
		if e != nil || !cert.IsCA || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
			return registered, errors.New("native issuer chain is not current CA material")
		}
		chain = append(chain, cert)
		remaining = rest
	}
	if len(chain) == 0 || chain[len(chain)-1].CheckSignatureFrom(chain[len(chain)-1]) != nil {
		return registered, errors.New("native bundle root signature differs")
	}
	for i := 0; i < len(chain)-1; i++ {
		if chain[i].CheckSignatureFrom(chain[i+1]) != nil {
			return registered, errors.New("native bundle intermediate signature differs")
		}
	}
	root := chain[len(chain)-1]
	if environmentPreflightBytesSHA256(root.Raw) != registered.RootFingerprint {
		return registered, errors.New("native bundle root pin differs")
	}
	rootFound := false
	for _, key := range []string{"root.pem", "roots.pem", "client-roots.pem"} {
		raw := []byte(deploymentplan.String(deploymentplan.Map(config["data"])[key]))
		for len(raw) > 0 {
			block, rest := pem.Decode(raw)
			if block == nil {
				break
			}
			if slices.Equal(block.Bytes, root.Raw) {
				rootFound = true
			}
			raw = rest
		}
	}
	if !rootFound {
		return registered, errors.New("mounted bundle roots do not contain original authority")
	}
	return registered, nil
}

func (a *historicalBundleACKProtocol) observe() error {
	f := a.fixture
	objects := historicalNativeInventory(f.native.objects)
	for _, target := range historicalBundleConsumers(f.p.Plan.Stack) {
		if objects[target.owner] == nil {
			continue
		}
		config, claim, err := historicalBundleMountedManifest(objects, target)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", target.domain, target.consumer, err)
		}
		ownerUID := deploymentplan.String(deploymentplan.Map(objects[target.owner]["metadata"])["uid"])
		configUID := deploymentplan.String(deploymentplan.Map(config["metadata"])["uid"])
		claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(ownerUID) || !environmentBootstrapOwnerUUID.MatchString(configUID) || !environmentBootstrapOwnerUUID.MatchString(claimUID) {
			return errors.New("bundle receipt requires original native owner UIDs")
		}
		var refs []struct {
			ID      string `json:"issuer_id"`
			Version string `json:"trust_bundle_version"`
		}
		if decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(config["data"])["bundles.json"])), &refs) != nil || len(refs) == 0 {
			return errors.New("native bundle manifest is malformed")
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			if seen[ref.ID] {
				return errors.New("duplicate native issuer in bundle manifest")
			}
			seen[ref.ID] = true
			issuer, err := historicalBundleAuthority(f, ref.ID, ref.Version, target.domain, config)
			if err != nil {
				return err
			}
			if target.manifestEnv == "" {
				c := managedNamedField(environmentPKIPod(objects[target.owner])["containers"], target.container)
				rootID := mqttFoundationEnv(c, "PKI_BROKER_"+strings.ToUpper(target.domain)+"_ROOT_ID")
				if issuer.Kind == "root" && issuer.ID != rootID {
					return errors.New("native broker root selection differs")
				}
			}
			receipt := historicalBundleReceipt{target: target, issuer: issuer, ownerUID: ownerUID, configID: deploymentplan.ID(config), configUID: configUID, configSHA: deploymentplan.Digest(config["data"]), claimID: deploymentplan.ID(claim), claimUID: claimUID}
			receipt.selectionSHA = historicalBundleSelection(objects[target.owner], target)
			receipt.cache = filepath.Join(a.cacheRoot, deploymentplan.Digest([]string{ownerUID, claimUID, target.domain, issuer.ID})+".pem")
			if err := os.WriteFile(receipt.cache, []byte(issuer.ChainPEM), 0600); err != nil {
				return err
			}
			if err := a.validate(receipt); err != nil {
				return err
			}
			a.receipts = append(a.receipts, receipt)
		}
	}
	return nil
}

func (a *historicalBundleACKProtocol) validate(r historicalBundleReceipt) error {
	objects := historicalNativeInventory(a.fixture.native.objects)
	owner := objects[r.target.owner]
	if deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"]) != r.ownerUID || deploymentplan.Map(owner["metadata"])["deletionTimestamp"] != nil || historicalBundleSelection(owner, r.target) != r.selectionSHA {
		return errors.New("native bundle consumer owner changed")
	}
	config, claim, err := historicalBundleMountedManifest(objects, r.target)
	if err != nil {
		return err
	}
	if deploymentplan.ID(config) != r.configID || deploymentplan.Map(config["metadata"])["uid"] != r.configUID || deploymentplan.Digest(config["data"]) != r.configSHA || deploymentplan.ID(claim) != r.claimID || deploymentplan.Map(claim["metadata"])["uid"] != r.claimUID {
		return errors.New("native bundle mount, claim or authority bytes changed")
	}
	pods := 0
	for _, pod := range objects {
		if pod["kind"] != "Pod" {
			continue
		}
		id, err := environmentPodController(pod, objects)
		if err == nil && id == r.target.owner {
			if !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])) || deploymentplan.Map(pod["status"])["phase"] != "Running" {
				return errors.New("native bundle consumer Pod is not current")
			}
			pods++
		}
	}
	if pods == 0 {
		return errors.New("native bundle consumer has no owned running Pod")
	}
	current, err := historicalBundleAuthority(a.fixture, r.issuer.ID, r.issuer.BundleVersion, r.target.domain, config)
	if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(r.issuer) {
		return errors.New("original bundle registration changed")
	}
	container := managedNamedField(environmentPKIPod(owner)["containers"], r.target.container)
	rootSetting, rootID := historicalBundleRootSetting(r.target)
	selected := mqttFoundationEnv(container, rootSetting)
	if rootID {
		doc := deploymentplan.Map(a.fixture.protocol["issuers"].(map[string]any)[selected])
		if doc["kind"] != "root" || doc["trust_domain"] != r.target.domain || doc["certificate_fingerprint_sha256"] != r.issuer.RootFingerprint {
			return errors.New("native bundle consumer selected a different registered root")
		}
	} else if selected != r.issuer.RootFingerprint {
		return errors.New("native bundle consumer root fingerprint differs")
	}
	raw, err := readBoundedEnvironmentFile(r.cache, true)
	if err != nil || string(raw) != r.issuer.ChainPEM || environmentPreflightBytesSHA256(raw) != r.issuer.BundleVersion {
		return errors.New("native consumed bundle cache missing or changed")
	}
	return nil
}

var historicalBundleACKQuery = regexp.MustCompile(`^SELECT COALESCE\(json_agg\(consumer_id ORDER BY consumer_id\),'\[\]'::json\) FROM pki_bundle_acknowledgments WHERE issuer_id='([a-f0-9-]{36})' AND bundle_version='([a-f0-9]{64})'$`)

func installHistoricalBundleACKProtocol(t *testing.T, f *acceptedCoreExecutionContract) *historicalBundleACKProtocol {
	t.Helper()
	a := &historicalBundleACKProtocol{fixture: f, cacheRoot: t.TempDir()}
	if err := a.observe(); err != nil {
		t.Fatal("native bundle receipt production", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args []string
		decoder := json.NewDecoder(r.Body)
		if r.Method != http.MethodPost || decoder.Decode(&args) != nil || len(args) == 0 {
			http.Error(w, "invalid native bundle query", 400)
			return
		}
		match := historicalBundleACKQuery.FindStringSubmatch(args[len(args)-1])
		if match == nil || !slices.Contains(args, "registry-read") || !slices.Contains(args, "PGOPTIONS=-c default_transaction_read_only=on") {
			http.Error(w, "unselected native bundle query", 400)
			return
		}
		var ids []string
		for _, receipt := range a.receipts {
			if receipt.issuer.ID != match[1] || receipt.issuer.BundleVersion != match[2] {
				continue
			}
			if err := a.validate(receipt); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			ids = append(ids, receipt.target.consumer)
		}
		slices.Sort(ids)
		if ids == nil {
			ids = []string{}
		}
		_ = json.NewEncoder(w).Encode(slices.Compact(ids))
	}))
	t.Cleanup(server.Close)
	previous := os.Getenv("RTK_CLOUD_KUBECTL")
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys,urllib.request
args=sys.argv[1:]
if 'registry-read' not in args or not args or 'pki_bundle_acknowledgments' not in args[-1]:os.execv(%q,[%q]+args)
req=urllib.request.Request(%q,data=json.dumps(args).encode(),headers={'Content-Type':'application/json'})
try:
 with urllib.request.urlopen(req,timeout=30) as r:sys.stdout.buffer.write(r.read())
except Exception as e:sys.exit(str(e))
`, previous, previous, server.URL)
	path := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return a
}

// A bounded native-interface fixture: real signed authorities and actual
// accepted child writes create the consumers. It does not claim the historical
// whole-deployment constructor or public preflight has completed.
func newHistoricalBundleNativeContract(t *testing.T) (*acceptedCoreExecutionContract, map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	f := newCoreExecutionContractAtBoundary(t, false, false, false, historicalCompleteDomainPolicy(t))
	// Independently compare these bindings with the maintained final Service
	// renderer before fixture scaffolding consumes the role table below.
	serviceDefinitions, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal("maintained native Service bundle consumers", err)
	}
	for _, target := range historicalBundleConsumers(f.p.Plan.Stack) {
		if target.domain != "service" {
			continue
		}
		object := createRecipeObject(t, serviceDefinitions, target.owner)
		container := managedNamedField(environmentPKIPod(object)["containers"], target.container)
		rootSetting, isID := historicalBundleRootSetting(target)
		if container == nil || mqttFoundationEnv(container, target.manifestEnv) != environmentCreateServiceTrust+"/bundles.json" || isID || mqttFoundationEnv(container, rootSetting) != f.service.Root.RootFingerprint {
			t.Fatal("native bundle role mapping differs from maintained Service renderer", target.consumer)
		}
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": f.service.Root}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": f.service.Intermediate}
	for ordinal, domain := range []string{"device", "app", "mqtt", "openbao_tls"} {
		root := f.trustAuthority(t, domain, ordinal+41, domain != "device")
		if domain == "device" {
			root = f.activateHistoricalReadyRoot(t, root, ordinal+41)
		}
		roots[domain] = root
		if domain != "device" {
			id := fmt.Sprintf("%08d-3333-4333-8333-333333333333", ordinal+41)
			var err error
			intermediates[domain], err = environmentCreateRegisteredIssuerFromMap(deploymentplan.Map(f.protocol["issuers"].(map[string]any)[id]), root.RootFingerprint)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	definitions := map[string]deploymentplan.Definition{}
	owners := map[string]deploymentplan.Object{}
	for _, target := range historicalBundleConsumers(f.p.Plan.Stack) {
		parts := strings.Split(target.owner, "/")
		ns, name := parts[1], parts[2]
		owner := owners[target.owner]
		if owner == nil {
			owner = deploymentplan.Clone(f.native.objects[target.owner])
			if owner == nil {
				owner = deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": name}}, "spec": deploymentplan.Object{"containers": []any{}}}}}
			}
			owners[target.owner] = owner
		}
		pod := environmentPKIPod(owner)
		c := managedNamedField(pod["containers"], target.container)
		if c == nil {
			c = deploymentplan.Object{"name": target.container, "image": f.p.Plan.Bootstrap.Image}
			pod["containers"] = append(certIssuerObjectList(pod["containers"]), c)
		}
		trustName, trustVolume, trustPath := "native-bundle-"+target.domain, "native-bundle-"+target.domain, "/run/native-bundle-"+target.domain
		root := roots[target.domain]
		var refs []map[string]string
		refs = append(refs, map[string]string{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion})
		if intermediate, ok := intermediates[target.domain]; ok {
			refs = append(refs, map[string]string{"issuer_id": intermediate.ID, "trust_bundle_version": intermediate.BundleVersion})
		}
		config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": trustName, "namespace": ns}, "data": deploymentplan.Object{"roots.pem": root.Certificate, "bundles.json": string(mustProviderRecoveryJSON(refs))}}
		definitions[deploymentplan.ID(config)] = deploymentplan.Definition{Phase: "configuration", Object: config}
		settings := map[string]string{}
		if target.manifestEnv != "" {
			settings[target.manifestEnv] = trustPath + "/bundles.json"
		} else {
			prefix := "PKI_BROKER_" + strings.ToUpper(target.domain)
			settings[prefix+"_BUNDLE_ACK_ENABLED"] = "true"
			settings[prefix+"_ROOT_ID"] = root.ID
			settings[prefix+"_ROOTS"] = trustPath + "/roots.pem"
		}
		rootSetting, rootID := historicalBundleRootSetting(target)
		settings[rootSetting] = root.RootFingerprint
		if rootID {
			settings[rootSetting] = root.ID
		}
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
		claimName := "native-bundle-" + name + "-" + target.stateVolume
		claim := environmentCreateServiceClaimDefinition(f.cfg, ns, claimName)
		definitions[deploymentplan.ID(claim.Object)] = claim
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount(trustVolume, trustPath, true), environmentCreateServiceMount(target.stateVolume, "/var/lib/native-"+target.stateVolume, false)})
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume(trustVolume, "configMap", trustName), environmentCreateServiceVolume(target.stateVolume, "persistentVolumeClaim", claimName)})
	}
	for id, owner := range owners {
		definitions[id] = deploymentplan.Definition{Phase: "workload", Object: owner}
	}
	var defs []deploymentplan.Definition
	for _, id := range slices.Sorted(maps.Keys(definitions)) {
		defs = append(defs, definitions[id])
	}
	f.stage(t, "kubernetes/native-bundle-consumer-contract", defs)
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	issuers := map[string]environmentCreateRegisteredIssuer{}
	for _, issuer := range roots {
		issuers[issuer.ID] = issuer
	}
	for _, issuer := range intermediates {
		issuers[issuer.ID] = issuer
	}
	return f, issuers
}

func TestHistoricalNativeBundleACKsRequireConsumedOriginalAuthority(t *testing.T) {
	f, issuers := newHistoricalBundleNativeContract(t)
	f.runtime.Backend = f.backend
	a := installHistoricalBundleACKProtocol(t, f)
	if len(issuers) != 9 || len(a.receipts) != 20 {
		t.Fatal("native original authority/consumer cardinality", len(issuers), len(a.receipts))
	}
	for id, issuer := range issuers {
		t.Run(issuer.Domain+"-"+issuer.Kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := waitEnvironmentCreateIssuerAcks(ctx, f.cfg, f.runtime, issuer, findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, issuer.Domain).BundleConsumers); err != nil {
				t.Fatal("actual consumed native bundle", id, err)
			}
		})
	}
	for _, mode := range []string{"missing-receipt", "foreign-owner", "foreign-claim", "wrong-mounted-bundle", "changed-cache", "changed-root-selection", "malformed-query"} {
		t.Run(mode, func(t *testing.T) {
			r := a.receipts[0]
			restore := func() {}
			switch mode {
			case "missing-receipt":
				old := slices.Clone(a.receipts)
				a.receipts = a.receipts[1:]
				restore = func() { a.receipts = old }
			case "foreign-owner", "foreign-claim":
				id := r.target.owner
				if mode == "foreign-claim" {
					id = r.claimID
				}
				old := deploymentplan.Clone(f.native.objects[id])
				deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] = "99999999-8888-4888-8888-999999999999"
				restore = func() { f.native.objects[id] = old }
			case "wrong-mounted-bundle":
				old := deploymentplan.Clone(f.native.objects[r.configID])
				deploymentplan.Map(f.native.objects[r.configID]["data"])["bundles.json"] = "[]"
				restore = func() { f.native.objects[r.configID] = old }
			case "changed-root-selection":
				old := deploymentplan.Clone(f.native.objects[r.target.owner])
				container := managedNamedField(environmentPKIPod(f.native.objects[r.target.owner])["containers"], r.target.container)
				key, _ := historicalBundleRootSetting(r.target)
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{key: strings.Repeat("f", 64)}))
				restore = func() { f.native.objects[r.target.owner] = old }
			case "changed-cache":
				old, err := os.ReadFile(r.cache)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.cache, []byte("foreign bundle"), 0600); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.WriteFile(r.cache, old, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer restore()
			if mode != "missing-receipt" && mode != "malformed-query" && a.validate(r) == nil {
				t.Fatal("native observer did not independently reject the selected fault", mode)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 180*time.Millisecond)
			defer cancel()
			var err error
			if mode == "malformed-query" {
				_, err = readEnvironmentCreateRegistryJSON(ctx, f.cfg, f.runtime, "SELECT consumer_id FROM pki_bundle_acknowledgments WHERE issuer_id='"+r.issuer.ID+"'")
			} else {
				err = waitEnvironmentCreateIssuerAcks(ctx, f.cfg, f.runtime, r.issuer, findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, r.issuer.Domain).BundleConsumers)
			}
			if err == nil {
				t.Fatal("native bundle receipt accepted absent or changed observed state", mode)
			}
		})
	}
}
