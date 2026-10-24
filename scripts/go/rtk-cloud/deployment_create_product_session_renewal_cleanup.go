package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productSessionRenewalApplyKey struct{}
type productSessionRenewalCleanupProof struct {
	SourceSHA256, OriginalSessionID, SessionID, ExpiredCertificateSHA256, RenewedCertificateSHA256, KeySHA256, CSRSHA256 string
}

func productSessionRenewalForRuntime(store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) (*createNormalTailProductLifecycle, *productSessionRenewal, error) {
	if runtime.ProductIdentityScope == "" {
		return nil, nil, nil
	}
	if !environmentPreflightDigest.MatchString(runtime.ProductIdentityScope) || runtime.Execution == nil {
		return nil, nil, productSessionRenewalBlock("cleanup original scope")
	}
	// Renewal is optional for ordinary and fresh product sessions. Select its
	// exact scoped state before requiring the normal-tail program that owns it.
	// Once any renewal evidence exists, missing source must still fail closed.
	dir, err := store.safePath(filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "session-renewal"))
	if err != nil {
		return nil, nil, err
	}
	if _, err = os.Lstat(dir); os.IsNotExist(err) {
		prefix := "kubernetes/product-identities-" + runtime.ProductIdentityScope[:12] + "-session-renewal-"
		candidate, _, _ := productSessionRenewalIdentity(runtime.ExecutionSHA256, runtime.ProductIdentityScope, s.BootstrapSessionID)
		policy := "openbao-policy-rtk-" + runtime.Execution.Journal.Environment + "-service-bootstrap-" + candidate
		for _, entry := range runtime.Execution.Journal.Entries {
			if strings.HasPrefix(entry.Key, prefix) || strings.HasPrefix(entry.Key, "product-session-renewal-retire-") || entry.Key == policy {
				return nil, nil, productSessionRenewalBlock("cleanup renewal state is missing")
			}
		}
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	p, err := readCreateNormalTailProgram(store, runtime.Execution)
	if err != nil {
		return nil, nil, err
	}
	if p.Products == nil || productIdentityScope(p.Products) != runtime.ProductIdentityScope {
		return nil, nil, productSessionRenewalBlock("cleanup lifecycle")
	}
	r, err := readProductSessionRenewal(store, runtime.Execution, p.Products)
	if err != nil {
		return nil, nil, err
	}
	if r == nil {
		return nil, nil, productSessionRenewalBlock("cleanup renewal completion is missing")
	}
	if deploymentplan.Digest(r.Installed.Ceremony) != deploymentplan.Digest(s) {
		return nil, nil, productSessionRenewalBlock("cleanup effective session")
	}
	return p.Products, r, nil
}

// Idempotent hardlinks preserve both public certificates while the native
// cleanup command continues to own precisely its canonical locked directory.
// The same finalizer intent remains pending until cleanup is re-observed.
func prepareProductSessionRenewalCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, finalized environmentCreateBootstrapFinalization) error {
	l, r, err := productSessionRenewalForRuntime(store, runtime, s)
	if err != nil || r == nil {
		return err
	}
	if finalized.SessionID != s.BootstrapSessionID || finalized.Environment != cfg.Environment || finalized.FinalizedAt.IsZero() {
		return productSessionRenewalBlock("genuine finalization before key cleanup")
	}
	// The native tombstone can precede expired-copy retirement and the final
	// parent receipt. Reconcile it without reading or recreating erased keys.
	marker, markerErr := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	if markerErr == nil && len(bytes.TrimSpace(marker)) > 0 {
		var tombstone managedLateCustodyTombstone
		var proof productSessionRenewalCleanupProof
		present, er := readProductSessionRenewalJSON(store, l, "cleanup-source.json", &proof, r.Source.Generation)
		if er != nil || !present || decodeEnvironmentJSON(marker, &tombstone) != nil || tombstone.Schema != "rtk-cloud.bootstrap-credentials/v1" || !managedLateFinalizationEqual(tombstone.Finalization, finalized) || tombstone.Root != s.Root.RootFingerprint || len(tombstone.Files) != 3 || tombstone.Files["bootstrap.key"] != r.Source.KeySHA256 || tombstone.Files["bootstrap.csr"] != r.Source.CSRSHA256 || tombstone.Files["bootstrap.crt"] != proof.RenewedCertificateSHA256 || proof.SourceSHA256 != r.SourceSHA256 || proof.SessionID != s.BootstrapSessionID || proof.OriginalSessionID != r.Source.OriginalSession.Session || proof.ExpiredCertificateSHA256 != r.Source.OriginalCertificateSHA256 || proof.KeySHA256 != r.Source.KeySHA256 || proof.CSRSHA256 != r.Source.CSRSHA256 {
			return productSessionRenewalBlock("genuine renewed cleanup tombstone")
		}
		for path, expected := range map[string]string{r.Source.CertificatePath: proof.RenewedCertificateSHA256, "/run/pki-bootstrap/expired-" + r.Source.OriginalSession.Session + "/bootstrap.crt": r.Source.OriginalCertificateSHA256} {
			raw, readErr := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
			if readErr != nil || environmentPreflightBytesSHA256(raw) != expected {
				return productSessionRenewalBlock("cleanup retained public certificate hashes")
			}
			if path == r.Source.CertificatePath {
				leaf, er := parseFirstCertificatePEM(raw)
				if er != nil || environmentPreflightBytesSHA256(leaf.Raw) != finalized.LeafSHA256 {
					return productSessionRenewalBlock("cleanup finalized public leaf")
				}
			}
		}
		if r.Source.Generation == 2 {
			first, er := readProductSessionRenewalFirst(store, runtime.Execution, l)
			if er != nil || first == nil {
				return productSessionRenewalBlock("first public archive lineage")
			}
			archive := "/run/pki-bootstrap/expired-" + first.Source.OriginalSession.Session + "/bootstrap.crt"
			raw, er := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", archive)
			if er != nil || environmentPreflightBytesSHA256(raw) != first.Source.OriginalCertificateSHA256 {
				return productSessionRenewalBlock("original public archive hash after tombstone")
			}
		}
		// Native cleanup validates each remaining file against this exact
		// tombstone and idempotently finishes any interrupted unlink prefix.

		return runtime.Execution.Check(ctx)
	}
	material := map[string][]byte{}
	oldArchive := "/run/pki-bootstrap/expired-" + r.Source.OriginalSession.Session + "/bootstrap.crt"
	for key, path := range map[string]string{"key": "/run/pki-bootstrap/bootstrap.key", "csr": "/run/pki-bootstrap/bootstrap.csr", "renewed": r.Source.CertificatePath} {
		v, er := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if er != nil {
			return productSessionRenewalBlock("finalized original custody")
		}
		material[key] = v
	}
	if environmentPreflightBytesSHA256(material["key"]) != r.Source.KeySHA256 || environmentPreflightBytesSHA256(material["csr"]) != r.Source.CSRSHA256 {
		return productSessionRenewalBlock("cleanup original key and CSR")
	}
	pair, err := tls.X509KeyPair(material["renewed"], material["key"])
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, s) || environmentPreflightBytesSHA256(pair.Certificate[0]) != finalized.LeafSHA256 {
		return productSessionRenewalBlock("finalized renewed certificate")
	}
	proof := productSessionRenewalCleanupProof{SourceSHA256: r.SourceSHA256, OriginalSessionID: r.Source.OriginalSession.Session, SessionID: s.BootstrapSessionID, ExpiredCertificateSHA256: r.Source.OriginalCertificateSHA256, RenewedCertificateSHA256: environmentPreflightBytesSHA256(material["renewed"]), KeySHA256: r.Source.KeySHA256, CSRSHA256: r.Source.CSRSHA256}
	if err = productSessionRenewalWrite(store, l, "cleanup-source.json", proof, r.Source.Generation); err != nil {
		return err
	}
	script := productSessionRenewalCleanupScript(r, proof)
	if r.Source.Generation == 2 {
		first, er := readProductSessionRenewalFirst(store, runtime.Execution, l)
		if er != nil || first == nil {
			return productSessionRenewalBlock("cleanup first generation")
		}
		script = productSessionRenewalNextCleanupPrefix(first, r.Source.OriginalCertificateSHA256) + script
	}
	if err = runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if _, err = runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", script); err != nil {
		return productSessionRenewalBlock("canonical finalized certificate reconciliation")
	}
	for path, hash := range map[string]string{oldArchive: r.Source.OriginalCertificateSHA256, "/run/pki-bootstrap/bootstrap.crt": proof.RenewedCertificateSHA256} {
		v, er := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if er != nil || environmentPreflightBytesSHA256(v) != hash {
			return productSessionRenewalBlock("preserved certificate hashes")
		}
	}
	return runtime.Execution.Check(ctx)
}
func productSessionRenewalOriginalRetireDefinitions(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal) ([]deploymentplan.Definition, error) {
	if r == nil || validateProductSessionRenewalSource(store, e, l, r.Source) != nil {
		return nil, productSessionRenewalBlock("retirement source")
	}
	secret, sj, err := productSessionRenewalPriorCopyStage(store, e, l, r, "credentials")
	if err != nil {
		return nil, err
	}
	controls, cj, err := productSessionRenewalPriorCopyStage(store, e, l, r, "controls")
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{"Secret/" + r.Original.Ceremony.BootstrapCredentialNamespace + "/" + r.Original.Ceremony.BootstrapCredentialSecret: true, "ConfigMap/" + r.Original.Ceremony.BootstrapCredentialNamespace + "/" + r.Original.Ceremony.CleanupBinding.ConfigMapName: true}
	var out []deploymentplan.Definition
	for _, pair := range []struct {
		p *deploymentplan.Plan
		j deploymentplan.Journal
	}{{secret, sj}, {controls, cj}} {
		for _, resource := range pair.p.Resources {
			if !ids[resource.ID] {
				continue
			}
			uid := pair.j.Owners[resource.ID]
			if !environmentBootstrapOwnerUUID.MatchString(uid) {
				return nil, productSessionRenewalBlock("original retirement UID")
			}
			o := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = uid
			out = append(out, deploymentplan.Definition{Phase: "retirement", Retire: true, Object: o})
			delete(ids, resource.ID)
		}
	}
	if len(ids) != 0 || len(out) != 2 {
		return nil, productSessionRenewalBlock("original expired copies")
	}
	if r.Source.Generation == 2 {
		first, er := readProductSessionRenewalFirst(store, e, l)
		if er != nil || first == nil {
			return nil, productSessionRenewalBlock("original copy lineage")
		}
		initial, er := productSessionRenewalOriginalRetireDefinitions(store, e, l, first)
		if er != nil {
			return nil, er
		}
		out = append(initial, out...)
	}
	return out, nil
}
func productSessionRenewalRetirePolicyIntent(cfg deploymentConfig, r *productSessionRenewal) deploymentplan.Object {
	return deploymentplan.Object{"policy": "rtk-" + cfg.Environment + "-service-bootstrap-" + r.Source.OriginalSession.Session, "policy_sha256": r.Source.SignerPolicySHA256, "source_sha256": r.SourceSHA256}
}
func productSessionRenewalRetirePolicyKey(r *productSessionRenewal) string {
	return "openbao-policy-retire-product-session-" + r.Source.OriginalSession.Session
}
func retireProductSessionRenewalOriginal(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, finalized environmentCreateBootstrapFinalization) error {
	l, r, err := productSessionRenewalForRuntime(store, runtime, s)
	if err != nil || r == nil {
		return err
	}
	if finalized.SessionID != s.BootstrapSessionID || finalized.FinalizedAt.IsZero() {
		return productSessionRenewalBlock("new finalization before expired authorization retirement")
	}
	old, err := readProductSessionRenewalNative(ctx, cfg, runtime, r.Source.OriginalSession.Session)
	if err != nil || !productSessionRenewalOldNativeMatches(r.Source, old) || old.Status != "expired" {
		return productSessionRenewalBlock("preserved expired native history")
	}
	definitions, err := productSessionRenewalOriginalRetireDefinitions(store, runtime.Execution, l, r)
	if err != nil {
		return err
	}
	proofs := deploymentplan.Object{}
	for _, definition := range definitions {
		spec := productSessionRenewalCopyRetireSpec(r, definition)
		if err = retireProductSessionRenewalCopy(ctx, runtime, definition, spec); err != nil {
			return err
		}
		entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, spec.Key)
		proofs[deploymentplan.ID(definition.Object)] = deploymentplan.Digest(jsonRawObject(entry.Receipt))
	}
	policies, er := productSessionRenewalPoliciesToRetire(store, runtime.Execution, l, r)
	if er != nil {
		return er
	}
	for _, prior := range policies {
		old, er := readProductSessionRenewalNative(ctx, cfg, runtime, prior.Source.OriginalSession.Session)
		if er != nil || !productSessionRenewalOldNativeMatches(prior.Source, old) || old.Status != "expired" {
			return productSessionRenewalBlock("all preserved expired native histories")
		}
		if err = retireProductSessionRenewalPolicy(ctx, cfg, runtime, prior); err != nil {
			return err
		}
	}
	return productSessionRenewalWrite(store, l, "cleanup-completed.json", deploymentplan.Object{"source_sha256": r.SourceSHA256, "new_finalization": finalized, "retirements": proofs, "old_policy_absent": true}, r.Source.Generation)
}
func retireProductSessionRenewalPolicy(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, r *productSessionRenewal) error {
	intent := productSessionRenewalRetirePolicyIntent(cfg, r)
	key := productSessionRenewalRetirePolicyKey(r)
	name := deploymentplan.String(intent["policy"])
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, key)
	if entry != nil && (entry.Phase != "bootstrap" || entry.IntentSHA256 != deploymentplan.Digest(intent) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return productSessionRenewalBlock("old policy uncertain intent")
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return productSessionRenewalBlock("original policy owner")
	}
	input := append(bytes.TrimSpace(token), '\n')
	list, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy list -format=json\n")
	var names []string
	if err != nil || json.Unmarshal(list, &names) != nil {
		return productSessionRenewalBlock("old policy existence readback")
	}
	exists := slices.Contains(names, name)
	proof := deploymentplan.Clone(intent)
	proof["absent"] = true
	if entry != nil && entry.State == "completed" {
		if exists || activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != deploymentplan.Digest(proof) {
			return productSessionRenewalBlock("old policy retirement receipt")
		}
		return runtime.Execution.Check(ctx)
	}
	if exists {
		if entry != nil && entry.State == "allocated" {
			return productSessionRenewalBlock("allocated old policy reappeared")
		}
		expected, er := readProductSessionRenewalPolicy(ctx, cfg, runtime, r.Installed.Ceremony.Intermediate.ID)
		if er != nil || environmentPreflightBytesSHA256([]byte(expected.ServiceClientSignerPolicy)) != r.Source.SignerPolicySHA256 {
			return productSessionRenewalBlock("bound original policy")
		}
		text, er := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+name+"\n")
		if er != nil || strings.TrimSpace(string(text)) != strings.TrimSpace(expected.ServiceClientSignerPolicy) {
			return productSessionRenewalBlock("original policy body")
		}
		if entry == nil {
			if err = runtime.Execution.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
				return err
			}
		}
		// A definitive unchanged policy is the same delete target; DELETE is
		// idempotent and cannot issue or recreate identity after a lost response.
		if _, err = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy delete "+name+" >/dev/null\n"); err != nil {
			return productSessionRenewalBlock("old policy deletion uncertain")
		}
		list, err = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy list -format=json\n")
		if err != nil || json.Unmarshal(list, &names) != nil || slices.Contains(names, name) {
			return productSessionRenewalBlock("old policy absence")
		}
	} else if entry == nil {
		return productSessionRenewalBlock("unrecorded old policy absence")
	}

	if err = runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

func productSessionRenewalCleanupScript(r *productSessionRenewal, proof productSessionRenewalCleanupProof) string {
	return fmt.Sprintf(`set -eu
cd /run/pki-bootstrap
test ! -L /run/pki-bootstrap; test ! -L bootstrap.crt; test ! -L '%s'
archive='%s'
test ! -L "$archive"; test ! -L "$(dirname "$archive")"
if test -e "$archive"; then
 directory=$(dirname "$archive"); test -d "$directory"; case "$(stat -c '%%a %%u %%g' "$directory")" in '700 10001 10001'|'2700 10001 10001') ;; *) exit 1;; esac
 printf '%%s  %%s\n' '%s' "$archive" | sha256sum -c - >/dev/null
else
 printf '%%s  bootstrap.crt\n' '%s' | sha256sum -c - >/dev/null
 directory=$(dirname "$archive")
 if test -e "$directory"; then test -d "$directory"; case "$(stat -c '%%a %%u %%g' "$directory")" in '700 10001 10001'|'2700 10001 10001') ;; *) exit 1;; esac; else mkdir -m 0700 "$directory"; fi
 ln bootstrap.crt "$archive"
fi
printf '%%s  %%s\n' '%s' '%s' | sha256sum -c - >/dev/null
if test -e bootstrap.crt; then
 current=$(sha256sum bootstrap.crt); current=${current%%%% *}
 if test "$current" != '%s'; then test "$current" = '%s'; rm bootstrap.crt; fi
fi
if test ! -e bootstrap.crt; then ln '%s' bootstrap.crt; fi
printf '%%s  bootstrap.crt\n' '%s' | sha256sum -c - >/dev/null
`, r.Source.CertificatePath, "/run/pki-bootstrap/expired-"+r.Source.OriginalSession.Session+"/bootstrap.crt", r.Source.OriginalCertificateSHA256, r.Source.OriginalCertificateSHA256, proof.RenewedCertificateSHA256, r.Source.CertificatePath, proof.RenewedCertificateSHA256, r.Source.OriginalCertificateSHA256, r.Source.CertificatePath, proof.RenewedCertificateSHA256)
}
