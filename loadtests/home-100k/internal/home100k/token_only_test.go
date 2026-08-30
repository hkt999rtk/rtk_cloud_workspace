package home100k

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestExecuteTokenOnlyWritesLatencyReport(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" {
			t.Fatalf("path = %s, want /request_token", r.URL.Path)
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","access_token":"abc.def","scope":"device"}`))
	}))
	defer server.Close()

	outDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"token-only",
		"--base-url", server.URL,
		"--out-dir", outDir,
		"--requests", "6",
		"--concurrency", "3",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(token-only) code = %d stderr=%s", code, stderr.String())
	}
	if got := requests.Load(); got != 6 {
		t.Fatalf("requests = %d, want 6", got)
	}
	var report TokenOnlyReport
	if err := readJSONFile(filepath.Join(outDir, "token-only-results.json"), &report); err != nil {
		t.Fatalf("read token-only report: %v", err)
	}
	if report.Requests != 6 || report.Concurrency != 3 || report.Success != 6 || report.Timeouts != 0 {
		raw, _ := json.Marshal(report)
		t.Fatalf("unexpected report: %s", raw)
	}
	if report.Latency.P50MS <= 0 || report.Latency.P95MS <= 0 || report.Latency.MaxMS <= 0 {
		t.Fatalf("latency summary not populated: %+v", report.Latency)
	}
}

func TestTokenOnlyHTTPClientScalesIdlePoolWithConcurrency(t *testing.T) {
	client, err := tokenOnlyHTTPClient(TokenOnlyOptions{Concurrency: 1000})
	if err != nil {
		t.Fatalf("tokenOnlyHTTPClient() error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.MaxIdleConns < 2000 || transport.MaxIdleConnsPerHost < 2000 {
		t.Fatalf("idle pool too small: MaxIdleConns=%d MaxIdleConnsPerHost=%d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
}

func TestClassifyTokenOnlyErrorRecognizesLocalAddressExhaustion(t *testing.T) {
	err := errors.New(`Post "https://device.example/request_token": dial tcp 192.0.2.10:443: connect: can't assign requested address`)
	if got := classifyTokenOnlyError(err, false); got != "local_addr_exhausted" {
		t.Fatalf("classifyTokenOnlyError() = %q, want local_addr_exhausted", got)
	}
}

func TestExecuteSeedTokenProjectionsWritesDeviceAndEntitlementKeys(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 4)
	go func() {
		for i := 0; i < 4; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			reader := bufio.NewReader(conn)
			var command bytes.Buffer
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					break
				}
				command.WriteString(line)
				if line == "60000\r\n" {
					break
				}
			}
			commands <- command.String()
			_, _ = conn.Write([]byte("+OK\r\n"))
			_ = conn.Close()
		}
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"seed-token-projections",
		"--redis-addr", listener.Addr().String(),
		"--devices", "2",
		"--device-prefix", "load-device-",
		"--ttl", "1m",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(seed-token-projections) code = %d stderr=%s", code, stderr.String())
	}
	seen := ""
	for i := 0; i < 4; i++ {
		seen += <-commands
	}
	for _, want := range []string{
		"video_cloud:device:load-device-000000",
		"video_cloud:entitlement:load-device-000000",
		"video_cloud:device:load-device-000001",
		"video_cloud:entitlement:load-device-000001",
	} {
		if !bytes.Contains([]byte(seen), []byte(want)) {
			t.Fatalf("seed commands missing %q:\n%s", want, seen)
		}
	}
	if !bytes.Contains(stdout.Bytes(), []byte(fmt.Sprintf("seeded token projections devices=%d", 2))) {
		t.Fatalf("stdout missing seed summary: %s", stdout.String())
	}
}
