package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOriginalOpenBaoTLSRoot2RequiresBothExactNativeConsumersAfterActualPublication(t *testing.T) {
	at := time.Date(2026, 10, 13, 23, 0, 0, 0, time.UTC)
	issuer := "734487b8-abcf-40be-a813-8b52c64f7954"
	digest := strings.Repeat("2", 64)
	original := []openBaoTLSRootCRLACK{{IssuerID: issuer, Digest: digest, Consumer: "certissuer", At: at.Add(time.Second)}, {IssuerID: issuer, Digest: digest, Consumer: "pki-controller", At: at.Add(2 * time.Second)}}
	for _, mode := range []string{"exact", "missing", "extra", "duplicate", "mqtt-consumer", "foreign-consumer", "other-root", "old-crl", "before-published", "future-ack", "zero-ack", "zero-publication", "future-publication", "reordered"} {
		t.Run(mode, func(t *testing.T) {
			rows := slices.Clone(original)
			published, now := at, at.Add(time.Minute)
			switch mode {
			case "missing":
				rows = rows[:1]
			case "extra":
				rows = append(rows, rows[0])
			case "duplicate":
				rows[1] = rows[0]
			case "mqtt-consumer":
				rows[0].Consumer = "video-cloud-api"
			case "foreign-consumer":
				rows[0].Consumer = "service:certissuer"
			case "other-root":
				rows[0].IssuerID = "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
			case "old-crl":
				rows[0].Digest = strings.Repeat("1", 64)
			case "before-published":
				rows[0].At = at.Add(-time.Second)
			case "future-ack":
				rows[0].At = now.Add(time.Second)
			case "zero-ack":
				rows[0].At = time.Time{}
			case "zero-publication":
				published = time.Time{}
			case "future-publication":
				published = now.Add(time.Second)
			case "reordered":
				slices.Reverse(rows)
			}
			if openBaoTLSRootCRLACKsValid(rows, issuer, digest, published, now) != (mode == "exact") {
				t.Fatal("wrong actual native two-consumer proof", mode)
			}
		})
	}
}
func TestOpenBaoTLSNativeCapabilityMustReturnExactBoundedReviewedOutput(t *testing.T) {
	next := time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)
	request, digest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, execute := range []bool{false, true} {
		for _, mode := range []string{"exact", "trailing-newline", "unsupported", "mqtt", "service", "unknown-domain", "wrong-number", "wrong-request", "wrong-digest", "wrong-epoch", "extra-line", "oversized", "malformed-input"} {
			t.Run(fmt.Sprintf("%t/%s", execute, mode), func(t *testing.T) {
				raw := fmt.Sprintf("validated dev openbao_tls Root CRL #2 through %s; no changes made; reviewed request %s", next.Format(time.RFC3339), request)
				if execute {
					raw = fmt.Sprintf("imported dev openbao_tls Root CRL #2 digest %s; reviewed request %s", digest, request)
				}
				inputRequest := request
				switch mode {
				case "trailing-newline":
					raw += "\n"
				case "unsupported":
					raw = "unknown command"
				case "mqtt":
					raw = strings.ReplaceAll(raw, "openbao_tls", "mqtt")
				case "service":
					raw = strings.ReplaceAll(raw, "openbao_tls", "service")
				case "unknown-domain":
					raw = strings.ReplaceAll(raw, "openbao_tls", "other")
				case "wrong-number":
					raw = strings.Replace(raw, "#2", "#3", 1)
				case "wrong-request":
					raw = strings.ReplaceAll(raw, request, strings.Repeat("c", 64))
				case "wrong-digest":
					if execute {
						raw = strings.ReplaceAll(raw, digest, strings.Repeat("c", 64))
					} else {
						raw += " digest " + digest
					}
				case "wrong-epoch":
					if !execute {
						raw = strings.Replace(raw, next.Format(time.RFC3339), next.Add(time.Second).Format(time.RFC3339), 1)
					} else {
						raw += " through " + next.Add(time.Second).Format(time.RFC3339)
					}
				case "extra-line":
					raw += "\nother output"
				case "oversized":
					raw += strings.Repeat(" ", 17<<10)
				case "malformed-input":
					inputRequest = "not-reviewed"
				}
				if validateOpenBaoTLSRootCRLNativeResult([]byte(raw), next, inputRequest, digest, execute) != (mode == "exact" || mode == "trailing-newline") {
					t.Fatal("unsupported or drifting native result admitted", mode)
				}
			})
		}
	}
}
