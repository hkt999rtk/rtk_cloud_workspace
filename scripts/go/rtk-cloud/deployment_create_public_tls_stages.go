package main

import (
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"golang.org/x/crypto/acme"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Read the two actual producer boundaries before the product protocol. Saved
// ACME responses are read only; a pending request without its order URL cannot
// authorize another issue request or a later Kubernetes publication.
func validateCreateNormalTailPublicTLSStages(store secretStore, e *environmentCreateExecution, p *environmentPublicTLSPlan, entries []environmentCreateJournalEntry) (int, []*deploymentplan.Plan, error) {
	r := &productStageReader{store: store, e: e, entries: entries}
	if len(entries) == 0 || p == nil {
		return r.result()
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil || deploymentplan.Digest(program.PublicTLS) != deploymentplan.Digest(p) {
		r.bad("public TLS source differs from its frozen program")
		return r.result()
	}
	if _, err = environmentPublicTLSOutputContract(p); err != nil {
		r.err = err
		return r.result()
	}
	r.admission, err = readCreateNormalTailProgramAdmission(store, e)
	if err != nil {
		r.err = err
		return r.result()
	}
	base := publicTLSBase(p)
	var material environmentPublicTLSMaterial
	materialErr := publicTLSReadJSON(store, filepath.Join(base, "material.json"), &material)
	if materialErr != nil && !errors.Is(materialErr, os.ErrNotExist) {
		r.bad("public TLS material is unsafe or malformed")
		return r.result()
	}
	if p.Source == nil {
		var keys environmentPublicTLSKeys
		var original acme.Order
		var account acme.Account
		if publicTLSReadJSON(store, filepath.Join(base, "keys.json"), &keys) != nil || !publicTLSOriginalKeysMatch(p, keys) || publicTLSReadJSON(store, filepath.Join(base, "order.json"), &original) != nil || !publicTLSOriginalOrderMatches(p, original) || publicTLSReadJSON(store, filepath.Join(base, "account.json"), &account) != nil || account.Status != acme.StatusValid || !publicTLSOriginalURLValid(p, account.URI) {
			r.bad("public TLS request lacks its original account, key, signed CSR and known order response")
			return r.result()
		}
		order, attemptsSHA, attempts, err := readEnvironmentPublicTLSOrderAttempts(store, p, deploymentplan.Digest(keys.CSR))
		if err != nil {
			r.err = err
			return r.result()
		}
		intent := struct{ Plan, CSR string }{deploymentplan.Digest(p), deploymentplan.Digest(keys.CSR)}
		if !r.take("public-tls/certificate", "public-tls", intent, func(entry environmentCreateJournalEntry) bool {
			if !original.Expires.IsZero() && !entry.StartedAt.Before(original.Expires) {
				return false
			}
			previous := entry.StartedAt
			for _, attempt := range attempts {
				if attempt.RequestedAt.IsZero() || attempt.RequestedAt.Before(previous) || attempt.RequestedAt.Before(r.admission.AdmittedAt) || !attempt.RequestedAt.Before(r.admission.Deadline) || attempt.RequestedAt.After(time.Now().UTC()) || entry.State == "completed" && attempt.RequestedAt.After(entry.CompletedAt) {
					return false
				}
				previous = attempt.RequestedAt
			}
			if !order.Expires.IsZero() && (!previous.Before(order.Expires) || entry.State == "completed" && !entry.CompletedAt.Before(order.Expires)) {
				return false
			}
			if entry.State == "pending" {
				return errors.Is(materialErr, os.ErrNotExist)
			}
			var receipt struct {
				Plan, Order, CSR, Material string
				Attempts                   string `json:",omitempty"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Plan != deploymentplan.Digest(p) || receipt.Order != order.URI || receipt.CSR != deploymentplan.Digest(keys.CSR) || receipt.Attempts != attemptsSHA || !environmentPreflightDigest.MatchString(receipt.Material) {
				return false
			}
			return materialErr != nil || receipt.Material == deploymentplan.Digest(material)
		}) {
			return r.result()
		}
		if r.n == len(entries) {
			return r.result() // material is saved only after the native receipt.
		}
	}
	if materialErr != nil || p.Source != nil && deploymentplan.Digest(material) != deploymentplan.Digest(p.Source) {
		r.bad("public TLS publication lacks its original issued material")
		return r.result()
	}
	if err := validateEnvironmentPublicTLSMaterial(p, material, entries[r.n].StartedAt); err != nil {
		r.err = err
		return r.result()
	}
	wanted := publicTLSDefinition(p, material, p.OwnerUID)
	child, err := readCreateNormalTailPreparationPlan(store, e, "public-tls")
	if err != nil || child == nil || child.BindingsSHA256 != environmentCreateStageBindingsSHA("public-tls", []deploymentplan.Definition{wanted}) || len(child.Resources) != 1 {
		r.bad("public TLS publication lacks its exact original standard child input")
		return r.result()
	}
	resource := child.Resources[0]
	wantedBody := deploymentplan.Clone(wanted.Object)
	delete(deploymentplan.Map(wantedBody["metadata"]), "uid")
	if resource.ID != p.SecretID || resource.Phase != wanted.Phase || resource.Action != "create" && resource.Action != "retain" || p.OwnerUID != "" && (resource.Action != "retain" || resource.UID != p.OwnerUID) || p.OwnerUID == "" && resource.Action != "create" || !deploymentplan.Matches(wantedBody, resource.Desired) || deploymentplan.Digest(wantedBody["data"]) != deploymentplan.Digest(resource.Desired["data"]) {
		r.bad("public TLS publication changed its original Secret body or ownership mode")
		return r.result()
	}
	intent, err := environmentCreateKubernetesStageIntent(child)
	if err != nil {
		r.err = err
		return r.result()
	}
	r.take("kubernetes/public-tls", "public-tls", json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
		actual, journal, err := readCreateNormalTailStage(store, e, "public-tls", child)
		if err != nil {
			return false
		}
		if actual != nil {
			r.children = append(r.children, actual)
		}
		var completion environmentPublicTLSReceipt
		completionErr := publicTLSReadJSON(store, filepath.Join(base, "completed.json"), &completion)
		if errors.Is(completionErr, os.ErrNotExist) {
			return r.n+1 == len(entries) // accepted child may precede local completion.
		}
		return completionErr == nil && entry.State == "completed" && actual != nil && completion.PlanSHA256 == deploymentplan.Digest(p) && completion.MaterialSHA256 == deploymentplan.Digest(material) && completion.SecretUID == journal.Owners[p.SecretID] && environmentBootstrapOwnerUUID.MatchString(completion.SecretUID)
	})
	return r.result()
}

func publicTLSOriginalKeysMatch(p *environmentPublicTLSPlan, keys environmentPublicTLSKeys) bool {
	if keys.PlanSHA256 != deploymentplan.Digest(p) {
		return false
	}
	account, _ := pem.Decode([]byte(keys.AccountKey))
	leaf, _ := pem.Decode([]byte(keys.LeafKey))
	der, err := base64.StdEncoding.DecodeString(keys.CSR)
	if account == nil || leaf == nil || err != nil {
		return false
	}
	accountKey, e1 := x509.ParsePKCS8PrivateKey(account.Bytes)
	leafKey, e2 := x509.ParsePKCS8PrivateKey(leaf.Bytes)
	csr, e3 := x509.ParseCertificateRequest(der)
	_, accountOK := accountKey.(crypto.Signer)
	signer, leafOK := leafKey.(crypto.Signer)
	return e1 == nil && e2 == nil && e3 == nil && accountOK && leafOK && csr.CheckSignature() == nil && slices.Equal(csr.DNSNames, p.Hosts) && deploymentplan.Digest(csr.PublicKey) == deploymentplan.Digest(signer.Public())
}

func publicTLSOriginalURLValid(p *environmentPublicTLSPlan, raw string) bool {
	u, err := url.Parse(raw)
	directory, directoryErr := url.Parse(p.DirectoryURL)
	return err == nil && directoryErr == nil && u.Scheme == "https" && u.Host != "" && u.Host == directory.Host && u.User == nil && u.Fragment == ""
}

func publicTLSOriginalOrderMatches(p *environmentPublicTLSPlan, order acme.Order) bool {
	if !publicTLSOriginalURLValid(p, order.URI) || !publicTLSOriginalURLValid(p, order.FinalizeURL) || order.CertURL != "" && !publicTLSOriginalURLValid(p, order.CertURL) || len(order.Identifiers) != len(p.Hosts) || len(order.AuthzURLs) != len(p.Hosts) {
		return false
	}
	var hosts []string
	seen := map[string]bool{}
	for _, id := range order.Identifiers {
		if id.Type != "dns" {
			return false
		}
		hosts = append(hosts, id.Value)
	}
	for _, raw := range order.AuthzURLs {
		if !publicTLSOriginalURLValid(p, raw) || seen[raw] {
			return false
		}
		seen[raw] = true
	}
	sort.Strings(hosts)
	return slices.Equal(hosts, p.Hosts)
}
