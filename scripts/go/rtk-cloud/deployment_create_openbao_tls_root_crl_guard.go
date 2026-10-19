// PRIVATE standalone guards; shared verbatim with the future concrete adapter.
package main

import (
	"fmt"
	"strings"
	"time"
)

type openBaoTLSRootCRLACK struct {
	IssuerID string    `json:"issuer_id"`
	Digest   string    `json:"digest"`
	Consumer string    `json:"consumer_id"`
	At       time.Time `json:"acknowledged_at"`
}

func openBaoTLSRootCRLACKsValid(rows []openBaoTLSRootCRLACK, issuer, digest string, published, now time.Time) bool {
	if published.IsZero() || published.After(now) || len(rows) != 2 {
		return false
	}
	for i, name := range []string{"certissuer", "pki-controller"} {
		row := rows[i]
		if row.IssuerID != issuer || row.Digest != digest || row.Consumer != name || row.At.IsZero() || row.At.Before(published) || row.At.After(now) {
			return false
		}
	}
	return true
}

func validateOpenBaoTLSRootCRLNativeResult(raw []byte, next time.Time, requestSHA, signedSHA string, execute bool) bool {
	if next.IsZero() || len(requestSHA) != 64 || strings.Trim(requestSHA, "0123456789abcdef") != "" || len(signedSHA) != 64 || strings.Trim(signedSHA, "0123456789abcdef") != "" {
		return false
	}
	expected := fmt.Sprintf("validated dev openbao_tls Root CRL #2 through %s; no changes made; reviewed request %s", next.Format(time.RFC3339), requestSHA)
	if execute {
		expected = fmt.Sprintf("imported dev openbao_tls Root CRL #2 digest %s; reviewed request %s", signedSHA, requestSHA)
	}
	return len(raw) <= 16<<10 && strings.TrimSpace(string(raw)) == expected
}
