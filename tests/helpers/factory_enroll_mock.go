package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

type requestLog struct {
	OK                     bool     `json:"ok"`
	Devid                  string   `json:"devid"`
	ServiceOptions         []string `json:"service_options"`
	MetadataServiceOptions []string `json:"metadata_service_options"`
}

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: factory_enroll_mock LOG PORT_FILE CA_CERT CA_KEY")
		os.Exit(2)
	}
	logPath, portPath, caCert, caKey := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(portPath, []byte(fmt.Sprintf("%d", port)), 0o644); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/factory/enroll", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestID := r.Header.Get("X-Video-Cloud-Request-ID")
		timestamp := r.Header.Get("X-Video-Cloud-Timestamp")
		hash := sha256.Sum256(body)
		canonical := bytes.Join([][]byte{
			[]byte("POST"),
			[]byte(r.URL.Path),
			[]byte(timestamp),
			[]byte(requestID),
			[]byte(hex.EncodeToString(hash[:])),
		}, []byte("\n"))
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write(canonical)
		expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		serviceOptions, _ := stringSlice(payload["service_options"])
		metadataServiceOptions := []string{}
		if metadata, ok := payload["metadata"].(map[string]any); ok {
			metadataServiceOptions, _ = stringSlice(metadata["service_options"])
		}
		ok := r.Header.Get("X-Video-Cloud-Signature") == expected &&
			payload["allowed_services"] == nil &&
			len(serviceOptions) > 0 &&
			equalStrings(serviceOptions, metadataServiceOptions)
		appendLog(logPath, requestLog{
			OK:                     ok,
			Devid:                  stringValue(payload["devid"]),
			ServiceOptions:         serviceOptions,
			MetadataServiceOptions: metadataServiceOptions,
		})
		if !ok {
			http.Error(w, `{"error":"bad factory enroll request"}`, http.StatusBadRequest)
			return
		}
		certPEM, err := signCSR(stringValue(payload["csr_pem"]), caCert, caKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		chain, _ := os.ReadFile(caCert)
		response := map[string]any{
			"request_id":            requestID,
			"device_id":             payload["devid"],
			"serial_number":         payload["serial_number"],
			"certificate_pem":       certPEM,
			"certificate_chain_pem": string(chain),
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	panic(http.Serve(listener, mux))
}

func stringSlice(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func appendLog(path string, entry requestLog) {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}
	defer fh.Close()
	data, _ := json.Marshal(entry)
	_, _ = fh.Write(append(data, '\n'))
}

func signCSR(csrPEM, caCert, caKey string) (string, error) {
	tmp, err := os.MkdirTemp("", "factory-enroll-cert-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	csrPath := filepath.Join(tmp, "device.csr.pem")
	certPath := filepath.Join(tmp, "device.cert.pem")
	if err := os.WriteFile(csrPath, []byte(csrPEM), 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("openssl", "x509", "-req", "-sha256", "-in", csrPath, "-CA", caCert, "-CAkey", caKey, "-CAcreateserial", "-days", "30", "-out", certPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("openssl sign failed: %v: %s", err, out)
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
