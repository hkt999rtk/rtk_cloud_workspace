package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDashboard struct {
	detailCalls   int
	activityCalls int
	webhookCalls  int
}

func (f *fakeDashboard) recordClientActivity()  { f.activityCalls++ }
func (f *fakeDashboard) triggerWebhookRefresh() { f.webhookCalls++ }
func (f *fakeDashboard) snapshot() Snapshot     { return Snapshot{} }
func (f *fakeDashboard) detail(_ context.Context, _, _ string, _ int64) (RunDetail, error) {
	f.detailCalls++
	return RunDetail{Card: Card{RunID: 1}}, nil
}

func TestSnapshotRecordsClientActivity(t *testing.T) {
	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	recorder := httptest.NewRecorder()
	newHandler(fake, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || fake.activityCalls != 1 {
		t.Fatalf("snapshot activity: status=%d calls=%d", recorder.Code, fake.activityCalls)
	}
}

func TestHandlerServesAssetsAndJSON(t *testing.T) {
	fake := &fakeDashboard{}
	server := httptest.NewServer(newHandler(fake, nil))
	defer server.Close()
	for _, test := range []struct {
		path, contentType string
	}{
		{"/", "text/html"},
		{"/styles.css", "text/css"},
		{"/api/snapshot", "application/json"},
		{"/healthz", "text/plain"},
	} {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), test.contentType) {
			t.Errorf("GET %s: status=%d type=%q", test.path, response.StatusCode, response.Header.Get("Content-Type"))
		}
	}
}

func TestRepositoryFleetIsCollapsedToggle(t *testing.T) {
	server := httptest.NewServer(newHandler(&fakeDashboard{}, nil))
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, `<details class="fleet-panel">`) || !strings.Contains(html, `<summary class="fleet-toggle">`) {
		t.Fatal("repository fleet is not rendered as a collapsed details toggle")
	}
	if !strings.Contains(html, `id="open-prs"`) || !strings.Contains(html, `id="pr-card-template"`) || !strings.Contains(html, "OPEN PRS") {
		t.Fatal("open PR lane is not rendered")
	}
}

func TestLandscapeTabletShowsThreePrimaryLanes(t *testing.T) {
	server := httptest.NewServer(newHandler(&fakeDashboard{}, nil))
	defer server.Close()

	response, err := http.Get(server.URL + "/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	css := string(body)
	if !strings.Contains(css, "(min-width: 768px) and (max-width: 1199px) and (orientation: landscape)") ||
		!strings.Contains(css, ".board { grid-template-columns: repeat(3, minmax(0, 1fr)); }") {
		t.Fatal("landscape tablet layout does not preserve three primary lanes")
	}
}

func TestHandlerRejectsUnsafeRunPathAndDoesNotLeakToken(t *testing.T) {
	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodGet, "/api/runs/bad%20owner/repo/1", nil)
	recorder := httptest.NewRecorder()
	newHandler(fake, nil).ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusBadRequest || fake.detailCalls != 0 {
		t.Fatalf("unsafe path was accepted: %d %s", recorder.Code, body)
	}
	if strings.Contains(string(body), "GITHUB_TOKEN") || strings.Contains(string(body), "secret-token") {
		t.Fatal("response leaked credential material")
	}
}

func TestGitHubWebhookValidatesSignatureAndTriggersRefresh(t *testing.T) {
	const secret = "test-webhook-secret"
	body := []byte(`{"action":"completed"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	request.Header.Set("X-GitHub-Event", "workflow_run")
	request.Header.Set("X-Hub-Signature-256", signature)
	recorder := httptest.NewRecorder()
	newHandler(fake, []byte(secret)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || fake.webhookCalls != 1 {
		t.Fatalf("valid webhook: status=%d refreshes=%d", recorder.Code, fake.webhookCalls)
	}
}

func TestGitHubWebhookRejectsInvalidSignature(t *testing.T) {
	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`{"action":"opened"}`))
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-Hub-Signature-256", "sha256=00")
	recorder := httptest.NewRecorder()
	newHandler(fake, []byte("secret")).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || fake.webhookCalls != 0 {
		t.Fatalf("invalid webhook: status=%d refreshes=%d", recorder.Code, fake.webhookCalls)
	}
}
