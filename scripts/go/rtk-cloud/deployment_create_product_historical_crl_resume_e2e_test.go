package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productHistoricalCRLPublicTrustMatches(f *productProtocolFixture) bool {
	definitions, err := productIdentityTrustDefinitions(f.l, f.next)
	if err != nil {
		return false
	}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		current := f.b.objects[id]
		if current == nil || deploymentplan.Map(current["metadata"])["uid"] != f.l.SourceOwners[id] || !deploymentplan.Matches(deploymentplan.Projection(definition.Object), current) {
			return false
		}
	}
	return true
}

// Only the isolated native boundary is modeled here. The signatures, monotonic
// Root lineage, native current-CRL reader and exact nine-consumer ACK reader are
// the maintained production validators; no completion receipt is synthesized.
func (x *productExpiryE2EFixture) publishCurrentRootCRLSuccessor(t *testing.T, c *compiledEnvironmentDeployment) {
	t.Helper()
	f := x.f
	previous, exists, err := c.Plan.ReadExecution(f.store.Root)
	if err != nil || !exists {
		t.Fatal("original partial CRL source journal", err)
	}
	source, child, journal, err := readCreateNormalTailAccountRegistrationCRLs(f.store, f.e, c.Plan, previous)
	if err != nil || source == nil || child == nil || !journal.Accepted {
		t.Fatal("accepted immutable historical registration CRL source", err)
	}
	for i, issuer := range source.Issuers {
		if issuer.ID != f.l.Root.ID {
			continue
		}
		block, rest := pem.Decode([]byte(source.CRLs[i]))
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			t.Fatal("historical Root2 encoding")
		}
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil || crl.Number.String() != "2" || crl.CheckSignatureFrom(f.root) != nil || !crl.NextUpdate.After(source.CapturedAt) {
			t.Fatal("genuinely signed Root2 current at source capture", err)
		}
		x.historicalRootCRL = &createRootCRLRecord{IssuerID: issuer.ID, Digest: environmentPreflightBytesSHA256(crl.Raw), Number: crl.Number.String(), PEM: source.CRLs[i], ThisUpdate: crl.ThisUpdate, NextUpdate: crl.NextUpdate}
	}
	if x.historicalRootCRL == nil {
		t.Fatal("historical Root2 issuer absent")
	}
	now := time.Now().UTC().Truncate(time.Second)
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(3), ThisUpdate: now, NextUpdate: now.Add(6 * time.Hour)}, f.root, f.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := x509.ParseRevocationList(der)
	if err != nil || crl.CheckSignatureFrom(f.root) != nil || crl.Number.String() != "3" || crl.ThisUpdate.Before(x.historicalRootCRL.ThisUpdate) || environmentPreflightBytesSHA256(f.root.Raw) != f.l.Root.RootFingerprint {
		t.Fatal("same-Root monotonic signed current successor", err)
	}
	record := createRootCRLRecord{IssuerID: f.l.Root.ID, Digest: environmentPreflightBytesSHA256(crl.Raw), Number: crl.Number.String(), PEM: productPEM("X509 CRL", der), ThisUpdate: crl.ThisUpdate, NextUpdate: crl.NextUpdate}
	if record.IssuerID != x.historicalRootCRL.IssuerID || record.Digest == x.historicalRootCRL.Digest {
		t.Fatal("current successor changed original Root identity")
	}
	x.currentRootCRL = &record
	x.verifyCurrentRootCRLSuccessor(t)
	t.Log("milestone: immutable signed short Root2 captured; same-key higher-number current Root3 and all nine native-boundary consumers validated")
}

func (x *productExpiryE2EFixture) verifyCurrentRootCRLSuccessor(t *testing.T) {
	t.Helper()
	f := x.f
	if x.currentRootCRL == nil {
		t.Fatal("current Root3 publication absent")
	}
	current, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil || current != x.currentRootCRL.PEM {
		t.Fatal("native current same-Root3 signature and lifetime", err)
	}
	consumers, err := productIdentityCRLConsumers(f.l)
	if err != nil || len(consumers) != 9 {
		t.Fatal("exact original nine-consumer policy", err)
	}
	reads := x.rootCRLACKReads
	if err := waitEnvironmentCreateCRLAcks(t.Context(), f.cfg, f.runtime, f.l.Root.ID, x.currentRootCRL.Digest, consumers); err != nil || x.rootCRLACKReads != reads+1 {
		t.Fatal("fresh exact current Root3 native ACK inventory", err)
	}
}

type productHistoricalCRLResumeInput struct {
	Configuration                                    deploymentConfig
	StoreRoot, ParentSHA256, OwnerSHA256, PlanSHA256 string
	OwnerOnly                                        bool `json:",omitempty"`
}

func (x *productExpiryE2EFixture) resumeLinkedCRLsInFreshProcess(t *testing.T, c *compiledEnvironmentDeployment) {
	t.Helper()
	f := x.f
	if x.historicalRootCRL == nil {
		t.Fatal("historical signed Root2 absent")
	}
	if wait := time.Until(x.historicalRootCRL.NextUpdate.Add(time.Millisecond)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	if x.historicalRootCRL.NextUpdate.After(time.Now().UTC()) {
		t.Fatal("historical Root2 did not genuinely expire")
	}
	x.verifyCurrentRootCRLSuccessor(t)
	beforeSource, err := os.ReadFile(createNormalTailAccountRegistrationCRLPath(f.e))
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal, err := os.ReadFile(f.e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	input := productHistoricalCRLResumeInput{Configuration: f.cfg, StoreRoot: f.store.Root, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), PlanSHA256: c.Plan.SHA256()}
	payload := filepath.Join(f.store.ConfigRoot, "historical-crl-resume.json")
	if err := os.WriteFile(payload, mustProviderRecoveryJSON(input), 0600); err != nil {
		t.Fatal(err)
	}
	// Release the actual process fence, then let a distinct test process reload
	// the original owner, journal, descriptors and renewal proofs from disk.
	if err := f.e.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProductHistoricalCRLLinkedResumeFreshProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "RTK_PRODUCT_HISTORICAL_CRL_CHILD="+payload, "RTK_PRODUCT_HISTORICAL_CRL_FIXTURE_ROOT="+f.store.ConfigRoot)
	out, processErr := cmd.CombinedOutput()
	fresh, reloadErr := loadCreateNormalTailExecution(t.Context(), f.cfg, f.store, input.ParentSHA256)
	if reloadErr == nil {
		*f.e = *fresh
	}
	if processErr != nil || reloadErr != nil || !bytes.Contains(out, []byte("--- PASS: TestProductHistoricalCRLLinkedResumeFreshProcess")) {
		t.Fatalf("fresh-process same0004 historical CRL replay: process=%v reload=%v %s", processErr, reloadErr, out)
	}
	t.Logf("fresh-process named proof:\n%s", out)
	afterSource, err := os.ReadFile(createNormalTailAccountRegistrationCRLPath(f.e))
	if err != nil || !bytes.Equal(beforeSource, afterSource) {
		t.Fatal("fresh resume rewrote immutable historical CRL source", err)
	}
	afterJournal, err := os.ReadFile(f.e.JournalPath)
	if err != nil || !bytes.Equal(beforeJournal, afterJournal) || c.Plan.SHA256() != input.PlanSHA256 || deploymentplan.Digest(f.e.owner) != input.OwnerSHA256 {
		t.Fatal("fresh resume changed original parent or target", err)
	}
	x.verifyCurrentRootCRLSuccessor(t)
	t.Log("milestone: naturally expired historical Root2; distinct process reloaded original owner and same accepted0004, linked CRL stage reused without fetch/apply; current signed Root3/nine ACKs remain fresh")
}

func TestProductHistoricalCRLLinkedResumeFreshProcess(t *testing.T) {
	path := os.Getenv("RTK_PRODUCT_HISTORICAL_CRL_CHILD")
	if path == "" {
		return
	}
	root := os.Getenv("RTK_PRODUCT_HISTORICAL_CRL_FIXTURE_ROOT")
	if !productHistoricalCRLFixturePath(root, path) {
		t.Fatal("child payload must be inside the parent's isolated synthetic fixture")
	}
	var input productHistoricalCRLResumeInput
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &input) != nil || !productHistoricalCRLFixturePath(root, input.StoreRoot) {
		t.Fatal("isolated synthetic child input", err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Dir(input.StoreRoot))
	store, err := newSecretStore(filepath.Dir(input.StoreRoot), input.Configuration.Environment)
	if err != nil || store.Root != input.StoreRoot {
		t.Fatal("fresh selected store", err)
	}
	e, err := loadCreateNormalTailExecution(t.Context(), input.Configuration, store, input.ParentSHA256)
	if err != nil {
		t.Fatal("fresh original process fence", err)
	}
	defer e.Close()
	if deploymentplan.Digest(e.owner) != input.OwnerSHA256 {
		t.Fatal("fresh original owner changed")
	}
	if input.OwnerOnly {
		if err := e.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		return
	}
	h := &createNormalTailHandoff{Execution: e}
	c := &compiledEnvironmentDeployment{Store: store, Configuration: input.Configuration}
	unexpected := errors.New("linked historical replay attempted fresh native work")
	read := func(context.Context, string) (deploymentplan.Object, error) { return nil, unexpected }
	fetch := func(context.Context, environmentCreateRegisteredIssuer) (string, error) { return "", unexpected }
	apply := func(context.Context, []deploymentplan.Definition) error { return unexpected }
	capture := func(context.Context) error { return unexpected }
	if err := ensureCreateNormalTailAccountRegistrationCRLsWithApply(t.Context(), input.Configuration, h, c, read, fetch, apply, capture); err != nil {
		t.Fatal("actual linked historical CRL ensure after expiry", err)
	}
	d, err := readCreateNormalTailRuntimeForward(store, e)
	if err != nil || d == nil || d.Sequence != 4 || d.Target.Plan.SHA256() != input.PlanSHA256 {
		t.Fatal("fresh process selected another target", err)
	}
	if err := e.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The parent passes its freshly generated TempDir store explicitly. Validate
// both the payload and selected store against it before reading any authority;
// fixed operator paths and the caller's HOME are never fallback inputs.
func productHistoricalCRLFixturePath(root, path string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	temporary, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	fixture, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
	}
	if !within(temporary, fixture) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && within(fixture, resolved)
}

// Exercise real owner primitives before the long lifecycle. The loader acquires
// only the original private process owner; it needs no invented bootstrap receipt.
func TestProductHistoricalCRLFreshProcessExecutionOwnerContract(t *testing.T) {
	t.Run("maintained-standard-profile", func(t *testing.T) {
		cfg, _, _, _ := maintainedDeploymentSnapshotFixture(t, "dev")
		path := filepath.Join(cfg.Workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
		appendFile(t, path, "\nDEPLOYMENT_PROTECTION_PROFILE=standard\n")
		resolved, err := resolveDeploymentConfig(cfg.Workspace, "dev", "")
		if err != nil || resolved.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" {
			t.Fatal("declared standard profile was not resolved", err)
		}
	})
	for _, mode := range []string{"original", "missing-profile", "foreign-owner", "foreign-stack"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			cfg.Values = appendMap(cfg.Values, map[string]string{"DEPLOYMENT_PROTECTION_PROFILE": "standard"})
			generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err = publishEnvironmentCreateArtifact(t.Context(), cfg, generation, artifact.Plan, operator, artifact.Qualification, artifact.ReleaseProofs, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			input := productHistoricalCRLResumeInput{Configuration: cfg, StoreRoot: store.Root, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), OwnerOnly: true}
			input.Configuration.Values = appendMap(cfg.Values, map[string]string{})
			switch mode {
			case "missing-profile":
				delete(input.Configuration.Values, "DEPLOYMENT_PROTECTION_PROFILE")
			case "foreign-owner":
				input.OwnerSHA256 = deploymentplan.Digest("foreign owner")
			case "foreign-stack":
				input.Configuration.Values["CLOUD_STACK_NAME"] = "foreign-dev"
			}
			payload := filepath.Join(store.ConfigRoot, "owner-reload.json")
			if err := os.WriteFile(payload, mustProviderRecoveryJSON(input), 0600); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProductHistoricalCRLLinkedResumeFreshProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "RTK_PRODUCT_HISTORICAL_CRL_CHILD="+payload, "RTK_PRODUCT_HISTORICAL_CRL_FIXTURE_ROOT="+store.ConfigRoot)
			out, processErr := cmd.CombinedOutput()
			fresh, reloadErr := loadCreateNormalTailExecution(t.Context(), cfg, store, input.ParentSHA256)
			if reloadErr != nil {
				t.Fatal("original parent could not reacquire after child", reloadErr)
			}
			*e = *fresh
			if mode == "original" && (processErr != nil || !bytes.Contains(out, []byte("--- PASS: TestProductHistoricalCRLLinkedResumeFreshProcess"))) || mode != "original" && processErr == nil {
				t.Fatalf("unexpected fresh process outcome: %v %s", processErr, out)
			}
			after, err := os.ReadFile(e.JournalPath)
			if err != nil || !bytes.Equal(before, after) || e.Check(t.Context()) != nil {
				t.Fatal("fresh process changed genuine original journal or lock", err)
			}
		})
	}
}
