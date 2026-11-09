package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Publish the selected Service intermediate's signed CRL while the bootstrap
// Controller still has independent transport. Its managed replacement requires
// the same registry record to admit the core's Service TLS connections.
func publishEnvironmentCreateServiceIntermediateCRL(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, issuer environmentCreateRegisteredIssuer) error {
	if runtime.Execution == nil || runtime.Custody == nil || runtime.Custody.Generation != runtime.ExecutionSHA256 ||
		runtime.PKIClient == nil || runtime.PKIClient.Environment != cfg.Environment ||
		issuer.Environment != cfg.Environment || issuer.Domain != "service" || issuer.Kind != "intermediate" ||
		issuer.Status != "active" || !environmentBootstrapOwnerUUID.MatchString(issuer.ID) {
		return environmentCreateBootstrapBlocker("initial Service CRL requires the original active intermediate and creation ownership")
	}
	version, matched := strings.CutPrefix(issuer.Reference, "pki-issuers/service/"+issuer.ID+"/v")
	number, err := strconv.ParseUint(version, 10, 32)
	if !matched || err != nil || number == 0 || strconv.FormatUint(number, 10) != version {
		return environmentCreateBootstrapBlocker("initial Service CRL requires the exact original signer mount")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if _, err := readProductCurrentCRL(ctx, runtime.PKIClient, issuer); err == nil {
		return nil
	} else {
		var failure *environmentCreatePKIHTTPFailure
		if !errors.As(err, &failure) || failure.Status != http.StatusServiceUnavailable {
			return err
		}
	}
	tokenPath, err := runtime.Execution.Store.safePath("openbao/root-token")
	if err != nil || tokenPath != runtime.OpenBao.RootTokenPath {
		return environmentCreateBootstrapBlocker("initial Service CRL requires the selected OpenBao bootstrap authority")
	}
	token, err := readBoundedEnvironmentFile(tokenPath, true)
	if err != nil {
		return err
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'),
		"IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -field=certificate "+issuer.Reference+"/cert/crl\n")
	if err != nil {
		return err
	}
	block, rest := pem.Decode(raw)
	cert, certErr := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 || certErr != nil {
		return environmentCreateBootstrapBlocker("original Service signer did not return its signed CRL")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	now := time.Now().UTC()
	if err != nil || crl.CheckSignatureFrom(cert) != nil || crl.ThisUpdate.After(now) || !crl.NextUpdate.After(now) {
		return environmentCreateBootstrapBlocker("original Service signer's CRL signature or validity differs")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	path := "/v1/platform/pki/issuers/" + issuer.ID + "/crl"
	publishErr := runtime.PKIClient.call(ctx, http.MethodPost, path, "", map[string]string{"crl_pem": string(raw)}, nil)
	current, err := readProductCurrentCRL(ctx, runtime.PKIClient, issuer)
	if err != nil {
		return errors.Join(publishErr, err)
	}
	observed, _ := pem.Decode([]byte(current))
	if observed == nil || !bytes.Equal(observed.Bytes, crl.Raw) {
		return environmentCreateBootstrapBlocker("registry readback differs from the original Service signer's CRL")
	}
	return nil
}
