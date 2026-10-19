package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailAccountRegistrationCRLStage = "account-registration-crls"
const createNormalTailAccountRegistrationCRLSchema = "rtk-cloud.create-account-registration-crls/v1"
const createNormalTailAccountRegistrationCRLProfile = "billing-payment-reference-sha256-v2-service-registration-crls-v1"

type createNormalTailAccountRegistrationCRLs struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256     string
	PreviousPlanSHA256, PreviousJournalSHA256, SecretUID string
	Issuers                                              []environmentCreateRegisteredIssuer
	CRLs                                                 []string
	CapturedAt                                           time.Time
}

func createNormalTailAccountRegistrationCRLPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-account-registration-crls.json")
}
func createNormalTailAccountRegistrationCRLID(stack string) string {
	return "Secret/" + stack + "-account-manager/account-manager-service-registration-tls"
}
func accountRegistrationCRLBlock(stage string) error {
	return environmentCreateBlocker("deployment.configuration.service-registration-crls", "recorded Account Manager registration CRLs require exact original owners and current signed issuer coverage: "+stage)
}
func validateAccountRegistrationCRLBundle(s *createNormalTailAccountRegistrationCRLs, at time.Time) error {
	if s == nil || len(s.Issuers) != 3 || len(s.CRLs) != 3 || s.CapturedAt.IsZero() || s.CapturedAt.After(time.Now().UTC()) {
		return accountRegistrationCRLBlock("public source")
	}
	seen := map[string]bool{}
	for i, issuer := range s.Issuers {
		cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
		block, rest := pem.Decode([]byte(s.CRLs[i]))
		if err != nil || block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 || seen[issuer.ID] {
			return accountRegistrationCRLBlock("issuer or CRL encoding")
		}
		seen[issuer.ID] = true
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil || crl.CheckSignatureFrom(cert) != nil || crl.ThisUpdate.After(at) || !crl.NextUpdate.After(at) || crl.Number == nil {
			return accountRegistrationCRLBlock("issuer signature or lifetime")
		}
	}
	return nil
}
func accountRegistrationCRLResource(p *deploymentplan.Plan) (*deploymentplan.Resource, error) {
	if p == nil {
		return nil, accountRegistrationCRLBlock("previous workload")
	}
	var found *deploymentplan.Resource
	for i := range p.Resources {
		if p.Resources[i].ID == createNormalTailAccountRegistrationCRLID(p.Stack) {
			if found != nil {
				return nil, accountRegistrationCRLBlock("duplicate Secret")
			}
			found = &p.Resources[i]
		}
	}
	if found == nil || found.Action != "retain" || !environmentBootstrapOwnerUUID.MatchString(found.UID) || found.Baseline == nil || deploymentplan.Map(found.Baseline["metadata"])["uid"] != found.UID {
		return nil, accountRegistrationCRLBlock("original retained Secret")
	}
	return found, nil
}
func accountRegistrationCRLDesired(previous *deploymentplan.Plan, s *createNormalTailAccountRegistrationCRLs) (deploymentplan.Object, error) {
	if err := validateAccountRegistrationCRLBundle(s, s.CapturedAt); err != nil {
		return nil, err
	}
	old, err := accountRegistrationCRLResource(previous)
	if err != nil {
		return nil, err
	}
	if old.UID != s.SecretUID {
		return nil, accountRegistrationCRLBlock("original Secret UID")
	}
	out := deploymentplan.Clone(old.Baseline)
	meta := deploymentplan.Map(out["metadata"])
	delete(meta, "uid")
	data := deploymentplan.Map(out["data"])
	if len(data) != 4 || data["client-ca.crt"] == nil || data["client.crl"] == nil || data["tls.crt"] == nil || data["tls.key"] == nil {
		return nil, accountRegistrationCRLBlock("original Secret fields")
	}
	bundle := ""
	for _, v := range s.CRLs {
		bundle += v
		if len(v) > 0 && v[len(v)-1] != '\n' {
			bundle += "\n"
		}
	}
	data["client.crl"] = base64.StdEncoding.EncodeToString([]byte(bundle))
	return out, nil
}
func readCreateNormalTailAccountRegistrationCRLs(store secretStore, e *environmentCreateExecution, previous *deploymentplan.Plan, j deploymentplan.Journal) (*createNormalTailAccountRegistrationCRLs, *deploymentplan.Plan, deploymentplan.Journal, error) {
	var zero deploymentplan.Journal
	bad := func(stage string) (*createNormalTailAccountRegistrationCRLs, *deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, nil, zero, accountRegistrationCRLBlock(stage)
	}
	if e == nil || e.Store != store {
		return bad("held original owner")
	}
	path := createNormalTailAccountRegistrationCRLPath(e)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailAccountRegistrationCRLStage) != nil {
			return bad("recorded child lost public source")
		}
		return nil, nil, zero, nil
	} else if err != nil {
		return nil, nil, zero, err
	}
	var s createNormalTailAccountRegistrationCRLs
	if readEnvironmentPreflightJSON(path, &s) != nil {
		return bad("source encoding")
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, nil, zero, err
	}
	if program.Products == nil {
		return bad("product lifecycle")
	}
	installed, err := readProductInstalled(store, program.Products)
	if err != nil || installed == nil {
		return bad("original permanent issuer")
	}
	expected := []environmentCreateRegisteredIssuer{program.Products.Root, program.Products.Intermediate, installed.Ceremony.Intermediate}
	if previous == nil || s.Schema != createNormalTailAccountRegistrationCRLSchema || s.ParentSHA256 != e.Journal.PlanSHA256 || s.OwnerSHA256 != deploymentplan.Digest(e.owner) || s.ProgramSHA256 != deploymentplan.Digest(program) || s.PreviousPlanSHA256 != previous.SHA256() || s.PreviousJournalSHA256 != deploymentplan.Digest(j) || !createNormalTailRuntimePartial(previous, j) || deploymentplan.Digest(s.Issuers) != deploymentplan.Digest(expected) {
		return bad("literal source lineage")
	}
	desired, err := accountRegistrationCRLDesired(previous, &s)
	if err != nil {
		return nil, nil, zero, err
	}
	p, err := readCreateNormalTailPreparationPlan(store, e, createNormalTailAccountRegistrationCRLStage)
	if err != nil {
		return nil, nil, zero, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailAccountRegistrationCRLStage)
	if p == nil {
		if entry != nil {
			return bad("started child lost Plan")
		}
		return &s, nil, zero, nil
	}
	if len(p.Resources) != 1 {
		return bad("exact one Secret")
	}
	r := p.Resources[0]
	old, _ := accountRegistrationCRLResource(previous)
	if r.ID != deploymentplan.ID(desired) || r.Phase != "configuration" || r.Action != "update" || r.UID != s.SecretUID || len(r.Dependencies) != 0 || deploymentplan.Digest(r.Baseline) != deploymentplan.Digest(old.Baseline) || deploymentplan.Digest(r.Desired) != deploymentplan.Digest(desired) {
		return bad("exact Secret update")
	}
	if entry == nil {
		if _, exists, err := p.ReadExecution(store.Root); err != nil || exists {
			return bad("orphan native child")
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if len(lease.Holder) >= len(p.SHA256()) && lease.Holder[:len(p.SHA256())] == p.SHA256() {
				return bad("orphan native lease")
			}
		}
		return &s, p, zero, nil
	}
	child, cj, err := readCreateNormalTailStage(store, e, createNormalTailAccountRegistrationCRLStage, p)
	return &s, child, cj, err
}

func ensureCreateNormalTailAccountRegistrationCRLs(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, out io.Writer) error {
	var runtime *environmentCreateBootstrapRuntime
	fetch := func(ctx context.Context, issuer environmentCreateRegisteredIssuer) (string, error) {
		if runtime == nil {
			program, err := readCreateNormalTailProgram(c.Store, h.Execution)
			if err != nil {
				return "", err
			}
			r, _, err := productIdentityRuntime(ctx, cfg, h, c, program.Products)
			if err != nil {
				return "", err
			}
			runtime = &r
		}
		return readProductCurrentCRL(ctx, runtime.PKIClient, issuer)
	}
	apply := func(ctx context.Context, definitions []deploymentplan.Definition) error {
		parent := *h.OriginalPlan
		parent.Release = c.Release
		return applyCreateNormalTailStage(ctx, cfg, c.Store, &parent, h.Execution, createNormalTailAccountRegistrationCRLStage, definitions, out)
	}
	return ensureCreateNormalTailAccountRegistrationCRLsWithApply(ctx, cfg, h, c, c.Backend.Read, fetch, apply, func(ctx context.Context) error {
		return captureEnvironmentCreateVolumeOwners(ctx, cfg, c.Backend, h.Execution)
	})
}
func ensureCreateNormalTailAccountRegistrationCRLsWithApply(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, read func(context.Context, string) (deploymentplan.Object, error), fetch func(context.Context, environmentCreateRegisteredIssuer) (string, error), apply func(context.Context, []deploymentplan.Definition) error, capture func(context.Context) error) error {
	e, store := h.Execution, h.Execution.Store
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		return nil
	}
	last := chain[len(chain)-1]
	// A linked successor retains this accepted child; the source remains pinned
	// to the literal earlier journal, never to the successor's new execution.
	for _, step := range chain {
		if step.Source.RegistrationCRLs != nil {
			// The accepted child is immutable historical evidence. Current runtime
			// freshness is checked independently by native consumers and health.
			return validateAccountRegistrationCRLBundle(step.Source.RegistrationCRLs, step.Source.RegistrationCRLs.CapturedAt)
		}
	}
	if last.Child == nil || !createNormalTailRuntimePartial(last.Child, last.Journal) {
		return nil
	}
	old, err := accountRegistrationCRLResource(last.Child)
	if err != nil {
		return err
	}
	s, p, j, err := readCreateNormalTailAccountRegistrationCRLs(store, e, last.Child, last.Journal)
	if err != nil {
		return err
	}
	if s == nil {
		// Persist genuine fresh allocation evidence before this partial child
		// becomes a historical ancestor of the next numbered workload.
		if err := capture(ctx); err != nil {
			return err
		}
		program, err := readCreateNormalTailProgram(store, e)
		if err != nil {
			return err
		}
		installed, err := readProductInstalled(store, program.Products)
		if err != nil || installed == nil {
			return accountRegistrationCRLBlock("installed permanent identities")
		}
		s = &createNormalTailAccountRegistrationCRLs{Schema: createNormalTailAccountRegistrationCRLSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: deploymentplan.Digest(program), PreviousPlanSHA256: last.Child.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(last.Journal), SecretUID: old.UID, Issuers: []environmentCreateRegisteredIssuer{program.Products.Root, program.Products.Intermediate, installed.Ceremony.Intermediate}, CapturedAt: time.Now().UTC()}
		for _, issuer := range s.Issuers {
			raw, err := fetch(ctx, issuer)
			if err != nil {
				return err
			}
			s.CRLs = append(s.CRLs, raw)
		}
		if err := validateAccountRegistrationCRLBundle(s, time.Now().UTC()); err != nil {
			return err
		}
		current, err := read(ctx, old.ID)
		if err != nil {
			return err
		}
		if _, err = old.Body(current); err != nil {
			return accountRegistrationCRLBlock("original native Secret body")
		}
		if err = e.Check(ctx); err != nil {
			return err
		}
		rel, _ := filepath.Rel(store.Root, createNormalTailAccountRegistrationCRLPath(e))
		if err = store.write(rel, mustProviderRecoveryJSON(s), false); err != nil {
			return err
		}
	}
	if p != nil && j.Accepted {
		current, err := read(ctx, old.ID)
		if err != nil {
			return err
		}
		if current == nil || deploymentplan.Map(current["metadata"])["uid"] != s.SecretUID || !deploymentplan.Matches(p.Resources[0].Desired, current) {
			return accountRegistrationCRLBlock("accepted Secret current body")
		}
		return e.Check(ctx)
	}
	if err = validateAccountRegistrationCRLBundle(s, time.Now().UTC()); err != nil {
		return err
	}
	desired, err := accountRegistrationCRLDesired(last.Child, s)
	if err != nil {
		return err
	}
	return apply(ctx, []deploymentplan.Definition{{Object: desired, Phase: "configuration"}})
}
