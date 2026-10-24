package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The same isolated HTTPS ACME producer creates the original account, keys,
// signed CSR and first known order. This helper only admits its frozen program.
func admitPublicTLSReaderProgram(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, defs []deploymentplan.Definition, p *environmentPublicTLSPlan, initialTimeout ...time.Duration) (createNormalTailProgramAdmission, func() error) {
	t.Helper()
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) == 0 {
		t.Fatal("selected product output required", err)
	}
	digest := deploymentplan.Digest("public TLS isolated complete program")
	program := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), HandoffSHA256: digest, ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(cfg), ClusterSHA256: digest, IdentitySHA256: digest, Definitions: defs, PublicTLS: p}
	program.HandoffSHA256 = deploymentplan.Digest(map[string]string{"public-tls": deploymentplan.Digest(p)})
	program.Products = &createNormalTailProductLifecycle{ParentSHA256: program.ParentSHA256, OwnerSHA256: program.OwnerSHA256, Outputs: outputs, SourceDefinitions: defs}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := createNormalTailProgramArtifact{Program: program, Generation: digest, CheckerSHA256: checker, QualifiedAt: now, ValidUntil: now.Add(10 * time.Minute), Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: deploymentplan.Digest(program)}}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: a.Qualification.PlanSHA256})
	}
	if err = saveCreateNormalTailProgramArtifact(store, e, a); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: deploymentplan.Digest(program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(program) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	timeout := 45 * time.Minute
	if len(initialTimeout) == 1 {
		timeout = initialTimeout[0]
	}
	admission, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, &createNormalTailHandoff{Execution: e}, a, timeout, now, func(ctx context.Context) error { return e.Check(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	start := len(e.Journal.Entries)
	return admission, func() error {
		n, _, err := validateCreateNormalTailPublicTLSStages(store, e, p, e.Journal.Entries[start:])
		if err == nil && n != len(e.Journal.Entries)-start {
			t.Fatal("reader left an unconsumed public TLS stage")
		}
		return err
	}
}

func TestCreatePublicTLSAttemptReaderPreservesLegacyAndBindsEveryKnownAttempt(t *testing.T) {
	for _, mode := range []string{"pending", "allocated", "completed-before-material", "missing-intent", "unknown-response", "gap", "previous-hash", "foreign-csr", "foreign-order", "repeated-old-order", "future", "before-certificate", "after-deadline", "completion-before-request", "missing-receipt-chain", "foreign-receipt-chain", "foreign-receipt-order", "public-attempt-directory", "symlink-attempt-directory"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, defs, p, ops, _, _, apply := publicTLSProtocolFixture(t)
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
			p.ConfigurationSHA256 = deploymentplan.Digest(cfg)
			admission, read := admitPublicTLSReaderProgram(t, cfg, store, e, defs, p)
			ops.WaitTXT = func(context.Context, string, string, map[string]string) error { return context.Canceled }
			if err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err == nil {
				t.Fatal("first real request did not stop before authorization")
			}
			if err := read(); err != nil {
				t.Fatal("legacy single known order was rejected", err)
			}
			var keys environmentPublicTLSKeys
			var original acme.Order
			if publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "keys.json"), &keys) != nil || publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "order.json"), &original) != nil {
				t.Fatal("real original request was not retained")
			}
			originalSHA := deploymentplan.Digest(original)
			keysSHA := deploymentplan.Digest(keys)
			invalid := original
			invalid.Status = acme.StatusInvalid
			attempt := environmentPublicTLSOrderAttempt{Plan: deploymentplan.Digest(p), CSR: deploymentplan.Digest(keys.CSR), PreviousOrder: original.URI, PreviousSHA256: originalSHA, Invalid: invalid, RequestedAt: time.Now().UTC()}
			order := original
			order.URI += "-second"
			order.FinalizeURL += "-second"
			order.CertURL = ""
			order.AuthzURLs = append([]string(nil), original.AuthzURLs...)
			for i := range order.AuthzURLs {
				order.AuthzURLs[i] += "-second"
			}
			base := publicTLSAttemptDirectory(p, 1)
			if publicTLSWrite(store, filepath.Join(base, "intent.json"), attempt) != nil || publicTLSWrite(store, filepath.Join(base, "order.json"), order) != nil {
				t.Fatal("cannot save isolated known attempt")
			}
			_, chain, _, err := readEnvironmentPublicTLSOrderAttempts(store, p, deploymentplan.Digest(keys.CSR))
			if err != nil {
				t.Fatal("valid known chain rejected", err)
			}
			put := func(relative string, value any) {
				t.Helper()
				path, err := store.safePath(relative)
				if err != nil || os.WriteFile(path, mustProviderRecoveryJSON(value), 0600) != nil {
					t.Fatal("cannot change isolated negative", err)
				}
			}
			path, _ := store.safePath(base)
			entry := createOpenBaoACKCompletionEntry(e.Journal, "public-tls/certificate")
			switch mode {
			case "missing-intent":
				_ = os.Remove(filepath.Join(path, "intent.json"))
			case "unknown-response":
				_ = os.Remove(filepath.Join(path, "order.json"))
			case "gap":
				_ = os.Rename(path, filepath.Join(filepath.Dir(path), "0002"))
			case "previous-hash":
				attempt.PreviousSHA256 = deploymentplan.Digest("foreign prior order")
			case "foreign-csr":
				attempt.CSR = "foreign signed CSR"
			case "foreign-order":
				order.URI = "https://foreign.test/order/2"
				put(filepath.Join(base, "order.json"), order)
			case "repeated-old-order":
				priorInvalid := order
				priorInvalid.Status = acme.StatusInvalid
				second := environmentPublicTLSOrderAttempt{Plan: attempt.Plan, CSR: attempt.CSR, PreviousOrder: order.URI, PreviousSHA256: deploymentplan.Digest(order), Invalid: priorInvalid, RequestedAt: time.Now().UTC()}
				secondBase := publicTLSAttemptDirectory(p, 2)
				if publicTLSWrite(store, filepath.Join(secondBase, "intent.json"), second) != nil || publicTLSWrite(store, filepath.Join(secondBase, "order.json"), original) != nil {
					t.Fatal("cannot save isolated repeated order")
				}
			case "future":
				attempt.RequestedAt = time.Now().Add(time.Minute)
			case "before-certificate":
				attempt.RequestedAt = entry.StartedAt.Add(-time.Nanosecond)
			case "after-deadline":
				attempt.RequestedAt = admission.Deadline
			case "public-attempt-directory":
				_ = os.Chmod(filepath.Dir(path), 0755)
			case "symlink-attempt-directory":
				parent := filepath.Dir(path)
				saved := parent + "-saved"
				if os.Rename(parent, saved) != nil || os.Symlink(saved, parent) != nil {
					t.Fatal("cannot save isolated unsafe directory")
				}
			}
			switch mode {
			case "previous-hash", "foreign-csr", "future", "before-certificate", "after-deadline":
				put(filepath.Join(base, "intent.json"), attempt)
			}
			switch mode {
			case "allocated", "completed-before-material", "completion-before-request", "missing-receipt-chain", "foreign-receipt-chain", "foreign-receipt-order":
				receipt := struct {
					Plan, Order, CSR, Material string
					Attempts                   string `json:",omitempty"`
				}{deploymentplan.Digest(p), order.URI, deploymentplan.Digest(keys.CSR), deploymentplan.Digest("native material hash"), chain}
				if mode == "missing-receipt-chain" {
					receipt.Attempts = ""
				} else if mode == "foreign-receipt-chain" {
					receipt.Attempts = deploymentplan.Digest("foreign chain")
				} else if mode == "foreign-receipt-order" {
					receipt.Order = original.URI
				}
				if err = e.RecordReceipt(entry.Key, mustProviderRecoveryJSON(receipt)); err != nil {
					t.Fatal(err)
				}
				if mode == "completed-before-material" || mode == "completion-before-request" {
					completedAt := time.Now().UTC()
					if mode == "completion-before-request" {
						completedAt = entry.StartedAt
					}
					if err = e.CompleteEntry(entry.Key, completedAt); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := deploymentplan.Digest(e.Journal)
			err = read()
			valid := mode == "pending" || mode == "allocated" || mode == "completed-before-material"
			if (err == nil) != valid || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("wrong immutable attempt-history disposition", err)
			}
			var unchangedKeys environmentPublicTLSKeys
			var unchangedOrder acme.Order
			if publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "keys.json"), &unchangedKeys) != nil || deploymentplan.Digest(unchangedKeys) != keysSHA || publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "order.json"), &unchangedOrder) != nil || deploymentplan.Digest(unchangedOrder) != originalSHA {
				t.Fatal("reader changed original account/key/CSR/order")
			}
		})
	}
}
