package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type accountCRLReaderBackend struct {
	*productProtocolBackend
	stopID  string
	unknown bool
}

func (b *accountCRLReaderBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.ID == b.stopID {
		return errors.New("isolated known native response before wait completion")
	}
	return b.productProtocolBackend.Wait(ctx, r)
}
func (b *accountCRLReaderBackend) Write(ctx context.Context, action string, o deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := b.productProtocolBackend.Write(ctx, action, o, dry)
	if err == nil && !dry && b.unknown && deploymentplan.ID(o) == b.stopID {
		return nil, errors.New("isolated lost native update response")
	}
	return actual, err
}

func TestCreateNormalTailAccountRegistrationCRLReaderGenuineStandardChild(t *testing.T) {
	for _, mode := range []string{"accepted", "admission-pending", "unknown-update", "changed-server-key"} {
		t.Run(mode, func(t *testing.T) {
			var original deploymentplan.Object
			f, program, _, _, apply := newProductOuterTargetFixtureWithSetup(t, true, func(f *productProtocolFixture, p *createNormalTailProgram) {
				original = accountCRLReaderOriginalSecret(t, f)
				id := deploymentplan.ID(original)
				f.b.objects[id] = deploymentplan.Clone(original)
				f.l.SourceOwners[id] = deploymentplan.String(deploymentplan.Map(original["metadata"])["uid"])
				f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Projection(original), Phase: "configuration", Retain: true})
			})
			ctx := withCreateNormalTailHandoff(t.Context(), f.h)
			if err := executeProductIdentityProtocol(ctx, f.cfg, f.p, f.store, f.runtime, f.l, apply); err != nil {
				t.Fatal("real five-product protocol", err)
			}
			installed, err := readProductInstalled(f.store, f.l)
			if err != nil || installed == nil {
				t.Fatal("real installed identities", err)
			}
			tail := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "isolated-after-registration-source", "namespace": f.p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"state": "unchanged"}}
			defs := []deploymentplan.Definition{{Object: deploymentplan.Projection(original), Phase: "configuration", Retain: true}, {Object: tail, Phase: "workload"}}
			inputs := deploymentplan.Inputs{Operation: "scoped", Environment: f.cfg.Environment, Stack: f.p.Stack, ConfigurationSHA256: program.MaintainedConfigSHA256, ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA("workload-forward-0003", defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: program.HandoffSHA256}
			previous, err := deploymentplan.Compile(inputs, defs, []deploymentplan.Object{original})
			if err != nil {
				t.Fatal(err)
			}
			check := func(c context.Context, _ *deploymentplan.Plan) error { return f.e.Check(c) }
			boundary := &accountCRLReaderBackend{productProtocolBackend: f.b, stopID: deploymentplan.ID(tail)}
			if err = executeCreateNormalTailStageBackend(ctx, f.store, f.h, "workload-forward-0003", previous, boundary, check, check, check, io.Discard, func(context.Context) error { return nil }); err == nil {
				t.Fatal("missing genuine partial predecessor")
			}
			j, exists, err := previous.ReadExecution(f.store.Root)
			if err != nil || !exists || !createNormalTailRuntimePartial(previous, j) {
				t.Fatal("literal genuine predecessor", err)
			}
			outer := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/workload-forward-0003")
			source := &createNormalTailAccountRegistrationCRLs{Schema: createNormalTailAccountRegistrationCRLSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), ProgramSHA256: deploymentplan.Digest(&program), PreviousPlanSHA256: previous.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(j), SecretUID: deploymentplan.String(deploymentplan.Map(original["metadata"])["uid"]), Issuers: []environmentCreateRegisteredIssuer{f.l.Root, f.l.Intermediate, installed.Ceremony.Intermediate}, CapturedAt: time.Now().UTC()}
			for i, issuer := range source.Issuers {
				cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
				if err != nil {
					t.Fatal(err)
				}
				key := f.icaKey
				if issuer.ID == f.l.Root.ID {
					key = f.rootKey
				}
				der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(int64(i + 1)), ThisUpdate: source.CapturedAt.Add(-time.Minute), NextUpdate: source.CapturedAt.Add(time.Hour)}, cert, key)
				if err != nil {
					t.Fatal(err)
				}
				source.CRLs = append(source.CRLs, productPEM("X509 CRL", der))
			}
			rel, err := filepath.Rel(f.store.Root, createNormalTailAccountRegistrationCRLPath(f.e))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.write(rel, mustProviderRecoveryJSON(source), false); err != nil {
				t.Fatal(err)
			}
			desired, err := accountRegistrationCRLDesired(previous, source)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed-server-key" {
				deploymentplan.Map(desired["data"])["tls.key"] = base64.StdEncoding.EncodeToString([]byte("foreign private material"))
			}
			repairDefs := []deploymentplan.Definition{{Object: desired, Phase: "configuration"}}
			inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(createNormalTailAccountRegistrationCRLStage, repairDefs)
			repair, err := deploymentplan.Compile(inputs, repairDefs, []deploymentplan.Object{original})
			if err != nil {
				t.Fatal(err)
			}
			if err = saveCreateNormalTailStagePlan(ctx, f.store, f.e, createNormalTailAccountRegistrationCRLStage, repair); err != nil {
				t.Fatal(err)
			}
			boundary.stopID = ""
			boundary.unknown = mode == "unknown-update"
			if boundary.unknown {
				boundary.stopID = deploymentplan.ID(original)
			}
			qualify := check
			if mode == "admission-pending" {
				qualify = func(context.Context, *deploymentplan.Plan) error { return errors.New("isolated admission stop") }
			}
			if mode != "changed-server-key" {
				err = executeCreateNormalTailStageBackend(ctx, f.store, f.h, createNormalTailAccountRegistrationCRLStage, repair, boundary, qualify, check, check, io.Discard, func(context.Context) error { return nil })
				if (mode == "accepted") != (err == nil) {
					t.Fatal("genuine repair prefix", err)
				}
			} else {
				// The pure source reader rejects the changed recipe before any native write.
				if _, _, _, err = readCreateNormalTailAccountRegistrationCRLs(f.store, f.e, previous, j); err == nil {
					t.Fatal("changed original server key accepted")
				}
				if f.b.writes[deploymentplan.ID(original)] != 0 {
					t.Fatal("foreign recipe reached native update")
				}
				return
			}
			entry := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+createNormalTailAccountRegistrationCRLStage)
			admission, err := readCreateNormalTailProgramAdmission(f.store, f.e)
			if err != nil {
				t.Fatal(err)
			}
			count, children, err := validateCreateNormalTailNestedAccountRegistrationCRLs(f.store, f.e, previous, j, outer, []environmentCreateJournalEntry{entry}, admission, outer.StartedAt)
			if mode == "unknown-update" {
				var cj deploymentplan.Journal
				readErr := readEnvironmentPreflightJSON(filepath.Join(f.store.Root, "deployment", "rollback", repair.SHA256(), "journal.json"), &cj)
				if readErr != nil || cj.InFlight != deploymentplan.ID(original) || f.b.writes[deploymentplan.ID(original)] != 1 {
					t.Fatal("unknown native effect was not genuine", readErr)
				}
				if err == nil {
					t.Fatal("unknown native update accepted")
				}
				return
			}
			expected := 1
			if mode == "admission-pending" {
				expected = 0
			}
			if err != nil || count != 1 || len(children) != expected {
				got := repair.Resources[0]
				old, _ := accountRegistrationCRLResource(previous)
				want, _ := accountRegistrationCRLDesired(previous, source)
				t.Logf("safe recipe diagnostics id=%t phase=%s action=%s uid=%t deps=%d baseline=%t desired=%t", got.ID == deploymentplan.ID(want), got.Phase, got.Action, got.UID == source.SecretUID, len(got.Dependencies), deploymentplan.Digest(got.Baseline) == deploymentplan.Digest(old.Baseline), deploymentplan.Digest(got.Desired) == deploymentplan.Digest(want))
				t.Fatal("genuine nested child refused", count, len(children), err)
			}
			if mode == "accepted" {
				if children[0].SHA256() != repair.SHA256() || f.b.writes[deploymentplan.ID(original)] != 1 {
					t.Fatal("accepted child owner or native effect lost")
				}
				bad := entry
				bad.StartedAt = source.CapturedAt.Add(-time.Nanosecond)
				if _, _, err = validateCreateNormalTailNestedAccountRegistrationCRLs(f.store, f.e, previous, j, outer, []environmentCreateJournalEntry{bad}, admission, outer.StartedAt); err == nil {
					t.Fatal("pre-source child chronology accepted")
				}
			}
		})
	}
}

func accountCRLReaderOriginalSecret(t *testing.T, f *productProtocolFixture) deploymentplan.Object {
	t.Helper()
	now := time.Now().UTC()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := "account-manager." + f.p.Stack + "-account-manager.svc.cluster.local"
	leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, f.ica, &key.PublicKey, f.icaKey)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	value := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	oldBundle := ""
	for _, signer := range []struct {
		cert *x509.Certificate
		key  *ecdsa.PrivateKey
	}{{f.root, f.rootKey}, {f.ica, f.icaKey}} {
		der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, signer.cert, signer.key)
		if err != nil {
			t.Fatal(err)
		}
		oldBundle += productPEM("X509 CRL", der)
	}
	original := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "account-manager-service-registration-tls", "namespace": f.p.Stack + "-account-manager", "uid": "dddddddd-1111-4111-8111-111111111111", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "type": "Opaque", "data": deploymentplan.Object{"client-ca.crt": value(productPEM("CERTIFICATE", f.root.Raw)), "client.crl": value(oldBundle), "tls.crt": value(productPEM("CERTIFICATE", leaf) + productPEM("CERTIFICATE", f.ica.Raw) + productPEM("CERTIFICATE", f.root.Raw)), "tls.key": value(productPEM("PRIVATE KEY", private))}}
	return original
}
