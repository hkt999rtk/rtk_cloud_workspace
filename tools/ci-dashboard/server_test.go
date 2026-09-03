package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDashboard struct {
	detailCalls   int
	activityCalls int
}

func (f *fakeDashboard) recordClientActivity() { f.activityCalls++ }
func (f *fakeDashboard) snapshot() Snapshot    { return Snapshot{} }
func (f *fakeDashboard) detail(_ context.Context, _, _ string, _ int64) (RunDetail, error) {
	f.detailCalls++
	return RunDetail{Card: Card{RunID: 1}}, nil
}

func TestSnapshotRecordsClientActivity(t *testing.T) {
	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	recorder := httptest.NewRecorder()
	newHandler(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || fake.activityCalls != 1 {
		t.Fatalf("snapshot activity: status=%d calls=%d", recorder.Code, fake.activityCalls)
	}
}

func TestHandlerServesAssetsAndJSON(t *testing.T) {
	fake := &fakeDashboard{}
	server := httptest.NewServer(newHandler(fake))
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
	server := httptest.NewServer(newHandler(&fakeDashboard{}))
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
}

func TestHandlerRejectsUnsafeRunPathAndDoesNotLeakToken(t *testing.T) {
	fake := &fakeDashboard{}
	request := httptest.NewRequest(http.MethodGet, "/api/runs/bad%20owner/repo/1", nil)
	recorder := httptest.NewRecorder()
	newHandler(fake).ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusBadRequest || fake.detailCalls != 0 {
		t.Fatalf("unsafe path was accepted: %d %s", recorder.Code, body)
	}
	if strings.Contains(string(body), "GITHUB_TOKEN") || strings.Contains(string(body), "secret-token") {
		t.Fatal("response leaked credential material")
	}
}
