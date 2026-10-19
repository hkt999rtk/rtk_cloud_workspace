package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func providerTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d fixture", status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func providerTestChecker(round func(*http.Request) (*http.Response, error)) deploymentCredentialChecker {
	return deploymentCredentialChecker{client: &http.Client{Transport: rolloutRoundTrip(round)}, linodeAPIRoot: "https://fixture.test/v4", ghcrTokenRoot: "https://fixture.test/token", ghcrRegistryRoot: "https://fixture.test", goDaddyAPIRoot: "https://fixture.test"}
}
func providerTestDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestDeploymentProviderReadOnlyDNSAuthenticates(t *testing.T) {
	for _, status := range []int{200, 401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.Header.Get("Authorization") != "sso-key key:secret" {
					t.Errorf("wrong auth probe %s", req.Method)
				}
				return providerTestResponse(status, "[]"), nil
			})
			checker.readOnly = true
			result := checker.checkGoDaddy(testDeploymentCredentialConfig(), map[string]string{"GODADDY_KEY": "key", "GODADDY_SECRET": "secret"})
			if calls != 1 || result.Passed != (status == 200) {
				t.Fatalf("calls=%d result=%+v", calls, result)
			}
		})
	}
}

func TestDeploymentProviderRetriesOnlyTransientReads(t *testing.T) {
	for _, tc := range []struct {
		method       string
		status, want int
	}{{"GET", 503, 2}, {"HEAD", 429, 2}, {"GET", 401, 1}, {"GET", 403, 1}, {"GET", 404, 1}, {"PUT", 503, 1}, {"DELETE", 503, 1}} {
		t.Run(fmt.Sprintf("%s-%d", tc.method, tc.status), func(t *testing.T) {
			calls := 0
			checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
				calls++
				return providerTestResponse(tc.status, "private-provider-text"), nil
			}).withCheckContext(context.Background())
			req, _ := http.NewRequest(tc.method, "https://fixture.test/resource", nil)
			_, err := checker.request(req)
			if err == nil || calls != tc.want || strings.Contains(err.Error(), "private") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDeploymentProviderTimeoutCancelsTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}).withCheckContext(ctx)
	req, _ := http.NewRequest("GET", "https://fixture.test/resource?token=hidden", nil)
	start := time.Now()
	_, err := checker.request(req)
	if err == nil || !strings.Contains(err.Error(), "TIMEOUT") || strings.Contains(err.Error(), "hidden") || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestDeploymentProviderPlansImageChecksBeforeBoundedExecution(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var active, maximum atomic.Int32
	var mu sync.Mutex
	pending, planned, running := 0, 0, false
	release := make(chan struct{})
	var released sync.Once
	config := `{"os":"linux","architecture":"amd64"}`
	configDigest := providerTestDigest(config)
	manifest := fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest)
	var images []string
	for index := 0; index < environmentDeploymentCheckConcurrency+5; index++ {
		images = append(images, fmt.Sprintf("ghcr.io/owner/repo-%02d@%s", index, providerTestDigest(manifest)))
	}
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for prior := maximum.Load(); n > prior && !maximum.CompareAndSwap(prior, n); prior = maximum.Load() {
		}
		if n == int32(environmentDeploymentCheckConcurrency) {
			released.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		if req.URL.Path == "/token" {
			return providerTestResponse(200, `{"token":"fixture"}`), nil
		}
		if strings.Contains(req.URL.Path, "/manifests/") {
			return providerTestResponse(200, manifest), nil
		}
		return providerTestResponse(200, config), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := checker.collectDeploymentChecks(ctx, testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("ghcr"), images: images, fast: true}, false, func(check deploymentCredentialCheck) {
		mu.Lock()
		defer mu.Unlock()
		if check.Status == "PENDING" {
			if running {
				t.Error("late PENDING after execution")
			}
			pending++
		}
		if check.Status == "RUNNING" {
			if !running {
				planned = pending
			}
			running = true
			if pending != planned || pending <= environmentDeploymentCheckConcurrency {
				t.Errorf("checks were not completely planned before RUNNING: pending=%d planned=%d", pending, planned)
			}
		}
	})
	if len(results) != planned || maximum.Load() != int32(environmentDeploymentCheckConcurrency) || active.Load() != 0 {
		t.Fatalf("results=%d max concurrency=%d", len(results), maximum.Load())
	}
	for _, result := range results {
		if result.Required && result.Status != "PASS" {
			t.Errorf("%+v", result)
		}
	}
}

func TestDeploymentProviderFastImagePlatformAndDedup(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	// No docker executable can be used by fast metadata qualification.
	t.Setenv("PATH", t.TempDir())
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			config := fmt.Sprintf(`{"os":"linux","architecture":%q}`, architecture)
			configDigest := providerTestDigest(config)
			manifest := fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest)
			manifestDigest := providerTestDigest(manifest)
			image := "ghcr.io/owner/repo@" + manifestDigest
			var calls atomic.Int32
			checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.Method != "GET" {
					t.Errorf("unexpected %s", req.Method)
				}
				switch req.URL.Path {
				case "/token":
					return providerTestResponse(200, `{"token":"fixture"}`), nil
				case "/v2/owner/repo/manifests/" + manifestDigest:
					return providerTestResponse(200, manifest), nil
				case "/v2/owner/repo/blobs/" + configDigest:
					return providerTestResponse(200, config), nil
				}
				t.Errorf("unexpected request %s", req.URL.Path)
				return providerTestResponse(404, ""), nil
			})
			results := checker.collectDeploymentChecks(context.Background(), testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("ghcr"), fast: true, images: []string{image, image}}, false, nil)
			if len(results) != 8 || calls.Load() != 3 {
				t.Fatalf("results=%d calls=%d", len(results), calls.Load())
			}
			if results[1].Passed != (architecture == "amd64") || results[2].Status != "SKIPPED" || results[2].Required {
				t.Fatalf("results=%+v", results)
			}
		})
	}
}

func TestDeploymentProviderFastImageIndexDigestBinding(t *testing.T) {
	config := `{"os":"linux","architecture":"amd64"}`
	configDigest := providerTestDigest(config)
	manifest := fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest)
	manifestDigest := providerTestDigest(manifest)
	index := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, manifestDigest)
	indexDigest := providerTestDigest(index)
	bodies := map[string]string{"/token": `{"token":"fixture"}`, "/v2/owner/repo/manifests/" + indexDigest: index, "/v2/owner/repo/manifests/" + manifestDigest: manifest, "/v2/owner/repo/blobs/" + configDigest: config}
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		return providerTestResponse(200, bodies[req.URL.Path]), nil
	})
	credentials := map[string]string{"GHCR_PULL_USERNAME": "user", "GHCR_PULL_TOKEN": "token"}
	image := "ghcr.io/owner/repo@" + indexDigest
	if result := checker.checkRolloutImageMetadata(credentials, image); !result.Passed {
		t.Fatal(result)
	}
	bodies["/v2/owner/repo/manifests/"+manifestDigest] += " "
	if result := checker.checkRolloutImageMetadata(credentials, image); result.Passed || !strings.Contains(result.Detail, "digest") {
		t.Fatal(result)
	}
}

func TestDeploymentProviderFastRejectsMalformedLayerDescriptors(t *testing.T) {
	configDigest := providerTestDigest(`{"os":"linux","architecture":"amd64"}`)
	for _, layers := range []string{"", `,"layers":null`, `,"layers":{}`, `,"layers":[null]`, `,"layers":[{"digest":"invalid"}]`, `,"layers":[{"digest":"` + configDigest + `","size":-1}]`} {
		t.Run(layers, func(t *testing.T) {
			manifest := fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q}%s}`, configDigest, layers)
			digest := providerTestDigest(manifest)
			checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/token" {
					return providerTestResponse(200, `{"token":"fixture"}`), nil
				}
				if req.URL.Path == "/v2/owner/repo/manifests/"+digest {
					return providerTestResponse(200, manifest), nil
				}
				t.Fatalf("unexpected request for malformed manifest: %s", req.URL.Path)
				return nil, nil
			})
			result := checker.checkRolloutImageMetadata(map[string]string{"GHCR_PULL_USERNAME": "fixture", "GHCR_PULL_TOKEN": "fixture"}, "ghcr.io/owner/repo@"+digest)
			if result.Passed || result.Code != "INVALID_IMAGE_METADATA" {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestDeploymentProviderStorageInventoryShared(t *testing.T) {
	var calls atomic.Int32
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return providerTestResponse(200, `{"data":[{"label":"shared","region":"us-test-1","s3_endpoint":"shared.test"}]}`), nil
	})
	checker.session = &deploymentProviderSession{inventory: make(map[string]*deploymentInventoryEntry)}
	checker = checker.withCheckContext(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buckets []linodeStorageBucket
			if err := checker.storageAccountInventory("token", "buckets", &buckets); err != nil || len(buckets) != 1 {
				t.Errorf("buckets=%v err=%v", buckets, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("inventory fetched %d times", calls.Load())
	}
}

func TestDeploymentProviderWriteGateAndRoute53Coverage(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var mutations atomic.Int32
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			mutations.Add(1)
		}
		return providerTestResponse(200, "[]"), nil
	})
	cfg := testDeploymentCredentialConfig()
	results := checker.collectDeploymentChecks(context.Background(), cfg, writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("dns")}, false, nil)
	if len(results) != 8 || results[1].Status != "PASS" || results[2].Status != "BLOCKED" || mutations.Load() != 0 {
		t.Fatalf("results=%+v mutations=%d", results, mutations.Load())
	}
	cfg.DNSAdapter = "route53"
	results = checker.collectDeploymentChecks(context.Background(), cfg, writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("dns"), fast: true}, false, nil)
	if len(results) != 7 || results[1].Code != "UNSUPPORTED" || results[1].Passed {
		t.Fatalf("results=%+v", results)
	}
}

func TestDeploymentProviderCanceledStorageCanaryCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deleted atomic.Bool
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		if req.Method == "PUT" {
			cancel()
			return providerTestResponse(200, ""), nil
		}
		if req.Method == "DELETE" {
			if req.Context().Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			deleted.Store(true)
			return providerTestResponse(204, ""), nil
		}
		return providerTestResponse(200, "<ListBucketResult/>"), nil
	}).withCheckContext(ctx)
	store := provisionObjectStore{bucket: "fixture", endpoint: "https://fixture.test", region: "test", accessKey: "access", secretKey: "secret"}
	err := checker.validateStorageReadWriteCanary(store, "validation")
	if err == nil || !deleted.Load() {
		t.Fatalf("err=%v cleaned=%v", err, deleted.Load())
	}
}

func TestDeploymentProviderLocalChecksNeedNoCredentialProfile(t *testing.T) {
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		t.Fatal("unexpected network request")
		return nil, nil
	})
	results := checker.collectDeploymentChecks(context.Background(), deploymentConfig{}, "absent-profile", deploymentCredentialCheckOptions{selected: keySet("tls"), tls: rolloutTLSOptions{cert: "absent", key: "absent"}}, false, nil)
	if len(results) != 6 || results[0].ID != "local.tls" || results[0].Passed {
		t.Fatalf("results=%+v", results)
	}
}

func TestDeploymentProviderPullContextCancellation(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pullRolloutImageContext(ctx, "dev", "user", "secret-token", "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), "TIMEOUT") || strings.Contains(err.Error(), "secret-token") || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestDeploymentProviderPlanningDoesNotReadProfilesOrNetwork(t *testing.T) {
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		t.Fatal("planning attempted network")
		return nil, nil
	})
	results := checker.collectDeploymentChecks(context.Background(), testDeploymentCredentialConfig(), "missing-profile", deploymentCredentialCheckOptions{planOnly: true}, false, nil)
	if len(results) == 0 {
		t.Fatal("empty plan")
	}
	for _, result := range results {
		if result.Status != "PENDING" {
			t.Fatalf("planning executed %+v", result)
		}
	}
}

func TestDeploymentProviderRepairPreservesLegacyScopes(t *testing.T) {
	checker := deploymentCredentialChecker{}
	for _, resolved := range []bool{false, true} {
		t.Run(fmt.Sprint(resolved), func(t *testing.T) {
			cfg := testDeploymentCredentialConfig()
			if resolved {
				cfg.Storage.RuntimeMedia.Bucket = "runtime"
			}
			results := checker.collectDeploymentChecks(context.Background(), cfg, "missing-profile", deploymentCredentialCheckOptions{planOnly: true, createMissingObjectStorageBucket: true}, true, nil)
			ids := map[string]bool{}
			for _, result := range results {
				ids[result.ID] = true
			}
			if !ids["storage.repair"] || ids["provider.linode"] == resolved || ids["provider.dns.write"] == resolved {
				t.Fatalf("resolved=%v plan=%v", resolved, ids)
			}
			if resolved && len(results) != 2 {
				t.Fatalf("resolved bootstrap gained unrelated checks: %v", ids)
			}
		})
	}
}

func TestDeploymentProviderRetryRecoveryDoesNotMaskInvalidMetadata(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var calls atomic.Int32
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return providerTestResponse(503, ""), nil
		}
		return providerTestResponse(200, "invalid-json"), nil
	})
	results := checker.collectDeploymentChecks(context.Background(), testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("ghcr"), images: []string{"ghcr.io/owner/repo@sha256:" + strings.Repeat("a", 64)}, fast: true}, false, nil)
	if results[1].Code == "PROVIDER_UNAVAILABLE" || results[1].Status != "ERROR" || results[1].Code != "INVALID_RESPONSE" || results[1].Attempts != 2 {
		t.Fatalf("retry recovery misclassified: %+v", results[1])
	}
}

func TestDeploymentProviderRetryAfterHonorsOverallDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if deploymentRetryDelay("3", now) != 3*time.Second || deploymentRetryDelay(now.Add(5*time.Second).Format(http.TimeFormat), now) != 5*time.Second {
		t.Fatal("Retry-After parsing failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		calls++
		response := providerTestResponse(429, "")
		response.Header.Set("Retry-After", "120")
		return response, nil
	}).withCheckContext(ctx)
	req, _ := http.NewRequest("GET", "https://fixture.test/resource", nil)
	start := time.Now()
	_, err := checker.request(req)
	if err == nil || calls != 1 || time.Since(start) > time.Second {
		t.Fatalf("calls=%d err=%v elapsed=%s", calls, err, time.Since(start))
	}
}

func TestDeploymentProviderAmbiguousDNSWriteCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed, deleted := false, false
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case "GET":
			if committed {
				return providerTestResponse(200, `[{"data":"rtk-cloud-credential-preflight","ttl":600}]`), nil
			}
			return providerTestResponse(200, "[]"), nil
		case "PUT":
			committed = true
			cancel()
			return nil, context.DeadlineExceeded
		case "DELETE":
			if req.Context().Err() != nil {
				t.Error("cleanup canceled")
			}
			deleted = true
			return providerTestResponse(204, ""), nil
		}
		t.Errorf("unexpected method %s", req.Method)
		return nil, nil
	}).withCheckContext(ctx)
	result := checker.checkGoDaddy(testDeploymentCredentialConfig(), map[string]string{"GODADDY_KEY": "key", "GODADDY_SECRET": "secret"})
	if result.Passed || !deleted {
		t.Fatalf("result=%+v deleted=%v", result, deleted)
	}
}

type providerFailingBody struct{}

func (providerFailingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (providerFailingBody) Close() error             { return nil }
func TestDeploymentProviderDNSIncompleteBodyNeverPasses(t *testing.T) {
	for _, body := range []io.ReadCloser{providerFailingBody{}, io.NopCloser(strings.NewReader("")), io.NopCloser(strings.NewReader("null")), io.NopCloser(strings.NewReader("[null]")), io.NopCloser(strings.NewReader("[{}]")), io.NopCloser(strings.NewReader(`[{"data":null}]`)), io.NopCloser(strings.NewReader(`[{"data":42}]`))} {
		checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
			response := providerTestResponse(200, "")
			response.Body = body
			return response, nil
		})
		checker.readOnly = true
		result := checker.checkGoDaddy(testDeploymentCredentialConfig(), map[string]string{"GODADDY_KEY": "key", "GODADDY_SECRET": "secret"})
		if result.Passed {
			t.Fatalf("incomplete DNS response passed: %+v", result)
		}
	}
}

func TestDeploymentProviderDNSInvalidEndpointIsRedactedBeforeNetwork(t *testing.T) {
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		t.Fatal("request for an invalid endpoint")
		return nil, nil
	})
	checker.goDaddyAPIRoot, checker.readOnly = "https://fixture.test/private-secret-%zz", true
	result := checker.checkGoDaddy(testDeploymentCredentialConfig(), map[string]string{"GODADDY_KEY": "key", "GODADDY_SECRET": "secret"})
	if result.Passed || strings.Contains(result.Detail, "private-secret") || !strings.Contains(result.Detail, "configured endpoint") {
		t.Fatalf("unsafe endpoint diagnostic: %+v", result)
	}
}

func deploymentProviderPullConcurrencyFixture(t *testing.T) (deploymentCredentialChecker, []string, string, secretStore) {
	t.Helper()
	clearDeploymentCredentialEnvironment(t)
	store := makeIsolatedTestSecretStore(t, "staging")
	dir := t.TempDir()
	slots := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("PULL_TEST_SLOTS", slots)
	script := "#!/bin/sh\nslot=''\nfor candidate in"
	for index := 1; index <= environmentDeploymentCheckConcurrency; index++ {
		script += fmt.Sprintf(" %d", index)
	}
	script += `; do
 if /bin/mkdir "$PULL_TEST_SLOTS/slot-$candidate" 2>/dev/null; then slot=$candidate; break; fi
done
if test -z "$slot"; then : > "$PULL_TEST_SLOTS/overflow"; exit 3; fi
printf '%s\n' "$$" >> "$PULL_TEST_SLOTS/pids"
while ! test -f "$PULL_TEST_SLOTS/release"; do /bin/sleep 0.01; done
/bin/sleep 0.02
/bin/rmdir "$PULL_TEST_SLOTS/slot-$slot"
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/token" {
			return providerTestResponse(200, `{"token":"fixture"}`), nil
		}
		return providerTestResponse(200, ""), nil
	})
	var images []string
	for i := 1; i <= environmentDeploymentCheckConcurrency+5; i++ {
		images = append(images, fmt.Sprintf("ghcr.io/owner/repo@sha256:%064x", i))
	}
	return checker, images, slots, store
}

func awaitDeploymentProviderPullLimit(t *testing.T, slots string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(slots)
		active := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "slot-") {
				active++
			}
		}
		raw, _ := os.ReadFile(filepath.Join(slots, "pids"))
		if active == environmentDeploymentCheckConcurrency && len(strings.Fields(string(raw))) == environmentDeploymentCheckConcurrency {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("full image pull checks did not overlap twelve process slots")
}

func assertDeploymentProviderPullsJoined(t *testing.T, slots string, store secretStore) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(slots, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range strings.Fields(string(raw)) {
		var pid int
		if _, err := fmt.Sscan(text, &pid); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Fatal("provider checks returned while a pull process remained alive", pid, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatal("provider checks returned before private Docker identity cleanup", err, entries)
	}
}

func TestDeploymentProviderFullImagePullConcurrencyTwelve(t *testing.T) {
	checker, images, slots, store := deploymentProviderPullConcurrencyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	envFile := writeDeploymentCredentialEnv(t, "https://fixture.test", "token")
	done := make(chan []deploymentCredentialCheck, 1)
	go func() {
		done <- checker.collectDeploymentChecks(ctx, testDeploymentCredentialConfig(), envFile, deploymentCredentialCheckOptions{selected: keySet("ghcr"), images: images}, true, nil)
	}()
	awaitDeploymentProviderPullLimit(t, slots)
	writeTestFile(t, filepath.Join(slots, "release"), "release")
	results := <-done
	for _, result := range results {
		if result.Required && !result.Passed {
			t.Errorf("%+v", result)
		}
	}
	if _, err := os.Stat(filepath.Join(slots, "overflow")); !os.IsNotExist(err) {
		t.Fatal("more than twelve concurrent pulls")
	}
	raw, _ := os.ReadFile(filepath.Join(slots, "pids"))
	if len(strings.Fields(string(raw))) != len(images) {
		t.Fatal("bounded pulls skipped an image")
	}
	assertDeploymentProviderPullsJoined(t, slots, store)
}

func TestDeploymentProviderFullImagePullCancellationJoinsAndCleans(t *testing.T) {
	checker, images, slots, store := deploymentProviderPullConcurrencyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envFile := writeDeploymentCredentialEnv(t, "https://fixture.test", "token")
	done := make(chan []deploymentCredentialCheck, 1)
	go func() {
		done <- checker.collectDeploymentChecks(ctx, testDeploymentCredentialConfig(), envFile, deploymentCredentialCheckOptions{selected: keySet("ghcr"), images: images}, true, nil)
	}()
	awaitDeploymentProviderPullLimit(t, slots)
	cancel()
	select {
	case results := <-done:
		canceled := 0
		for _, result := range results {
			if result.Code == "CANCELED" {
				canceled++
			}
		}
		if canceled < len(images) {
			t.Fatal("image tasks did not preserve cancellation", results)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled provider checks left image pull processes running")
	}
	if _, err := os.Stat(filepath.Join(slots, "overflow")); !os.IsNotExist(err) {
		t.Fatal("cancelled pulls exceeded twelve slots")
	}
	assertDeploymentProviderPullsJoined(t, slots, store)
}

func TestDeploymentProviderCleanupFailureIsReported(t *testing.T) {
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		if req.Method == "DELETE" {
			return providerTestResponse(403, "private-error"), nil
		}
		if req.Method == "GET" && strings.Contains(req.URL.Path, "__rtk_cloud_validation__") {
			return providerTestResponse(200, "wrong-canary"), nil
		}
		return providerTestResponse(200, "<ListBucketResult/>"), nil
	}).withCheckContext(context.Background())
	err := checker.validateStorageReadWriteCanary(provisionObjectStore{bucket: "test", endpoint: "https://fixture.test", region: "test", accessKey: "access", secretKey: "secret"}, "test")
	if err == nil || !strings.Contains(err.Error(), "content mismatch") || !strings.Contains(err.Error(), "cleanup failed") || strings.Contains(err.Error(), "private-error") {
		t.Fatalf("err=%v", err)
	}
}

func TestDeploymentProviderCanceledWriteRetainsSeparateCleanupFailure(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempted atomic.Bool
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			if attempted.Load() {
				// The ambiguous PUT reached the provider. Guarded cleanup must
				// see our exact value before attempting its DELETE.
				return providerTestResponse(200, `[{"data":"rtk-cloud-credential-preflight","ttl":600}]`), nil
			}
			return providerTestResponse(200, "[]"), nil
		case http.MethodPut:
			attempted.Store(true)
			cancel()
			return nil, context.Canceled
		case http.MethodDelete:
			if req.Context().Err() != nil {
				t.Error("cleanup inherited canceled context")
			}
			return providerTestResponse(403, "private-error-marker"), nil
		}
		t.Fatalf("unexpected request %s", req.Method)
		return nil, nil
	})
	results := checker.collectDeploymentChecks(ctx, testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("dns")}, true, nil)
	seen := map[string]deploymentCredentialCheck{}
	for _, result := range results {
		seen[result.ID] = result
	}
	if c := seen["provider.dns.write"]; c.Status != "ERROR" || !strings.Contains(c.Detail, "cleanup failed") {
		t.Fatalf("lost mutation failure: %+v", c)
	}
	if c := seen["provider.dns.write.cleanup"]; c.Status != "ERROR" || c.Code != "CANARY_CLEANUP_FAILED" || !c.Required || strings.Contains(c.Detail, "private-error-marker") {
		t.Fatalf("lost or unsafe cleanup evidence: %+v", c)
	}
}

func TestDeploymentProviderUnconfiguredDefaultsAreExplicit(t *testing.T) {
	checker := deploymentCredentialChecker{}
	results := checker.collectDeploymentChecks(context.Background(), deploymentConfig{}, "absent", deploymentCredentialCheckOptions{planOnly: true}, false, nil)
	ids := map[string]bool{}
	for _, result := range results {
		if ids[result.ID] {
			t.Fatalf("duplicate id %s", result.ID)
		}
		ids[result.ID] = true
	}
	for _, id := range []string{"provider.linode", "provider.ghcr", "provider.dns", "provider.storage", "local.tls", "local.mounts"} {
		if !ids[id] {
			t.Errorf("missing coverage %s", id)
		}
	}
}

func TestDeploymentProviderLateSuccessAfterCancellationIsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) { cancel(); return providerTestResponse(200, "[]"), nil })
	results := checker.collectDeploymentChecks(ctx, testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, "https://fixture.test", "token"), deploymentCredentialCheckOptions{selected: keySet("dns"), readOnly: true}, false, nil)
	if results[1].Passed || results[1].Status != "ERROR" || results[1].Code != "CANCELED" {
		t.Fatalf("canceled success accepted: %+v", results[1])
	}
}

func TestDeploymentProviderParallelFixtureHalvesSerialTime(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	checker := providerTestChecker(func(req *http.Request) (*http.Response, error) {
		time.Sleep(40 * time.Millisecond)
		if req.URL.Path == "/token" {
			return providerTestResponse(200, `{"token":"fixture"}`), nil
		}
		return providerTestResponse(200, `{"tags":["main"]}`), nil
	})
	profile := writeDeploymentCredentialEnv(t, "https://fixture.test", "token")
	values, check := deploymentCredentialProfileValues("staging", profile, "")
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	start := time.Now()
	serial := checker.checkGHCR(values)
	serialTime := time.Since(start)
	start = time.Now()
	parallel := checker.collectDeploymentChecks(context.Background(), testDeploymentCredentialConfig(), profile, deploymentCredentialCheckOptions{selected: keySet("ghcr"), fast: true}, false, nil)
	parallelTime := time.Since(start)
	passed := map[string]bool{}
	for _, result := range parallel {
		if strings.HasPrefix(result.ID, "provider.ghcr.") {
			passed[result.Name] = result.Passed
		}
	}
	for _, result := range serial {
		if !result.Passed || !passed[result.Name] {
			t.Fatalf("fixture coverage differs for %s", result.Name)
		}
	}
	t.Logf("six repositories, two 40ms reads each: serial=%s parallel=%s reduction=%.1f%%", serialTime, parallelTime, 100*(1-float64(parallelTime)/float64(serialTime)))
	if len(serial) != 6 || len(passed) != 6 || parallelTime > serialTime/2 {
		t.Fatalf("parallel fixture exceeded 50%% of serial: %s vs %s", parallelTime, serialTime)
	}
}

func TestDeploymentProviderPullCancellationKillsDescendants(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	orphan := filepath.Join(dir, "orphan")
	t.Setenv("PATH", dir)
	t.Setenv("PULL_CHILD_READY", ready)
	t.Setenv("PULL_CHILD_ORPHAN", orphan)
	script := `#!/bin/sh
(printf ready > "$PULL_CHILD_READY"; /bin/sleep 0.3; printf orphan > "$PULL_CHILD_ORPHAN") &
wait
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- pullRolloutImageContext(ctx, "dev", "user", "token", "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64))
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled pull passed")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled pull retained child process")
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("Docker child survived process-group cancellation")
	}
}
