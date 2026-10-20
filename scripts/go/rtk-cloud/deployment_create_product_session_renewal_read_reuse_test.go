package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductSessionRenewalReadReusesOnlyCurrentAuthenticatedHistory(t *testing.T) {
	x := newProductExpiryE2EFixture(t)
	f := x.f
	if wait := time.Until(x.oldSession.Expires.Add(time.Millisecond)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	made, err := executeProductSessionRenewal(t.Context(), f.cfg, f.store, f.runtime, f.l, x.apply)
	if err != nil || made == nil {
		t.Fatal("genuine completed renewal", err)
	}
	read, deps, err := readProductSessionRenewalWithDependencies(f.store, f.e, f.l, &f.cfg)
	if err != nil || read == nil || len(deps) == 0 || deploymentplan.Digest(read) != deploymentplan.Digest(made) {
		t.Fatal("combined authenticated read", err)
	}
	before := deploymentplan.Digest(read)
	independent, err := productSessionRenewalDependencyResources(f.cfg, f.store, f.e, f.l, read)
	if err != nil || deploymentplan.Digest(independent) != deploymentplan.Digest(deps) || deploymentplan.Digest(read) != before {
		t.Fatal("combined and independent recipes differ or mutate the renewal", err)
	}
	// The canonical completion recipe and caller configuration are separate
	// checks. Reuse must not let the canonical check replace the latter.
	foreign := f.cfg
	foreign.Environment = "staging"
	if _, err := productSessionRenewalDependencyResources(foreign, f.store, f.e, f.l, read); err == nil {
		t.Fatal("independent recipe must reject the foreign configuration")
	}
	if _, _, err := readProductSessionRenewalWithDependencies(f.store, f.e, f.l, &foreign); err == nil {
		t.Fatal("caller configuration recipe was omitted")
	}
	base := filepath.Join(f.store.Root, productSessionRenewalBase(f.l))
	stage, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+productSessionRenewalStage(f.l, read.Source, "job"))
	if err != nil || stage == nil || len(stage.Resources) == 0 {
		t.Fatal("accepted renewal Job", err)
	}
	source := read.Source
	source.ParentSHA256 = strings.Repeat("f", 64)
	observed, err := readProductSessionRenewalObserved(f.store, f.e, f.l, read.Source)
	if err != nil || observed == nil {
		t.Fatal("accepted observation", err)
	}
	observed.CertificateSHA256 = strings.Repeat("f", 64)
	completed := *read
	completed.SourceSHA256 = strings.Repeat("f", 64)
	journal.Owners[stage.Resources[0].ID] = "99999999-9999-4999-8999-999999999999"
	for _, test := range []struct {
		name, path string
		changed    any
	}{
		{"source-lineage", filepath.Join(base, "source.json"), source},
		{"observed-credential", filepath.Join(base, "observed.json"), observed},
		{"completed-source", filepath.Join(base, "completed.json"), completed},
		{"accepted-owner", filepath.Join(f.store.Root, "deployment/rollback", stage.SHA256(), "journal.json"), journal},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(test.path, original, 0600); err != nil {
					t.Error(err)
				}
			})
			changed := mustProviderRecoveryJSON(test.changed)
			if err := os.WriteFile(test.path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readProductSessionRenewalWithDependencies(f.store, f.e, f.l, &f.cfg); err == nil {
				t.Fatal("combined read reused history from before mutation")
			}
			if _, err := readProductSessionRenewal(f.store, f.e, f.l); err == nil {
				t.Fatal("ordinary read reused history from before mutation")
			}
			if raw, err := os.ReadFile(test.path); err != nil || !bytes.Equal(raw, changed) {
				t.Fatal("reader changed historical evidence", err)
			}
		})
	}
	after, again, err := readProductSessionRenewalWithDependencies(f.store, f.e, f.l, &f.cfg)
	if err != nil || deploymentplan.Digest(after) != before || deploymentplan.Digest(again) != deploymentplan.Digest(deps) {
		t.Fatal("restored evidence did not produce an independent equivalent read", err)
	}
}
