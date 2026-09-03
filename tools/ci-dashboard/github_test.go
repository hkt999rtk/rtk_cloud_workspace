package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshRepoEnrichesRunAndUsesETag(t *testing.T) {
	var runRequests atomic.Int32
	var jobsRequests atomic.Int32
	var unexpectedETag atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4990")
		switch r.URL.Path {
		case "/repos/hkt999rtk/repo/actions/runs":
			request := runRequests.Add(1)
			if request == 2 && r.Header.Get("If-None-Match") != "" {
				unexpectedETag.Store(true)
				http.Error(w, "active jobs must bypass the workflow-runs ETag", http.StatusBadRequest)
				return
			}
			if request == 3 && r.Header.Get("If-None-Match") == `"runs-v2"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			status, conclusion, etag := "in_progress", "null", `"runs-v1"`
			if request >= 2 {
				status, conclusion, etag = "completed", `"success"`, `"runs-v2"`
			}
			w.Header().Set("ETag", etag)
			fmt.Fprintf(w, `{"workflow_runs":[{"id":77,"name":"CI","display_title":"Fix retries","event":"pull_request","status":%q,"conclusion":%s,"workflow_id":9,"run_number":12,"run_attempt":2,"head_branch":"fix/retry","head_sha":"1234567890","html_url":"https://github.test/run/77","created_at":"2026-09-01T01:00:00Z","updated_at":"2026-09-01T01:02:00Z","run_started_at":"2026-09-01T01:00:05Z","actor":{"login":"amy"},"triggering_actor":{"login":"bob"},"pull_requests":[{"number":42}]}]}`, status, conclusion)
		case "/repos/hkt999rtk/repo/pulls/42":
			fmt.Fprint(w, `{"number":42,"title":"Actual PR title","html_url":"https://github.test/pr/42"}`)
		case "/repos/hkt999rtk/repo/actions/runs/77/jobs":
			request := jobsRequests.Add(1)
			if request == 1 {
				fmt.Fprint(w, `{"jobs":[{"id":1,"name":"test","status":"completed","conclusion":"failure","steps":[]},{"id":2,"name":"build","status":"queued","conclusion":null,"runner_name":"","labels":["self-hosted","macOS","ARM64"],"steps":[]}]}`)
				return
			}
			fmt.Fprint(w, `{"jobs":[{"id":1,"name":"test","status":"completed","conclusion":"success","steps":[]},{"id":2,"name":"build","status":"completed","conclusion":"success","runner_name":"rtk-ci-ameba-webrtc-m4","labels":["self-hosted","macOS","ARM64"],"steps":[]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &githubClient{baseURL: server.URL, token: "secret-token", http: server.Client()}
	p := newPoller(client, []Repository{{Owner: "hkt999rtk", Name: "repo", IsSubmodule: true}}, 15*time.Second)
	if err := p.refreshRepo(context.Background(), "hkt999rtk/repo"); err != nil {
		t.Fatal(err)
	}
	snapshot := p.snapshot()
	if len(snapshot.Queued) != 1 {
		t.Fatalf("queued = %d, want 1", len(snapshot.Queued))
	}
	card := snapshot.Queued[0]
	if card.Attempt != 2 || card.PR == nil || card.PR.Title != "Actual PR title" || card.Kind != "job" || card.JobName == "" {
		t.Fatalf("unexpected enriched card: %#v", card)
	}
	if err := p.refreshRepo(context.Background(), "hkt999rtk/repo"); err != nil {
		t.Fatal(err)
	}
	if unexpectedETag.Load() {
		t.Fatal("active jobs used the workflow-runs ETag")
	}
	snapshot = p.snapshot()
	var assigned Card
	for _, candidate := range snapshot.Completed {
		if candidate.JobName == "build" {
			assigned = candidate
		}
	}
	if assigned.RunnerName != "rtk-ci-ameba-webrtc-m4" || assigned.Status != "completed" {
		t.Fatalf("runner assignment was not refreshed: %#v", assigned)
	}
	if err := p.refreshRepo(context.Background(), "hkt999rtk/repo"); err != nil {
		t.Fatal(err)
	}
	if runRequests.Load() != 3 || len(p.snapshot().Completed) != 2 {
		t.Fatal("304 refresh should preserve completed job cards")
	}
	if p.snapshot().RateLimit.Remaining != 4990 {
		t.Fatal("rate limit headers were not captured")
	}
}

func TestPollerDoesNotCallGitHubWithoutClientActivity(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"workflow_runs":[]}`)
	}))
	defer server.Close()

	p := newPoller(&githubClient{baseURL: server.URL, token: "x", http: server.Client()}, []Repository{{Owner: "hkt999rtk", Name: "repo"}}, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	p.run(ctx)
	if requests.Load() != 0 {
		t.Fatalf("GitHub requests without a client = %d, want 0", requests.Load())
	}
}

func TestClientActivityWakesPollerAndExpires(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requestSeen <- struct{}{}:
		default:
		}
		fmt.Fprint(w, `{"workflow_runs":[]}`)
	}))
	defer server.Close()

	p := newPoller(&githubClient{baseURL: server.URL, token: "x", http: server.Client()}, []Repository{{Owner: "hkt999rtk", Name: "repo"}}, time.Hour)
	now := time.Now()
	p.recordClientActivityAt(now)
	if !p.clientActiveAt(now.Add(defaultClientIdleTimeout-time.Millisecond)) || p.clientActiveAt(now.Add(defaultClientIdleTimeout+time.Millisecond)) {
		t.Fatal("client activity did not expire at the idle timeout")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.run(ctx)
		close(done)
	}()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("first client activity did not trigger an immediate refresh")
	}
	cancel()
	<-done
}

func TestCardFromJobKeepsHierarchyAndSchedulingMetadata(t *testing.T) {
	started := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	parent := Card{Key: "hkt999rtk/repo/7", Kind: "run", Owner: "hkt999rtk", Repo: "repo", RunID: 7, RunNumber: 8, Attempt: 2, Workflow: "CI", CreatedAt: started.Add(-time.Minute)}
	card := cardFromJob(parent, Job{ID: 9, Name: "Linux tests", Status: "queued", Labels: []string{"self-hosted", "Linux", "X64"}}, 1)
	if card.Kind != "job" || card.JobID != 9 || card.JobName != "Linux tests" || card.Status != "queued" || len(card.RunnerLabels) != 3 || !strings.Contains(card.Key, "/job/Linux tests/1") {
		t.Fatalf("unexpected job card: %#v", card)
	}
}

func TestRefreshFailureKeepsCardsAndMarksRepoStale(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"workflow_runs":[{"id":1,"name":"CI","display_title":"manual smoke","event":"workflow_dispatch","status":"completed","conclusion":"success","run_attempt":1,"created_at":"2026-09-01T01:00:00Z","updated_at":"2026-09-01T01:01:00Z","run_started_at":"2026-09-01T01:00:00Z","actor":{"login":"ops"}}]}`)
	}))
	defer server.Close()
	p := newPoller(&githubClient{baseURL: server.URL, token: "x", http: server.Client()}, []Repository{{Owner: "hkt999rtk", Name: "repo"}}, time.Second)
	if err := p.refreshRepo(context.Background(), "hkt999rtk/repo"); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	err := p.refreshRepo(context.Background(), "hkt999rtk/repo")
	if err == nil {
		t.Fatal("expected API failure")
	}
	p.recordFailure("hkt999rtk/repo", err)
	snapshot := p.snapshot()
	if len(snapshot.Completed) != 1 || !snapshot.Repositories[0].Stale || !strings.Contains(snapshot.Repositories[0].Error, "503") {
		t.Fatalf("stale snapshot did not preserve data: %#v", snapshot)
	}
}

func TestDetailLoadsEveryAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/attempts/1/jobs"):
			fmt.Fprint(w, `{"jobs":[{"id":1,"name":"test","status":"completed","conclusion":"failure","steps":[{"number":1,"name":"run","status":"completed","conclusion":"failure"}]}]}`)
		case strings.Contains(r.URL.Path, "/attempts/2/jobs"):
			fmt.Fprint(w, `{"jobs":[{"id":2,"name":"test","status":"in_progress","steps":[]}]}`)
		case strings.HasSuffix(r.URL.Path, "/attempts/1"):
			fmt.Fprint(w, `{"status":"completed","conclusion":"failure","run_attempt":1,"run_started_at":"2026-09-01T01:00:00Z","updated_at":"2026-09-01T01:01:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/attempts/2"):
			fmt.Fprint(w, `{"status":"in_progress","run_attempt":2,"run_started_at":"2026-09-01T01:02:00Z","updated_at":"2026-09-01T01:02:30Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := newPoller(&githubClient{baseURL: server.URL, token: "x", http: server.Client()}, []Repository{{Owner: "hkt999rtk", Name: "repo"}}, time.Second)
	p.repos["hkt999rtk/repo"].runs = []Card{{Key: "hkt999rtk/repo/7", Owner: "hkt999rtk", Repo: "repo", RunID: 7, Attempt: 2, Status: "in_progress"}}
	detail, err := p.detail(context.Background(), "hkt999rtk", "repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 || detail.Attempts[0].Conclusion != "failure" || len(detail.Attempts[0].Jobs[0].Steps) != 1 {
		t.Fatalf("unexpected detail: %#v", detail)
	}
}

func TestManualTopicFallback(t *testing.T) {
	run := workflowRun{Name: "Deploy", Event: "workflow_dispatch", Actor: apiActor{Login: "ops"}, HeadBranch: "main", RunAttempt: 1}
	card := cardFromRun(Repository{Owner: "hkt999rtk", Name: "repo"}, run)
	if card.Topic != "Deploy · @ops · main" {
		t.Fatalf("topic = %q", card.Topic)
	}
}

func TestRetryUntilOnlyUsesRateLimitResetForRateLimitResponses(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	header := make(http.Header)
	header.Set("X-RateLimit-Remaining", "4875")
	header.Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
	if got := retryUntil(http.StatusBadGateway, header); !got.IsZero() {
		t.Fatalf("502 retry deferred until %v, want normal exponential backoff", got)
	}
	header.Set("X-RateLimit-Remaining", "0")
	if got := retryUntil(http.StatusForbidden, header); !got.Equal(reset) {
		t.Fatalf("rate-limit reset = %v, want %v", got, reset)
	}
}
