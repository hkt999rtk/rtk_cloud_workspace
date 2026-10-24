package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The selected completion fixture disables Account registration. This test
// proves that its Video exporter preserves unrelated native protocol ownership;
// it deliberately does not fabricate a missing managed Account server identity.
func TestHistoricalAPIExportReinstallationPreservesNativeDispatchAndSignedOwners(t *testing.T) {
	f, _, _, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	if f.cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "false" {
		t.Fatal("completion fixture changed its originally selected Account registration scope")
	}
	clients, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "isolated-api-interop-client-enrollment", clients)
	identities := map[string]historicalAPIIdentity{}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		identities[name] = historicalAPIServerIdentity(t, f, f.service, name)
	}
	originalMaterial := deploymentplan.Digest(identities)
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	marker := filepath.Join(dir, "unrelated-export-refused")
	accountName := "account-manager." + f.p.Plan.Stack + "-account-manager.svc.cluster.local"
	script := `#!/usr/bin/env python3
import os,sys
a=sys.argv[1:]
if 'original-native-dispatch-probe' in a:print('original-native-dispatch');sys.exit()
if 'export-server' in a and a[-1]==ACCOUNT_NAME:
 with open(MARKER,'a') as output:output.write('unrelated export reached its original native owner\n')
 sys.exit(51)
os.execv(PARENT,[PARENT]+a)
`
	script = strings.ReplaceAll(script, "PARENT", strconv.Quote(parent))
	script = strings.ReplaceAll(script, "ACCOUNT_NAME", strconv.Quote(accountName))
	script = strings.ReplaceAll(script, "MARKER", strconv.Quote(marker))
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installHistoricalAPIExportProtocol(t, f, identities)
	installHistoricalAPIExportProtocol(t, f, identities)
	// A bounded request detects mutable-parent recursion without waiting for a
	// complete bootstrap. Its actual predecessor must answer exactly once.
	probeCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	raw, err := f.backend.command(probeCtx, nil, "original-native-dispatch-probe")
	cancel()
	if err != nil || strings.TrimSpace(string(raw)) != "original-native-dispatch" {
		t.Fatal("reinstalled wrapper lost its immutable native predecessor", err)
	}
	if err := publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, historicalAPIApply(t, f)); err != nil {
		t.Fatal("actual two signed original API exports through repeated wrapper", err)
	}
	if deploymentplan.Digest(identities) != originalMaterial {
		t.Fatal("export wrapper changed original private identities")
	}
	for name, identity := range identities {
		p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/server-secret-"+name)
		if err != nil || !j.Accepted || len(p.Resources) != 1 {
			t.Fatal("actual serving Secret receipt missing", name, err)
		}
		data := deploymentplan.Map(p.Resources[0].Desired["data"])
		certificate, certErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.crt"]))
		key, keyErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.key"]))
		if certErr != nil || keyErr != nil || string(certificate) != identity.Material.Certificate || string(key) != identity.Material.Key {
			t.Fatal("original signed API material changed", name, certErr, keyErr)
		}
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	_, err = f.backend.command(ctx, nil, "--namespace", f.p.Plan.Stack+"-account-manager", "exec", "pod/original-account-custody", "--container", "custody", "--", "/app/serviceidentity-bootstrap", "export-server", "/var/lib/pki/service/private/server.json", "service", "service:account-manager", f.service.Root.RootFingerprint, accountName)
	cancel()
	if err == nil {
		t.Fatal("unavailable unrelated native identity was manufactured")
	}
	if got := readTestFile(t, marker); got != "unrelated export reached its original native owner\n" {
		t.Fatal("unrelated exporter was swallowed or recursively delegated")
	}
	// A selected Video owner whose original native row disappears must fail
	// within that scope, rather than falling through to an unrelated exporter.
	selected := identities["video-cloud-api-pki"]
	f.protocol["historical_api_exports"] = map[string]historicalAPIIdentity{"video-cloud-api-app-pki": identities["video-cloud-api-app-pki"]}
	f.publish(t)
	ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
	_, err = f.backend.command(ctx, nil, "export-server", selected.Material.Name)
	cancel()
	if err == nil {
		t.Fatal("missing selected Video export accepted")
	}
	if got := readTestFile(t, marker); got != "unrelated export reached its original native owner\n" {
		t.Fatal("missing selected Video identity delegated to unrelated owner")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestHistoricalLateLogInventoryPreservesExactPodSelectors(t *testing.T) {
	f, late, _, _ := newHistoricalLateLogNativeContract(t)
	ns := strings.Split(late.LogProducer.Original.ID, "/")[1]
	selector := "app.kubernetes.io/name=video-cloud-logingester"
	objects := f.protocol["objects"].(map[string]deploymentplan.Object)
	expected := map[string]bool{}
	total := 0
	for id, object := range objects {
		meta := deploymentplan.Map(object["metadata"])
		if object["kind"] != "Pod" || meta["namespace"] != ns {
			continue
		}
		total++
		if deploymentplan.Map(meta["labels"])["app.kubernetes.io/name"] == "video-cloud-logingester" {
			expected[id] = true
		}
	}
	if len(expected) == 0 || total <= len(expected) {
		t.Fatal("selector regression needs genuine distinct native Pod owners")
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	for _, flag := range []string{"-l", "--selector"} {
		for _, selected := range []string{selector, "app.kubernetes.io/name=absent-original-owner"} {
			raw, err := f.backend.command(t.Context(), nil, "get", "pods", "--namespace", ns, flag, selected, "-o", "json")
			var list struct {
				Items []deploymentplan.Object `json:"items"`
			}
			if err != nil || json.Unmarshal(raw, &list) != nil {
				t.Fatal("native Pod inventory query failed", flag, err)
			}
			got := map[string]bool{}
			for _, pod := range list.Items {
				got[deploymentplan.ID(pod)] = true
			}
			if selected == selector {
				if deploymentplan.Digest(got) != deploymentplan.Digest(expected) {
					t.Fatal("Log wrapper dropped native Pod selector", flag)
				}
			} else if len(got) != 0 {
				t.Fatal("foreign selector received original namespace Pods", flag)
			}
		}
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}
