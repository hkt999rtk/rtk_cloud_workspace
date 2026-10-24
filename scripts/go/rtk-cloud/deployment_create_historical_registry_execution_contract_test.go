package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const historicalActiveIssuerRegistryQuery = "SELECT json_agg(json_build_object('id',id,'environment',environment,'domain',domain,'kind',kind,'status',status,'document',document) ORDER BY id) FROM pki_issuers"

// Project the actual native issuer registry, including any drift, rather than
// manufacturing rows from a source validator's expected local publications.
func installHistoricalActiveIssuerRegistry(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
a=sys.argv[1:]
prefix="SELECT json_agg(json_build_object('id',id,'environment',environment,'domain',domain,'kind',kind,'status',status,'document',document)"
if not any(v.startswith(prefix) for v in a):os.execv(os.environ['RTK_HISTORICAL_REGISTRY_ORIGINAL'],[os.environ['RTK_HISTORICAL_REGISTRY_ORIGINAL']]+a)
query=prefix+" ORDER BY id) FROM pki_issuers"
assert a[-2:]==['registry-read',query] and 'PGOPTIONS=-c default_transaction_read_only=on' in a
assert a[a.index('--namespace')+1]==os.environ['RTK_HISTORICAL_REGISTRY_NAMESPACE']
assert a[a.index('exec')+1]=='postgresql-0' and a[a.index('--container')+1]=='postgres'
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as r:d=json.load(r)
issuers=d['issuers'];rows=[]
for id,v in sorted(issuers.items()):
 rows.append({'id':id,'environment':v['environment'],'domain':v['trust_domain'],'kind':v['kind'],'status':v['status'],'document':v})
print(json.dumps(rows if rows else None))
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_REGISTRY_ORIGINAL", original)
	t.Setenv("RTK_HISTORICAL_REGISTRY_NAMESPACE", f.p.Plan.Stack+"-platform")
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHistoricalActiveIssuerRegistryProjectsActualNativeDocumentsReadOnly(t *testing.T) {
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	installHistoricalActiveIssuerRegistry(t, f)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	native := readTestFile(t, f.protocolPath)
	raw, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, f.runtime, historicalActiveIssuerRegistryQuery)
	var rows []struct {
		ID, Environment, Domain, Kind, Status string
		Document                              deploymentplan.Object
	}
	if err != nil || json.Unmarshal(raw, &rows) != nil || len(rows) != 9 {
		t.Fatal("actual nine signed native issuers cannot be read", err)
	}
	expected := map[string]environmentCreateRegisteredIssuer{}
	for _, cohort := range []map[string]environmentCreateRegisteredIssuer{roots, intermediates} {
		for _, issuer := range cohort {
			expected[issuer.ID] = issuer
		}
	}
	previous := ""
	for _, row := range rows {
		issuer, ok := expected[row.ID]
		if !ok || row.ID <= previous || row.Environment != f.cfg.Environment || row.Domain != issuer.Domain || row.Kind != issuer.Kind || row.Status != issuer.Status || deploymentplan.Digest(row.Document) != deploymentplan.Digest(issuer.Document) {
			t.Fatal("native SQL projection replaced an original document", row.ID)
		}
		previous = row.ID
		delete(expected, row.ID)
	}
	if len(expected) != 0 || readTestFile(t, f.protocolPath) != native {
		t.Fatal("registry read omitted or mutated original native issuers")
	}
	if _, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, f.runtime, historicalActiveIssuerRegistryQuery+" WHERE environment='dev'"); err == nil {
		t.Fatal("native all-authority inspection accepted a different SQL projection")
	}
	if _, err := f.backend.command(t.Context(), nil, "--namespace", f.p.Plan.Stack+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "registry-read", historicalActiveIssuerRegistryQuery); err == nil {
		t.Fatal("native authority inspection accepted a non-readonly database session")
	}
	// A changed native status must remain observable. The protocol must never
	// replace it with the expected local publication's ACTIVE value.
	selected := roots["service"]
	doc := deploymentplan.Clone(deploymentplan.Map(f.protocol["issuers"].(map[string]any)[selected.ID]))
	doc["status"] = "ready"
	f.protocol["issuers"].(map[string]any)[selected.ID] = doc
	f.publish(t)
	raw, err = readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, f.runtime, historicalActiveIssuerRegistryQuery)
	if err != nil || json.Unmarshal(raw, &rows) != nil {
		t.Fatal("native drift disappeared from the actual projection", err)
	}
	found := false
	for _, row := range rows {
		if row.ID == selected.ID {
			found = row.Status == "ready" && row.Document["status"] == "ready"
		}
	}
	if !found {
		t.Fatal("native projection masked a changed issuer status")
	}
	f.protocol["issuers"] = map[string]any{}
	f.publish(t)
	raw, err = readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, f.runtime, historicalActiveIssuerRegistryQuery)
	if err != nil || string(raw) != "null" {
		t.Fatal("empty native json_agg lost its SQL null result", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}
