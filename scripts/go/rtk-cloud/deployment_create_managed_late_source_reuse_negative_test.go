package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateSourceReuseNegativeFixture(t *testing.T) (secretStore, *environmentCreateExecution, deploymentConfig, map[string]string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	journal := managedLateExpiredRenewalNegativeJournal()
	journal.OwnerToken = strings.Repeat("f", 64)
	owner := environmentCreateOwner{Environment: "dev", Stack: journal.Stack, PlanSHA256: journal.PlanSHA256, OwnerToken: journal.OwnerToken, Journal: "deployment/create/executions/" + journal.PlanSHA256 + "/execution-isolated/journal.json"}
	e := &environmentCreateExecution{Store: store, Journal: journal, owner: owner}
	paths := map[string]string{"owner": "deployment/.execution-active/owner.json", "source": owner.Journal, "material": "deployment/create/openbao-recovery/" + strings.Repeat("7", 64) + "/qualification.json", "issuer": "pki/service/issuer.json", "private": "openbao/root-token"}
	values := map[string][]byte{"owner": mustProviderRecoveryJSON(owner), "source": mustProviderRecoveryJSON(journal), "material": []byte(`{"material":"original"}`), "issuer": []byte(`{"issuer":"original"}`), "private": []byte("synthetic-private-test-token")}
	for key, relative := range paths {
		managedLateSourceReuseNegativeWrite(t, store, relative, values[key])
	}
	cap, err := newManagedLateSourceReuse(store, e)
	if err != nil {
		t.Fatal("isolated structural source capability refused", err)
	}
	e.lateSourceReuse = cap
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	return store, e, cfg, paths
}

func managedLateSourceReuseNegativeWrite(t *testing.T, store secretStore, relative string, raw []byte) {
	t.Helper()
	path := filepath.Join(store.Root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLateSourceReuseReturnsIndependentExactObjects(t *testing.T) {
	store, e, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	const kind = "isolated-pure-history"
	value := map[string]any{"nested": map[string]any{"issuer": "original"}, "owners": []any{"original-owner"}}
	var miss map[string]any
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &miss); err != nil || hit {
		t.Fatal("structural scope alone granted authenticated history", err)
	}
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, value); err != nil {
		t.Fatal(err)
	}
	value["nested"].(map[string]any)["issuer"] = "caller-mutated-after-remember"
	var first map[string]any
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &first); err != nil || !hit || first["nested"].(map[string]any)["issuer"] != "original" {
		t.Fatal("remember retained mutable caller pointers", err)
	}
	first["nested"].(map[string]any)["issuer"] = "caller-mutated-return"
	var second map[string]any
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &second); err != nil || !hit || second["nested"].(map[string]any)["issuer"] != "original" {
		t.Fatal("returned object mutated retained source authority", err)
	}
	dirty := map[string]any{"untrusted": "must-not-survive"}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &dirty); err != nil || !hit {
		t.Fatal(err)
	}
	if _, exists := dirty["untrusted"]; exists {
		t.Fatal("prepopulated target merged untrusted fields into authenticated history")
	}
	type view struct {
		Original  string `json:"original"`
		Untrusted string `json:"untrusted,omitempty"`
	}
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind+"-struct", view{Original: "original"}); err != nil {
		t.Fatal(err)
	}
	dirtyStruct := view{Untrusted: "must-not-survive"}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind+"-struct", &dirtyStruct); err != nil || !hit || dirtyStruct.Untrusted != "" {
		t.Fatal("prepopulated struct retained unauthenticated omitted fields", err)
	}
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]any{"different": "source"}); err == nil {
		t.Fatal("same key silently replaced successful historical authority")
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		t.Fatal("pure caller mutations changed original source files", err)
	}
}

func TestManagedLateSourceReuseRejectsRawAndStructuralDrift(t *testing.T) {
	for _, mode := range []string{"source-bytes", "owner-bytes", "material-bytes", "material-format", "issuer-bytes", "private-bytes", "added-input", "removed-input", "new-prior-absence", "file-mode", "directory-mode", "private-parent-mode", "owner-parent-mode", "private-parent-symlink", "symlink", "source-object", "source-tail", "source-lease", "owner-object", "store-root"} {
		t.Run(mode, func(t *testing.T) {
			store, e, cfg, paths := managedLateSourceReuseNegativeFixture(t)
			const kind = "isolated-pure-history"
			if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"original": "history"}); err != nil {
				t.Fatal(err)
			}
			changedStore := store
			switch mode {
			case "source-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["source"], []byte(`{"changed":"source"}`))
			case "owner-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["owner"], []byte(`{"changed":"owner"}`))
			case "material-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["material"], []byte(`{"material":"modified"}`))
			case "material-format":
				managedLateSourceReuseNegativeWrite(t, store, paths["material"], []byte("{\"material\":\"original\"}\n"))
			case "issuer-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["issuer"], []byte(`{"issuer":"modified"}`))
			case "private-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["private"], []byte("private-stderr-canary-must-not-escape"))
			case "added-input":
				managedLateSourceReuseNegativeWrite(t, store, "pki/service/foreign.json", []byte(`{}`))
			case "removed-input":
				if err := os.Remove(filepath.Join(store.Root, paths["material"])); err != nil {
					t.Fatal(err)
				}
			case "new-prior-absence":
				managedLateSourceReuseNegativeWrite(t, store, "openbao/init.json", []byte(`{}`))
			case "file-mode":
				if err := os.Chmod(filepath.Join(store.Root, paths["material"]), 0640); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(filepath.Join(store.Root, "pki/service"), 0755); err != nil {
					t.Fatal(err)
				}
			case "private-parent-mode":
				if err := os.Chmod(filepath.Join(store.Root, "openbao"), 0755); err != nil {
					t.Fatal(err)
				}
			case "owner-parent-mode":
				if err := os.Chmod(filepath.Join(store.Root, "deployment/.execution-active"), 0755); err != nil {
					t.Fatal(err)
				}
			case "private-parent-symlink":
				original, foreign := filepath.Join(store.Root, "openbao"), filepath.Join(store.Root, "foreign-openbao")
				if err := os.Rename(original, foreign); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, original); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				p := filepath.Join(store.Root, paths["material"])
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(store.Root, paths["issuer"]), p); err != nil {
					t.Fatal(err)
				}
			case "source-object":
				e.Journal.Entries[0].Receipt = []byte(`{"changed":"source"}`)
			case "source-tail":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unadmitted-new-write", State: "pending"})
			case "source-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/ns/foreign", UID: "11111111-1111-4111-8111-111111111111", Holder: "foreign"})
			case "owner-object":
				e.owner.OwnerToken = strings.Repeat("e", 64)
			case "store-root":
				changedStore.Root = filepath.Join(store.Root, "foreign")
			}
			var got map[string]string
			hit, err := readManagedLateSourceReuse(changedStore, e, cfg, kind, &got)
			if err == nil || hit {
				t.Fatal("changed canonical/source authority reused", mode)
			}
			if strings.Contains(err.Error(), "private-stderr-canary") {
				t.Fatal("private file contents escaped manifest refusal")
			}
		})
	}
}

func TestManagedLateSourceReuseCannotCrossConfigurationCommandOrWrite(t *testing.T) {
	store, e, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	const kind = "isolated-pure-history"
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"original": "history"}); err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.Values = map[string]string{"CLOUD_STACK_NAME": "foreign"}
	var got map[string]string
	if hit, err := readManagedLateSourceReuse(store, e, changed, kind, &got); err != nil || hit {
		t.Fatal("configuration drift reused cached authority", err)
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, "native-observation-not-a-pure-source", &got); err != nil || hit {
		t.Fatal("historical result became authority for another namespace", err)
	}
	fresh := *e
	fresh.lateSourceReuse = nil
	if hit, err := readManagedLateSourceReuse(store, &fresh, cfg, kind, &got); err != nil || hit || managedLateSourceReuseActive(&fresh) {
		t.Fatal("next command inherited a prior command's proof", err)
	}
	cap, err := newManagedLateSourceReuse(store, &fresh)
	if err != nil {
		t.Fatal(err)
	}
	fresh.lateSourceReuse = cap
	if hit, err := readManagedLateSourceReuse(store, &fresh, cfg, kind, &got); err != nil || hit {
		t.Fatal("new empty command scope recovered previous values", err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var restored environmentCreateExecution
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("isolated serialized execution")
	}
	if hit, err := readManagedLateSourceReuse(store, &restored, cfg, kind, &got); err != nil || hit || managedLateSourceReuseActive(&restored) {
		t.Fatal("serialized caller data recreated private authority", err)
	}
	raw, err = json.Marshal(e.lateSourceReuse)
	if err != nil {
		t.Fatal(err)
	}
	var forged managedLateSourceReuse
	if json.Unmarshal(raw, &forged) != nil {
		t.Fatal("isolated serialized capability")
	}
	restored = *e
	restored.lateSourceReuse = &forged
	if hit, err := readManagedLateSourceReuse(store, &restored, cfg, kind, &got); err == nil || hit {
		t.Fatal("deserialized opaque scope granted authority")
	}
	invalidateManagedLateSourceReuse(e)
	if managedLateSourceReuseActive(e) || checkManagedLateSourceReuse(e) == nil {
		t.Fatal("write boundary did not invalidate old capability")
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &got); err != nil || hit {
		t.Fatal("historical authority reused after native/local mutation boundary", err)
	}
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"forged": "after-write"}); err != nil {
		t.Fatal("inactive scope should force ordinary derivation", err)
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &got); err != nil || hit {
		t.Fatal("remember reactivated invalidated capability", err)
	}
}

func TestManagedLateSourceReuseOutputAllowanceCannotHideOriginalInputs(t *testing.T) {
	store, e, cfg, paths := managedLateSourceReuseNegativeFixture(t)
	const kind = "isolated-pure-history"
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"original": "history"}); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(e.Journal)
	output := "deployment/create/openbao-recovery/" + strings.Repeat("9", 64)
	if err := allowManagedLateSourceReuseOutput(e, output); err != nil {
		t.Fatal("new unreferenced qualified-output directory refused", err)
	}
	managedLateSourceReuseNegativeWrite(t, store, output+"/qualification.json", []byte(`{"new_prospective_output":true}`))
	if err := checkManagedLateSourceReuse(e); err != nil {
		t.Fatal("legitimate own preview output invalidated historical inputs", err)
	}
	for _, relative := range []string{filepath.Dir(paths["material"]), "deployment/create/openbao-recovery/" + strings.Repeat("a", 64), "deployment/create/openbao-recovery/../" + strings.Repeat("8", 64), "deployment/create/openbao-recovery/" + strings.Repeat("8", 64) + "/qualification.json", "deployment/repairs/" + strings.Repeat("8", 64)} {
		if err := allowManagedLateSourceReuseOutput(e, relative); err == nil {
			t.Fatal("output registration hid existing, referenced or foreign input", relative)
		}
	}
	var got map[string]string
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &got); err != nil || !hit {
		t.Fatal("refused output changed genuine cached source", err)
	}
	managedLateSourceReuseNegativeWrite(t, store, paths["material"], []byte(`{"material":"modified"}`))
	if err := checkManagedLateSourceReuse(e); err == nil {
		t.Fatal("own preview output allowance also hid original material drift")
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("pure reuse/output checks rewrote source journal")
	}
}

func TestManagedLateSourceReuseHistoricalFrameMustMatchLiteralPrefix(t *testing.T) {
	store, e, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	bounded := *e
	bounded.Journal = e.Journal
	bounded.Journal.Entries = slices.Clone(e.Journal.Entries[:370])
	const kind = "isolated-original370-pure-source"
	if err := rememberManagedLateSourceReuse(store, &bounded, cfg, kind, map[string]string{"original": "370"}); err != nil {
		t.Fatal("authenticated literal historical prefix refused", err)
	}
	var got map[string]string
	if hit, err := readManagedLateSourceReuse(store, &bounded, cfg, kind, &got); err != nil || !hit {
		t.Fatal(err)
	}
	bounded.Journal.Entries[0].Receipt = []byte(`{"invented":"historical-prefix"}`)
	if hit, err := readManagedLateSourceReuse(store, &bounded, cfg, kind, &got); err == nil || hit {
		t.Fatal("caller-invented historical prefix reused authority")
	}
	if deploymentplan.Digest(e.lateSourceReuse.source.Entries[0]) == deploymentplan.Digest(bounded.Journal.Entries[0]) {
		t.Fatal("caller slice mutation altered private captured source")
	}
}
