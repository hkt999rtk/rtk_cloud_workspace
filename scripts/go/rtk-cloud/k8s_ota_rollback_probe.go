package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"
)

type otaIngressControllerPod struct {
	Name string
	UID  string
}

type otaRollbackPodProbe func(map[string]string, otaIngressControllerPod, string, tls.Certificate, *x509.CertPool) error

// The Ingress API spec changes before every controller has reloaded it. Core
// may own operator OTA again only after every serving controller returns the
// cut-over core's sentinel response for a device-host OTA request.
func lkeRequireOTARollbackDataPlane(env map[string]string) error {
	return lkeRequireOTARollbackDataPlaneWithProbe(env, lkeProbeOTARollbackPod)
}

func lkeRequireOTARollbackDataPlaneWithProbe(env map[string]string, probe otaRollbackPodProbe) error {
	exists, cutover, err := lkeObservedOTACoreCutover(env)
	if err != nil {
		return err
	}
	if !exists {
		return nil // Initial deployment.
	}
	if !cutover {
		// An already restored core needs no device certificate, but a rollout
		// in progress cannot be treated as a stable initial state.
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/video-cloud-api", "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return fmt.Errorf("core API has not completed OTA handler restoration: %w", err)
		}
		return nil
	}
	if err := lkeRequireObservedOTACoreCutover(env); err != nil {
		return err
	}
	if err := lkeRequireActiveOTACoreDeviceRoute(env); err != nil {
		return err
	}
	identity, roots, err := lkeOTARollbackProbeIdentity(env)
	if err != nil {
		return err
	}
	before, err := lkeReadyOTAIngressControllerPods(env)
	if err != nil {
		return err
	}
	host := firstNonEmpty(lkeEnvValue(env, "LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+env["VIDEO_CLOUD_DOMAIN"])
	if host == "device." || strings.ContainsAny(host, "/:@ 	\r\n") {
		return fmt.Errorf("OTA rollback probe requires a valid device hostname")
	}
	for _, pod := range before {
		if err := probe(env, pod, host, identity, roots); err != nil {
			return fmt.Errorf("OTA rollback device route has not reached core on ingress controller %s: %w", pod.Name, err)
		}
	}
	if err := lkeRequireObservedOTACoreCutover(env); err != nil {
		return err
	}
	after, err := lkeReadyOTAIngressControllerPods(env)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return fmt.Errorf("OTA ingress controller Pods changed during rollback probe; retry before restoring core")
	}
	if err := lkePreventOTAEdgeRollbackOverlap(env); err != nil {
		return err
	}
	return lkeRequireActiveOTACoreDeviceRoute(env)
}

func lkeOTARollbackProbeIdentity(env map[string]string) (tls.Certificate, *x509.CertPool, error) {
	certFile := strings.TrimSpace(lkeEnvValue(env, "LKE_OTA_ROLLBACK_PROBE_CERT_FILE"))
	keyFile := strings.TrimSpace(lkeEnvValue(env, "LKE_OTA_ROLLBACK_PROBE_KEY_FILE"))
	if certFile == "" || keyFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("OTA rollback requires a valid test-device mTLS certificate and private key file")
	}
	info, err := os.Stat(keyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return tls.Certificate{}, nil, fmt.Errorf("OTA rollback probe private key must be a regular file with mode 0600")
	}
	identity, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || len(identity.Certificate) == 0 {
		return tls.Certificate{}, nil, fmt.Errorf("OTA rollback probe certificate and private key are invalid")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return tls.Certificate{}, nil, fmt.Errorf("OTA rollback probe device certificate is invalid or expired")
	}
	rootsFile := strings.TrimSpace(lkeEnvValue(env, "LKE_OTA_ROLLBACK_PROBE_SERVER_CA_FILE"))
	if rootsFile == "" {
		return identity, nil, nil // Use the system server trust store.
	}
	data, err := os.ReadFile(rootsFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read OTA rollback probe server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return tls.Certificate{}, nil, fmt.Errorf("OTA rollback probe server CA is invalid")
	}
	return identity, roots, nil
}

func lkeReadyOTAIngressControllerPods(env map[string]string) ([]otaIngressControllerPod, error) {
	const selector = "app.kubernetes.io/component=controller,app.kubernetes.io/instance=ingress-nginx"
	if err := runKubectl("-n", lkeIngressNamespace(env), "rollout", "status", "deployment/ingress-nginx-controller", "--timeout", firstNonEmpty(os.Getenv("LKE_INGRESS_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return nil, fmt.Errorf("OTA ingress controller rollout is not complete: %w", err)
	}
	body, err := kubectlCombinedOutput(nil, "-n", lkeIngressNamespace(env), "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("inspect OTA ingress controller Pods: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string  `json:"name"`
				UID               string  `json:"uid"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode OTA ingress controller Pods: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("OTA rollback requires Ready ingress controller Pods")
	}
	pods := make([]otaIngressControllerPod, 0, len(list.Items))
	for _, item := range list.Items {
		ready := false
		for _, condition := range item.Status.Conditions {
			ready = ready || condition.Type == "Ready" && condition.Status == "True"
		}
		if item.Metadata.Name == "" || item.Metadata.UID == "" || item.Metadata.DeletionTimestamp != nil || item.Status.Phase != "Running" || !ready {
			return nil, fmt.Errorf("OTA ingress controller Pod %s is absent, terminating, or not Ready", item.Metadata.Name)
		}
		pods = append(pods, otaIngressControllerPod{Name: item.Metadata.Name, UID: item.Metadata.UID})
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func lkeProbeOTARollbackPod(env map[string]string, pod otaIngressControllerPod, host string, identity tls.Certificate, roots *x509.CertPool) error {
	port, cleanup, err := lkeOTARollbackPodPortForward(env, pod.Name)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := lkeProbeOTARollbackHTTPS(host, port, identity, roots); err != nil {
		return err
	}
	return lkeProbeOTARollbackWithoutCertificate(host, port, roots)
}

func lkeOTARollbackPodPortForward(env map[string]string, podName string) (int, func(), error) {
	port, err := freeLocalPort()
	if err != nil {
		return 0, nil, err
	}
	args := lkeKubectlArgs("-n", lkeIngressNamespace(env), "port-forward", "pod/"+podName, fmt.Sprintf("%d:443", port))
	cmd := exec.Command(lkeKubectl(), args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("start OTA ingress controller port-forward: %w", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	if err := waitForLocalTCPPort(port, 10*time.Second); err != nil {
		cleanup()
		return 0, nil, fmt.Errorf("OTA ingress controller port-forward did not become ready: %w", err)
	}
	return port, cleanup, nil
}

func lkeProbeOTARollbackHTTPS(host string, port int, identity tls.Certificate, roots *x509.CertPool) error {
	status, body, err := lkeOTARollbackGET(host, port, []tls.Certificate{identity}, roots)
	if err != nil {
		return fmt.Errorf("mTLS device-host route probe failed: %w", err)
	}
	var result struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	if status != http.StatusServiceUnavailable || json.Unmarshal(body, &result) != nil || result.Status != "fail" || result.Code != "OTA_DIRECT_ROUTE_REQUIRED" {
		return fmt.Errorf("device-host route did not return the cut-over core sentinel (HTTP %d)", status)
	}
	return nil
}

func lkeProbeOTARollbackWithoutCertificate(host string, port int, roots *x509.CertPool) error {
	status, _, err := lkeOTARollbackGET(host, port, nil, roots)
	if err != nil {
		return fmt.Errorf("device-host request without a client certificate failed inconclusively: %w", err)
	}
	if status != http.StatusBadRequest {
		return fmt.Errorf("device-host route did not reject a missing client certificate with HTTP 400 (got %d)", status)
	}
	return nil
}

func lkeOTARollbackGET(host string, port int, certificates []tls.Certificate, roots *x509.CertPool) (int, []byte, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, nil, fmt.Errorf("create OTA rollback probe nonce: %w", err)
	}
	probeURL := &url.URL{Scheme: "https", Host: host, Path: "/v1/device/ota/rollback-probe", RawQuery: "nonce=" + hex.EncodeToString(nonce[:])}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Cache-Control", "no-store")
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		},
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: roots, Certificates: certificates},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("OTA rollback probe redirected") }}
	response, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, body, nil
}
