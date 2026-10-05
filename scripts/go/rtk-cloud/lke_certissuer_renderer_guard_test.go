package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func certIssuerRendererGuardDeployment(t *testing.T, containers string) certIssuerIngressObject {
	t.Helper()
	var deployment certIssuerIngressObject
	if err := json.Unmarshal([]byte(`{"spec":{"template":{"spec":{"containers":`+containers+`}}},"status":{"availableReplicas":0}}`), &deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestCertIssuerLegacyRendererGuardPreservesManagedIdentityEvenWhenDown(t *testing.T) {
	for _, container := range []string{
		`{"name":"certissuer","env":[{"name":"CERT_ISSUER_HOST_IDENTITY_STATE","value":"/private/identity/state.json"}]}`,
		`{"name":"certissuer","env":[{"name":"CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED","value":"true"}]}`,
		`{"name":"certissuer","env":[{"name":"CERT_ISSUER_HOST_PKI_ENABLED","value":"1"}]}`,
		`{"name":"certissuer","env":[{"name":"CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED","valueFrom":{"configMapKeyRef":{"name":"managed-policy","key":"enabled"}}}]}`,
		`{"name":"certissuer","env":[{"name":"CERT_ISSUER_HOST_IDENTITY_STATE","valueFrom":{"configMapKeyRef":{"name":"managed-policy","key":"state"}}}]}`,
		`{"name":"pkimanagement","image":"reviewed-image"}`,
		`{"name":"renamed-sidecar","command":["/app/pkimanagement"]}`,
		`{"name":"identity-init","command":["/app/serviceidentity-bootstrap"]}`,
	} {
		t.Run(container, func(t *testing.T) {
			deployment := certIssuerRendererGuardDeployment(t, "["+container+"]")
			err := lkeValidateCertIssuerLegacyRenderer(deployment)
			if err == nil || !strings.Contains(err.Error(), "--workloads") || !strings.Contains(err.Error(), "--dns") || strings.Contains(err.Error(), "/private/identity/state.json") {
				t.Fatalf("managed guard missing actionable sanitized error: %v", err)
			}
		})
	}
	deployment := certIssuerRendererGuardDeployment(t, `[{"name":"certissuer"}]`)
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	pod["initContainers"] = []any{map[string]any{"name": "identity-init", "command": []any{"/app/serviceidentity-bootstrap"}}}
	if err := lkeValidateCertIssuerLegacyRenderer(deployment); err == nil {
		t.Fatal("managed init container was ignored")
	}
}

func TestCertIssuerLegacyRendererGuardAllowsAbsentAndDefaultStaticDeployment(t *testing.T) {
	if err := lkeValidateCertIssuerLegacyRenderer(nil); err != nil {
		t.Fatal(err)
	}
	deployment := certIssuerRendererGuardDeployment(t, `[{"name":"certissuer","command":["/app/certissuer"],"env":[{"name":"CERT_ISSUER_SERVER_CERT","value":"/etc/certissuer/tls.crt"},{"name":"CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED","value":"false"},{"name":"CERT_ISSUER_HOST_PKI_ENABLED","value":"0"}]}]`)
	if err := lkeValidateCertIssuerLegacyRenderer(deployment); err != nil {
		t.Fatal(err)
	}
	if err := lkeValidateCertIssuerLegacyRenderer(certIssuerIngressObject{}); err == nil {
		t.Fatal("malformed existing deployment was treated as absent")
	}
}

func TestCertIssuerRendererCompatibilityReadsOnlySelectedDeployment(t *testing.T) {
	root := t.TempDir()
	kubectl, calls := filepath.Join(root, "kubectl"), filepath.Join(root, "calls")
	if err := os.WriteFile(kubectl, []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$CERTISSUER_GUARD_CALLS"
printf '%s' '{"spec":{"template":{"spec":{"containers":[{"name":"certissuer","env":[{"name":"CERT_ISSUER_HOST_IDENTITY_STATE","value":"/state/managed.json"}]}]}}},"status":{"availableReplicas":0}}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("CERTISSUER_GUARD_CALLS", calls)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	err := lkeRequireCertIssuerRendererCompatibility(map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
	if err == nil {
		t.Fatal("live managed Deployment was allowed into static reconciliation")
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 1 || !strings.Contains(string(data), "-n video-cloud-dev-video-cloud get deployment certissuer --ignore-not-found=true -o json") {
		t.Fatalf("unexpected guard operation: %s", data)
	}
}

func TestCertIssuerRendererCompatibilityContextCancelsRead(t *testing.T) {
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	calls := deploymentRuntimeTestKubectl(t, "exec sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- lkeRequireCertIssuerRendererCompatibilityContext(ctx, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
	}()
	readyBy := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(calls); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("guard finished before blocking read: %v", err)
		default:
		}
		if time.Now().After(readyBy) {
			t.Fatal("guard did not start read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "CHECK_CANCELLED") || time.Since(started) > 2*time.Second || deploymentRuntimeCallCount(t, calls) != 1 {
			t.Fatalf("renderer guard ignored cancellation: err=%v duration=%s", err, time.Since(started))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renderer guard did not cancel its blocked read")
	}
}
