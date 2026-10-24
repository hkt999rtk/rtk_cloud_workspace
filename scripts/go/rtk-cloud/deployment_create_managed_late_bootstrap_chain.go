package main

import (
	"crypto/x509"
	"slices"
)

// The selected native bootstrap producer appends IssuingCAPEM and CAChainPEM.
// Its original OpenBao response may repeat that same ICA once. Preserve those
// bytes: only leaf/ICA/Root and leaf/ICA/the identical ICA/Root are permitted.
func managedLateBootstrapChainMatches(chain [][]byte, ceremony environmentCreateServiceCeremony) bool {
	if len(chain) != 3 && len(chain) != 4 {
		return false
	}
	leaf, leafErr := x509.ParseCertificate(chain[0])
	ica, icaErr := parseFirstCertificatePEM([]byte(ceremony.Intermediate.Certificate))
	root, rootErr := parseFirstCertificatePEM([]byte(ceremony.Root.Certificate))
	if leafErr != nil || icaErr != nil || rootErr != nil || leaf == nil || ica == nil || root == nil || leaf.IsCA || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.CheckSignatureFrom(ica) != nil || ica.CheckSignatureFrom(root) != nil || root.CheckSignatureFrom(root) != nil || environmentPreflightBytesSHA256(root.Raw) != ceremony.Root.RootFingerprint || !slices.Equal(chain[1], ica.Raw) || !slices.Equal(chain[len(chain)-1], root.Raw) {
		return false
	}
	return len(chain) == 3 || slices.Equal(chain[2], ica.Raw)
}
