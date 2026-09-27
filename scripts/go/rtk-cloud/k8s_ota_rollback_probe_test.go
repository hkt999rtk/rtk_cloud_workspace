package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const otaRollbackCoreDeploymentJSON = `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED","value":"true"}]}]}}}}`

const otaRollbackReadyPodsJSON = `{"items":[{"metadata":{"name":"ingress-nginx-controller-a","uid":"uid-a"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"ingress-nginx-controller-b","uid":"uid-b"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`

const otaRollbackCoreIngressJSON = `{"metadata":{"annotations":{"nginx.ingress.kubernetes.io/auth-tls-verify-client":"on"}},"spec":{"rules":[{"host":"device.example.test","http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"public-video-cloud-api-video-cloud","port":{"number":80}}}}]}}]}}`

func otaRollbackTestTLS(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string, tls.Certificate, *x509.CertPool) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	server.StartTLS()
	t.Cleanup(server.Close)
	certificate := server.TLS.Certificates[0]
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1"
	if len(leaf.DNSNames) > 0 {
		host = leaf.DNSNames[0]
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return server, host, certificate, roots
}

func otaRollbackWriteTestIdentity(t *testing.T, identity tls.Certificate) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "device.crt")
	keyFile := filepath.Join(dir, "device.key")
	keyDER, err := x509.MarshalPKCS8PrivateKey(identity.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := []byte{}
	for _, der := range identity.Certificate {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestLKEOTARollbackProbeRequiresMTLSCoreSentinel(t *testing.T) {
	var expectedHost string
	server, host, identity, roots := otaRollbackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/device/ota/rollback-probe" || r.URL.Query().Get("nonce") == "" || r.Host != expectedHost || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "bad probe", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"fail","code":"OTA_DIRECT_ROUTE_REQUIRED","reason":"OTA device route requires direct mTLS edge routing"}`))
	})
	expectedHost = host
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	if err := lkeProbeOTARollbackHTTPS(host, port, identity, roots); err != nil {
		t.Fatalf("valid per-pod mTLS core route failed: %v", err)
	}
	if err := lkeProbeOTARollbackWithoutCertificate(host, port, roots); err != nil {
		t.Fatalf("probe did not recognize the expected no-certificate denial: %v", err)
	}
	if err := lkeProbeOTARollbackHTTPS(host, port, tls.Certificate{}, roots); err == nil {
		t.Fatal("probe accepted a route without device mTLS")
	}
	if err := lkeProbeOTARollbackHTTPS(host, port, identity, x509.NewCertPool()); err == nil {
		t.Fatal("probe accepted an untrusted server certificate")
	}
	if err := lkeProbeOTARollbackHTTPS("wrong.example.test", port, identity, roots); err == nil {
		t.Fatal("probe accepted a different TLS hostname")
	}
}

func TestLKEOTARollbackProbeRejectsWrongRouteResponse(t *testing.T) {
	responseBody := `{"status":"fail","code":"OTA_UNAVAILABLE"}`
	server, host, identity, roots := otaRollbackTestTLS(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(responseBody))
	})
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	if err := lkeProbeOTARollbackHTTPS(host, port, identity, roots); err == nil || !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("probe accepted a dedicated OTA or upstream 503: %v", err)
	}
	responseBody = `{"error":{"code":"OTA_DIRECT_ROUTE_REQUIRED"}}`
	if err := lkeProbeOTARollbackHTTPS(host, port, identity, roots); err == nil || !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("probe accepted a noncanonical nested error: %v", err)
	}
	responseBody = `{"status":"fail","code":"OTA_DIRECT_ROUTE_REQUIRED"}`
	if err := lkeProbeOTARollbackWithoutCertificate(host, port, roots); err == nil || !strings.Contains(err.Error(), "missing client certificate") {
		t.Fatalf("probe accepted a core response without device mTLS: %v", err)
	}
}

func TestLKEOTARollbackDataPlaneGuardsEveryController(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_DEVICE_DOMAIN": "device.example.test"}
	probeCalls := []string{}
	probe := func(_ map[string]string, pod otaIngressControllerPod, host string, _ tls.Certificate, _ *x509.CertPool) error {
		if host != "device.example.test" {
			t.Fatalf("probe used host %q", host)
		}
		probeCalls = append(probeCalls, pod.Name+"/"+pod.UID)
		return nil
	}
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err != nil {
		t.Fatalf("initial deployment required a rollback probe: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED","value":"false"}]}]}}}}`)
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err != nil {
		t.Fatalf("already restored core required a rollback probe: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", otaRollbackCoreDeploymentJSON)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", otaRollbackCoreIngressJSON)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", strings.Replace(otaRollbackCoreIngressJSON, `:"on"`, `:"off"`, 1))
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err == nil || !strings.Contains(err.Error(), "mTLS ingress authentication") {
		t.Fatalf("rollback accepted a core route without device mTLS: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", otaRollbackCoreIngressJSON)
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err == nil || !strings.Contains(err.Error(), "mTLS certificate") {
		t.Fatalf("rollback accepted missing device mTLS identity: %v", err)
	}
	_, _, identity, _ := otaRollbackTestTLS(t, func(http.ResponseWriter, *http.Request) {})
	certFile, keyFile := otaRollbackWriteTestIdentity(t, identity)
	env["LKE_OTA_ROLLBACK_PROBE_CERT_FILE"] = certFile
	env["LKE_OTA_ROLLBACK_PROBE_KEY_FILE"] = keyFile
	t.Setenv("FAKE_OTA_INGRESS_PODS_JSON", otaRollbackReadyPodsJSON)
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err != nil {
		t.Fatalf("ready stable ingress controllers were blocked: %v", err)
	}
	if got := strings.Join(probeCalls, ","); got != "ingress-nginx-controller-a/uid-a,ingress-nginx-controller-b/uid-b" {
		t.Fatalf("not every controller was probed: %s", got)
	}
	dedicated, dedicatedHost, dedicatedIdentity, dedicatedRoots := otaRollbackTestTLS(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"fail","code":"OTA_UNAVAILABLE"}`))
	})
	_, portText, _ := net.SplitHostPort(dedicated.Listener.Addr().String())
	var dedicatedPort int
	_, _ = fmt.Sscanf(portText, "%d", &dedicatedPort)
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, func(_ map[string]string, _ otaIngressControllerPod, _ string, _ tls.Certificate, _ *x509.CertPool) error {
		return lkeProbeOTARollbackHTTPS(dedicatedHost, dedicatedPort, dedicatedIdentity, dedicatedRoots)
	}); err == nil || !strings.Contains(err.Error(), "has not reached core") {
		t.Fatalf("core restoration accepted a dedicated-service 503: %v", err)
	}
	t.Setenv("FAKE_OTA_INGRESS_PODS_JSON", `{"items":[{"metadata":{"name":"ingress-nginx-controller-a","uid":"uid-a"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}]}}]}`)
	if err := lkeRequireOTARollbackDataPlaneWithProbe(env, probe); err == nil || !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("rollback accepted an unready ingress controller: %v", err)
	}
	t.Setenv("FAKE_OTA_INGRESS_PODS_JSON", otaRollbackReadyPodsJSON)
	changed := false
	err := lkeRequireOTARollbackDataPlaneWithProbe(env, func(env map[string]string, pod otaIngressControllerPod, host string, identity tls.Certificate, roots *x509.CertPool) error {
		if !changed {
			changed = true
			t.Setenv("FAKE_OTA_INGRESS_PODS_JSON", strings.ReplaceAll(otaRollbackReadyPodsJSON, "uid-b", "replacement-uid"))
		}
		return probe(env, pod, host, identity, roots)
	})
	if err == nil || !strings.Contains(err.Error(), "changed during rollback probe") {
		t.Fatalf("rollback accepted a changed controller UID set: %v", err)
	}
	t.Setenv("FAKE_OTA_INGRESS_PODS_JSON", otaRollbackReadyPodsJSON)
	err = lkeRequireOTARollbackDataPlaneWithProbe(env, func(env map[string]string, pod otaIngressControllerPod, host string, identity tls.Certificate, roots *x509.CertPool) error {
		t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[{"http":{"paths":[{"path":"/v1/device/ota/"}]}}]}}`)
		return probe(env, pod, host, identity, roots)
	})
	if err == nil || !strings.Contains(err.Error(), "before restoring core") {
		t.Fatalf("rollback accepted an edge route reapplied during the probe: %v", err)
	}
}
