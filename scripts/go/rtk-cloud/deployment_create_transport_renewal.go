package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initialTransportRenewal struct {
	Schema    string            `json:"schema"`
	Parent    string            `json:"parent_plan_sha256"`
	Review    string            `json:"review_sha256"`
	Material  map[string]string `json:"material_sha256"`
	Owners    map[string]string `json:"owners"`
	Completed bool              `json:"completed"`
}

func readInitialTransportRenewal(store secretStore, parent string, command ...*environmentCreateExecution) (map[string]environmentCreateTransport, *initialTransportRenewal, error) {
	original, proof, err := readLegacyInitialTransportRenewal(store, parent)
	if err != nil {
		return nil, nil, err
	}
	return readActiveTransportRenewal(store, parent, original, proof, command...)
}

func readLegacyInitialTransportRenewal(store secretStore, parent string) (map[string]environmentCreateTransport, *initialTransportRenewal, error) {
	path := filepath.Join("deployment/create/material", parent, "transport-renewal.json")
	if _, err := os.Lstat(filepath.Join(store.Root, path)); os.IsNotExist(err) {
		return nil, nil, nil
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
	if err != nil {
		return nil, nil, err
	}
	var proof initialTransportRenewal
	bad := func() (map[string]environmentCreateTransport, *initialTransportRenewal, error) {
		return nil, nil, createProviderRecoveryBlock("initial transport renewal proof or original serving-key binding differs")
	}
	if json.Unmarshal(raw, &proof) != nil || proof.Schema != "rtk-cloud.initial-transport-renewal/v1" || proof.Parent != parent || !environmentPreflightDigest.MatchString(proof.Review) || !proof.Completed || len(proof.Owners) != 10 || len(proof.Material) != 8 {
		return bad()
	}
	base := filepath.Join("deployment/create/transport-renewal", proof.Review)
	completed, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "completed.json"), true)
	if err != nil || string(completed) != string(raw) {
		return bad()
	}
	transports := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		oldPath := filepath.Join("deployment/create/material", parent, role+"-transport/identities.json")
		oldRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, oldPath), true)
		if err != nil || proof.Material[oldPath] != environmentPreflightBytesSHA256(oldRaw) {
			return bad()
		}
		var old, current environmentCreateTransport
		if json.Unmarshal(oldRaw, &old) != nil {
			return bad()
		}
		for _, name := range []string{"identities.json", "ca.crt", "ca.key"} {
			file := filepath.Join(base, role+"-transport", name)
			bytes, err := readBoundedEnvironmentFile(filepath.Join(store.Root, file), true)
			if err != nil || proof.Material[file] != environmentPreflightBytesSHA256(bytes) {
				return bad()
			}
			if name == "identities.json" && json.Unmarshal(bytes, &current) != nil {
				return bad()
			}
			if name == "ca.crt" && current.CACert != string(bytes) {
				return bad()
			}
		}
		ca, err := parseFirstCertificatePEM([]byte(current.CACert))
		oldCA, oldErr := parseFirstCertificatePEM([]byte(old.CACert))
		if err != nil || oldErr != nil || ca == nil || oldCA == nil || !ca.IsCA || ca.Subject.String() != oldCA.Subject.String() || len(old.Identities) != len(current.Identities) {
			return bad()
		}
		for name, original := range old.Identities {
			leaf, exists := current.Identities[name]
			cert, err := parseFirstCertificatePEM([]byte(leaf.Certificate))
			originalCert, originalErr := parseFirstCertificatePEM([]byte(original.Certificate))
			if !exists || leaf.Key != original.Key || err != nil || originalErr != nil || cert == nil || originalCert == nil || cert.CheckSignatureFrom(ca) != nil || cert.Subject.String() != originalCert.Subject.String() || deploymentplan.Digest(cert.DNSNames) != deploymentplan.Digest(originalCert.DNSNames) || deploymentplan.Digest(cert.IPAddresses) != deploymentplan.Digest(originalCert.IPAddresses) {
				return bad()
			}
			pub, _ := x509.MarshalPKIXPublicKey(cert.PublicKey)
			oldPub, _ := x509.MarshalPKIXPublicKey(originalCert.PublicKey)
			if string(pub) != string(oldPub) {
				return bad()
			}
		}
		transports[role] = current
	}
	return transports, &proof, nil
}

func verifyNoInitialIssuers(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend) error {
	script := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c 'SELECT count(*) FROM pki_issuers'"
	raw, err := b.command(ctx, nil, "-n", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "-c", "postgres", "--", "/bin/sh", "-ec", script)
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		return createProviderRecoveryBlock("temporary transport renewal requires no native PKI issuers; managed identity cannot use this profile")
	}
	return nil
}

func loadAcceptedInitialAccountCheckpoint(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var source, verifiedStage string
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/account-bootstrap" && entry.State == "completed" {
			var proof struct {
				Source string `json:"original_plan_sha256"`
				Stage  string `json:"actual_retained_verification_stage"`
			}
			if json.Unmarshal(entry.Receipt, &proof) != nil {
				return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("completed Account Manager provenance unavailable")
			}
			source, verifiedStage = proof.Source, proof.Stage
		}
	}
	var original deploymentplan.Plan
	var originalJournal deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(source) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", source, "plan.json"), &original) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", source, "journal.json"), &originalJournal) != nil || original.SHA256() != source || len(original.Resources) != 2 || len(originalJournal.Owners) != 2 || originalJournal.InFlight != "" || originalJournal.Failed != "deployment.post-deploy.workloads" {
		return nil, originalJournal, createProviderRecoveryBlock("original Account Manager creation is not complete and known")
	}
	var acceptedSHA string
	for _, entry := range e.Journal.Entries {
		if entry.Key == verifiedStage && entry.State == "completed" {
			var proof struct {
				Plan string `json:"plan_sha256"`
			}
			if json.Unmarshal(entry.Receipt, &proof) == nil {
				acceptedSHA = proof.Plan
			}
		}
	}
	var accepted deploymentplan.Plan
	var acceptedJournal deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(acceptedSHA) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", acceptedSHA, "plan.json"), &accepted) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", acceptedSHA, "journal.json"), &acceptedJournal) != nil || accepted.SHA256() != acceptedSHA || !acceptedJournal.Accepted || acceptedJournal.InFlight != "" || acceptedJournal.Failed != "" || len(accepted.Resources) != 2 {
		return nil, originalJournal, createProviderRecoveryBlock("original Account Manager retained verification is incomplete")
	}
	for _, r := range original.Resources {
		found := false
		for _, actual := range accepted.Resources {
			if actual.ID == r.ID && actual.Action == "retain" && deploymentplan.Digest(actual.Desired) == deploymentplan.Digest(r.Desired) && acceptedJournal.Owners[r.ID] == originalJournal.Owners[r.ID] {
				found = true
			}
		}
		if !found {
			return nil, originalJournal, createProviderRecoveryBlock("accepted Account Manager owner or body differs from original creation")
		}
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = source, deploymentplan.Digest(originalJournal)
	return &original, originalJournal, nil
}

func renewInitialTransportMaterial(old environmentCreateTransport, now time.Time) (environmentCreateTransport, []byte, error) {
	var out environmentCreateTransport
	ca, err := parseFirstCertificatePEM([]byte(old.CACert))
	if err != nil || ca == nil || !ca.IsCA || len(old.Identities) == 0 {
		return out, nil, createProviderRecoveryBlock("original temporary authority unavailable")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return out, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return out, nil, err
	}
	root := &x509.Certificate{SerialNumber: serial, Subject: ca.Subject, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: ca.KeyUsage}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		return out, nil, err
	}
	root, err = x509.ParseCertificate(der)
	if err != nil {
		return out, nil, err
	}
	out = environmentCreateTransport{CACert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Identities: map[string]environmentCreateTransportIdentity{}}
	for name, identity := range old.Identities {
		leaf, err := parseFirstCertificatePEM([]byte(identity.Certificate))
		if err != nil || leaf == nil || leaf.CheckSignatureFrom(ca) != nil {
			return out, nil, createProviderRecoveryBlock("original temporary leaf lacks its authority")
		}
		block, _ := pem.Decode([]byte(identity.Key))
		if block == nil {
			return out, nil, createProviderRecoveryBlock("original temporary serving key unavailable")
		}
		private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return out, nil, err
		}
		leafKey, ok := private.(*ecdsa.PrivateKey)
		if !ok {
			return out, nil, createProviderRecoveryBlock("original serving key is not the admitted P-256 key")
		}
		pub, _ := x509.MarshalPKIXPublicKey(&leafKey.PublicKey)
		originalPub, _ := x509.MarshalPKIXPublicKey(leaf.PublicKey)
		if string(pub) != string(originalPub) {
			return out, nil, createProviderRecoveryBlock("original leaf/key identity differs")
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return out, nil, err
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: leaf.Subject, DNSNames: leaf.DNSNames, IPAddresses: leaf.IPAddresses, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: leaf.KeyUsage, ExtKeyUsage: leaf.ExtKeyUsage}
		der, err := x509.CreateCertificate(rand.Reader, template, root, &leafKey.PublicKey, key)
		if err != nil {
			return out, nil, err
		}
		out.Identities[name] = environmentCreateTransportIdentity{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Key: identity.Key}
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return out, nil, err
	}
	return out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
}

func initialTransportOverride(original deploymentplan.Object, transports map[string]environmentCreateTransport) (deploymentplan.Object, bool) {
	name := deploymentplan.String(deploymentplan.Map(original["metadata"])["name"])
	kind := deploymentplan.String(original["kind"])
	var data map[string]string
	if kind == "Secret" {
		role, identity := "management", strings.TrimSuffix(strings.TrimPrefix(name, "pki-create-"), "-transport")
		if name == "openbao-transport-tls" {
			role, identity = "openbao", "openbao"
		} else if !strings.HasPrefix(name, "pki-create-") || !strings.HasSuffix(name, "-transport") {
			return original, false
		}
		t := transports[role]
		leaf, ok := t.Identities[identity]
		if !ok {
			return original, false
		}
		data = map[string]string{"ca.crt": t.CACert, "tls.crt": leaf.Certificate, "tls.key": leaf.Key}
	} else if kind == "ConfigMap" && (name == "pki-create-transport-trust" || name == "pki-openbao-transport-ca") {
		role := "management"
		if name == "pki-openbao-transport-ca" {
			role = "openbao"
		}
		data = map[string]string{"ca.crt": transports[role].CACert}
	} else {
		return original, false
	}
	out := deploymentplan.Clone(original)
	fields := deploymentplan.Map(out["data"])
	for k, v := range data {
		if kind == "Secret" {
			v = base64.StdEncoding.EncodeToString([]byte(v))
		}
		fields[k] = v
	}
	return out, true
}

func renewEnvironmentInitialTransports(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, foundation *deploymentplan.Plan, journal deploymentplan.Journal, review string) (resultErr error) {
	leasePlan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", BindingsSHA256: review}}
	lease, err := b.AcquireExecution(ctx, leasePlan)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	base := filepath.Join("deployment/create/transport-renewal", review)
	key := "initial-transport-renewal-" + review
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(map[string]string{"review_sha256": review, "profile": "before-native-issuers-reuse-original-leaf-keys"}), time.Now().UTC()); err != nil {
		return err
	}
	transports := map[string]environmentCreateTransport{}
	proof := initialTransportRenewal{Schema: "rtk-cloud.initial-transport-renewal/v1", Parent: e.Journal.PlanSHA256, Review: review, Material: map[string]string{}, Owners: map[string]string{}}
	for _, role := range []string{"management", "openbao"} {
		oldPath := filepath.Join("deployment/create/material", proof.Parent, role+"-transport/identities.json")
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, oldPath), true)
		if err != nil {
			return err
		}
		proof.Material[oldPath] = environmentPreflightBytesSHA256(raw)
		var old environmentCreateTransport
		if json.Unmarshal(raw, &old) != nil {
			return createProviderRecoveryBlock("original transport material unreadable")
		}
		material, caKey, err := renewInitialTransportMaterial(old, time.Now().UTC())
		if err != nil {
			return err
		}
		transports[role] = material
		for name, value := range map[string][]byte{"identities.json": mustProviderRecoveryJSON(material), "ca.crt": []byte(material.CACert), "ca.key": caKey} {
			path := filepath.Join(base, role+"-transport", name)
			if err := writeEnvironmentCreateDerivedPrivateFile(store, path, value); err != nil {
				return err
			}
			proof.Material[path] = environmentPreflightBytesSHA256(value)
		}
	}
	for _, r := range foundation.Resources {
		desired, changed := initialTransportOverride(r.Desired, transports)
		if !changed {
			continue
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := lease.Check(ctx); err != nil {
			return err
		}
		actual, err := b.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return createProviderRecoveryBlock("temporary transport resource differs from its original foundation")
		}
		body := deploymentplan.Clone(actual)
		body["data"] = desired["data"]
		delete(body, "status")
		part := key + "-" + strings.ReplaceAll(r.ID, "/", "-")
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "original", strings.ReplaceAll(r.ID, "/", "-")+".json"), mustProviderRecoveryJSON(actual)); err != nil {
			return err
		}
		if err := e.BeginEntry(part, "recovery", mustProviderRecoveryJSON(body), time.Now().UTC()); err != nil {
			return err
		}
		if _, err := b.Write(ctx, "update", body, true); err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := lease.Check(ctx); err != nil {
			return err
		}
		if _, err := b.Write(ctx, "update", body, false); err != nil {
			return err
		}
		readback, err := b.Read(ctx, r.ID)
		if err != nil || readback == nil || deploymentplan.String(deploymentplan.Map(readback["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(desired, readback) {
			return createProviderRecoveryBlock("temporary transport renewal readback differs")
		}
		proof.Owners[r.ID] = journal.Owners[r.ID]
		if err := e.RecordReceipt(part, mustProviderRecoveryJSON(map[string]string{"resource": r.ID, "uid": journal.Owners[r.ID], "desired_sha256": deploymentplan.Digest(desired)})); err != nil {
			return err
		}
		if err := e.CompleteEntry(part, time.Now().UTC()); err != nil {
			return err
		}
	}
	if len(proof.Owners) != 10 {
		return createProviderRecoveryBlock("complete initial transport projection set was not renewed")
	}
	leaf := transports["openbao"].Identities["openbao"]
	script := "set -eu; for n in $(seq 1 90); do actual=$(sha256sum /openbao/transport/tls.crt | cut -d' ' -f1); ca=$(sha256sum /openbao/transport/ca.crt | cut -d' ' -f1); if [ \"$actual\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Certificate)) + "\" ] && [ \"$ca\" = \"" + environmentPreflightBytesSHA256([]byte(transports["openbao"].CACert)) + "\" ]; then kill -HUP 1; exit 0; fi; sleep 2; done; exit 1"
	if _, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script); err != nil {
		return err
	}
	if _, _, err := verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, b); err != nil {
		return err
	}
	if err := verifyNoInitialIssuers(ctx, cfg, b); err != nil {
		return err
	}
	proof.Completed = true
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "completed.json"), mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	if err := store.write(filepath.Join("deployment/create/material", proof.Parent, "transport-renewal.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		return err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
