package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productSessionRenewalPriorCopyStage(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal, suffix string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if r.Source.Generation == 2 {
		first, err := readProductSessionRenewalFirst(store, e, l)
		if err != nil || first == nil {
			return nil, deploymentplan.Journal{}, productSessionRenewalBlock("prior copy generation")
		}
		return acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, first.Source, suffix))
	}
	key := "session-controls"
	if suffix == "credentials" {
		key = "service-credential-secrets"
	}
	return acceptedCreateStageForProduct(store, e, l, key)
}
func productSessionRenewalPoliciesToRetire(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal) ([]*productSessionRenewal, error) {
	if r.Source.Generation == 0 {
		return []*productSessionRenewal{r}, nil
	}
	first, err := readProductSessionRenewalFirst(store, e, l)
	if err != nil || first == nil {
		return nil, productSessionRenewalBlock("prior policy history")
	}
	// The latest source authenticates both generations. Only the original policy
	// name differs; the latest receipt never claims the earlier child succeeded.
	prior := *first
	prior.SourceSHA256 = r.SourceSHA256
	return []*productSessionRenewal{&prior, r}, nil
}
func productSessionRenewalNextCleanupPrefix(first *productSessionRenewal, previousCertificate string) string {
	return fmt.Sprintf(`set -eu
cd /run/pki-bootstrap
test ! -L /run/pki-bootstrap; test ! -L bootstrap.crt
original_archive='/run/pki-bootstrap/expired-%s/bootstrap.crt'
test ! -L "$original_archive"; test ! -L "$(dirname "$original_archive")"
if test -e "$original_archive"; then
 original_directory=$(dirname "$original_archive"); test -d "$original_directory"; case "$(stat -c '%%a %%u %%g' "$original_directory")" in '700 10001 10001'|'2700 10001 10001') ;; *) exit 1;; esac
 printf '%%s  %%s\n' '%s' "$original_archive" | sha256sum -c - >/dev/null
else
 printf '%%s  bootstrap.crt\n' '%s' | sha256sum -c - >/dev/null
 original_directory=$(dirname "$original_archive")
 if test -e "$original_directory"; then test -d "$original_directory"; case "$(stat -c '%%a %%u %%g' "$original_directory")" in '700 10001 10001'|'2700 10001 10001') ;; *) exit 1;; esac; else mkdir -m 0700 "$original_directory"; fi
 ln bootstrap.crt "$original_archive"
fi
test ! -L '%s'
printf '%%s  %%s\n' '%s' '%s' | sha256sum -c - >/dev/null
if test -e bootstrap.crt; then
 old_current=$(sha256sum bootstrap.crt);old_current=${old_current%%%% *}
 if test "$old_current" = '%s'; then rm bootstrap.crt;ln '%s' bootstrap.crt;fi
fi
`, first.Source.OriginalSession.Session, first.Source.OriginalCertificateSHA256, first.Source.OriginalCertificateSHA256, first.Source.CertificatePath, previousCertificate, first.Source.CertificatePath, first.Source.OriginalCertificateSHA256, first.Source.CertificatePath)
}
