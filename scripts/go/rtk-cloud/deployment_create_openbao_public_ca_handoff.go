package main

import (
	"crypto/x509"
	"encoding/pem"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

// openBaoCAOverlap derives a public-only, ordered transition bundle from the
// original foundation CA and the already-bound managed authority. It never
// generates or replaces a root and is idempotent on the existing bundle.
func openBaoCAOverlap(original, managed, current string, expectedManaged string, now time.Time) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("OpenBao CA transition requires its original bootstrap and managed public roots")
	}
	parse := func(value string) (*x509.Certificate, bool) {
		block, rest := pem.Decode([]byte(value))
		if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return nil, false
		}
		c, err := x509.ParseCertificate(block.Bytes)
		return c, err == nil
	}
	old, ok := parse(original)
	if !ok || !old.IsCA || old.CheckSignatureFrom(old) != nil {
		return bad()
	}
	root, ok := parse(managed)
	if !ok || !root.IsCA || root.CheckSignatureFrom(root) != nil || environmentPreflightBytesSHA256(root.Raw) != expectedManaged || now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
		return bad()
	}
	original = strings.TrimSpace(original) + "\n"
	managed = strings.TrimSpace(managed) + "\n"
	if environmentPreflightBytesSHA256(old.Raw) == expectedManaged {
		return bad()
	}
	desired := original + managed
	if strings.TrimSpace(current) != strings.TrimSpace(original) && strings.TrimSpace(current) != strings.TrimSpace(desired) {
		return bad()
	}
	return desired, nil
}

func openBaoCAOverlapDefinition(source deploymentplan.Object, uid, managed, pin string, now time.Time) (deploymentplan.Definition, error) {
	if source["kind"] != "ConfigMap" || uid == "" {
		return deploymentplan.Definition{}, createProviderRecoveryBlock("original public CA ConfigMap owner is unavailable")
	}
	data := deploymentplan.Map(source["data"])
	original := deploymentplan.String(data["ca.crt"])
	desired, err := openBaoCAOverlap(original, managed, original, pin, now)
	if err != nil {
		return deploymentplan.Definition{}, err
	}
	obj := deploymentplan.Clone(source)
	deploymentplan.Map(obj["data"])["ca.crt"] = desired
	deploymentplan.Map(obj["metadata"])["uid"] = uid
	return deploymentplan.Definition{Object: obj, Phase: "configuration"}, nil
}

// validateOpenBaoListenerFile verifies the public chain currently configured
// for the OpenBao listener against the already recorded managed root.
func validateOpenBaoListenerFile(chainPEM, rootPEM, server string, now time.Time) error {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(rootPEM)) {
		return createProviderRecoveryBlock("managed OpenBao root is unavailable")
	}
	var chain []*x509.Certificate
	rest := []byte(chainPEM)
	for len(strings.TrimSpace(string(rest))) > 0 {
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return createProviderRecoveryBlock("OpenBao listener public chain is malformed")
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		chain = append(chain, c)
		rest = next
	}
	if len(chain) < 2 || server == "" {
		return createProviderRecoveryBlock("OpenBao listener public chain or server name is unavailable")
	}
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: server, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		return createProviderRecoveryBlock("OpenBao listener public chain differs from the recorded managed authority")
	}
	return nil
}

// advanceOpenBaoCASource follows only an already authenticated native
// successor receipt; a current Kubernetes body alone cannot replace source.
func advanceOpenBaoCASource(source deploymentplan.Object, uid string, writes []activeTransportRenewalResource) (deploymentplan.Object, error) {
	current := deploymentplan.Clone(source)
	seen := false
	for _, write := range writes {
		if write.ID != deploymentplan.ID(source) {
			continue
		}
		if seen || uid == "" || write.UID != uid || write.Before == nil || write.Desired == nil || !deploymentplan.Matches(current, write.Before) || deploymentplan.ID(write.Desired) != write.ID {
			return nil, createProviderRecoveryBlock("completed public CA successor differs from its original owner")
		}
		current = deploymentplan.Clone(write.Desired)
		seen = true
	}
	return current, nil
}
