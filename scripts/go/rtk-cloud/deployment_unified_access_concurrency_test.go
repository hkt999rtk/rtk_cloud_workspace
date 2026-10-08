package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func planAccessReviewFixture(t *testing.T, review func(http.ResponseWriter, *http.Request, environmentAccessRequirement)) (environmentKubernetesBackend, *deploymentplan.Plan) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(directory, "rtk_cloud"))
	kube, err := json.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), "dev")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			APIVersion, Kind string
			Spec             struct {
				ResourceAttributes struct {
					environmentAccessRequirement
					Subresource string
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.APIVersion != "authorization.k8s.io/v1" || body.Kind != "SelfSubjectAccessReview" || body.Spec.ResourceAttributes.Subresource != "" {
			t.Error("invalid native access review", err, body)
			http.Error(w, "invalid fixture input", http.StatusBadRequest)
			return
		}
		review(w, r, body.Spec.ResourceAttributes.environmentAccessRequirement)
	}))
	t.Cleanup(server.Close)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	stub := `import json,os,stat,sys,urllib.request
from pathlib import Path
sys.stderr=open(os.environ['RTK_PLAN_ACCESS_ERROR_FILE'],'a')
args=sys.argv[1:]
assert args[0]=='--kubeconfig'
identity=Path(args[1])
root=Path(os.environ['RTK_PLAN_ACCESS_STORE_ROOT'])
assert identity.parent.parent==root/'deployment'/'tmp'
assert identity.name=='kubeconfig.yaml'
assert stat.S_IMODE(identity.stat().st_mode)==0o600
assert stat.S_IMODE(identity.parent.stat().st_mode)==0o700
assert args[2:]==['--request-timeout=90s','create','--raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews','-f','-']
request=urllib.request.Request(os.environ['RTK_PLAN_ACCESS_REVIEW_URL'],data=sys.stdin.buffer.read(),headers={'Content-Type':'application/json'},method='POST')
with urllib.request.urlopen(request,timeout=30) as response:
 sys.stdout.buffer.write(response.read())
`
	path := filepath.Join(directory, "kubectl")
	script := filepath.Join(directory, "kubectl.py")
	writeTestFile(t, script, stub)
	writeTestFile(t, path, "#!/bin/sh\nexec "+shellQuote(python)+" "+shellQuote(script)+" \"$@\"\n")
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_PLAN_ACCESS_STORE_ROOT", backend.Store.Root)
	t.Setenv("RTK_PLAN_ACCESS_REVIEW_URL", server.URL)
	t.Setenv("RTK_PLAN_ACCESS_ERROR_FILE", filepath.Join(directory, "fixture-errors"))
	t.Cleanup(func() {
		if t.Failed() {
			raw, _ := os.ReadFile(filepath.Join(directory, "fixture-errors"))
			t.Log(string(raw))
		}
	})
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: "Deployment/video-cloud-dev-platform/app", Action: "update", Desired: deploymentplan.Object{"apiVersion": "apps/v1"}}}}
	return backend, plan
}

func assertPlanAccessPrivateFilesRemoved(t *testing.T, backend environmentKubernetesBackend) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(backend.Store.Root, "deployment", "tmp"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("access review returned before private subprocess identity cleanup", entries)
	}
}

func TestPlanAccessReviewsOverlapWithAtMostFourExactRequests(t *testing.T) {
	var mu sync.Mutex
	var active, maximum int
	var observed []environmentAccessRequirement
	ready, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	backend, plan := planAccessReviewFixture(t, func(w http.ResponseWriter, r *http.Request, permission environmentAccessRequirement) {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		observed = append(observed, permission)
		if len(observed) == 4 {
			close(ready)
		}
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"allowed": true}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- backend.CheckPlanAccess(ctx, plan) }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("independent access reviews did not overlap")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("bounded access reviews did not finish")
	}
	requirements, err := environmentPlanAccessRequirements(plan)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != 4 || active != 0 || len(observed) != len(requirements) {
		t.Fatal("access reviews lost their concurrency bound, cleanup or coverage", maximum, active, len(observed))
	}
	pending := map[environmentAccessRequirement]bool{}
	for _, permission := range requirements {
		pending[permission] = true
	}
	for _, permission := range observed {
		if !pending[permission] {
			t.Fatal("access review changed or duplicated the exact resource/name/verb", permission)
		}
		delete(pending, permission)
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
}

func TestPlanAccessReviewsSelectFirstSortedDenialRegardlessOfCompletionOrder(t *testing.T) {
	laterDenied := make(chan struct{})
	var observed atomic.Int32
	backend, plan := planAccessReviewFixture(t, func(w http.ResponseWriter, r *http.Request, permission environmentAccessRequirement) {
		observed.Add(1)
		denied := permission.Resource == "deployments" && permission.Name == "app" && (permission.Verb == "get" || permission.Verb == "update")
		if denied && permission.Verb == "get" {
			select {
			case <-laterDenied:
			case <-r.Context().Done():
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"allowed": !denied, "reason": "credential=private-authorizer-detail"}})
		if denied && permission.Verb == "update" {
			w.(http.Flusher).Flush()
			close(laterDenied)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := backend.CheckPlanAccess(ctx, plan)
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.permissions" || blocker.Resource != "deployments/video-cloud-dev-platform/app" || blocker.Cause != "selected deployment identity lacks verified get permission for the planned resource" {
		t.Fatal("completion order selected a different permission denial", err)
	}
	if strings.Contains(err.Error(), "credential=") {
		t.Fatal("concurrent review leaked authorizer details")
	}
	requirements, err := environmentPlanAccessRequirements(plan)
	if err != nil || int(observed.Load()) != len(requirements) {
		t.Fatal("a denial returned before the independent reviews finished", err, observed.Load())
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
}

func TestPlanAccessReviewCancellationWaitsForLaunchedSubprocesses(t *testing.T) {
	ready := make(chan struct{})
	var observed, active atomic.Int32
	var handlers sync.WaitGroup
	backend, plan := planAccessReviewFixture(t, func(w http.ResponseWriter, r *http.Request, permission environmentAccessRequirement) {
		handlers.Add(1)
		defer handlers.Done()
		active.Add(1)
		defer active.Add(-1)
		if observed.Add(1) == 4 {
			close(ready)
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- backend.CheckPlanAccess(ctx, plan) }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("access review fixture did not launch four independent probes")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("canceled access reviews did not preserve cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled access reviews did not wait and finish")
	}
	stopped := make(chan struct{})
	go func() { handlers.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("a launched kubectl kept its request alive after cancellation")
	}
	if observed.Load() != 4 || active.Load() != 0 {
		t.Fatal("cancellation leaked or launched additional probes", observed.Load(), active.Load())
	}
	assertPlanAccessPrivateFilesRemoved(t, backend)
	if err := backend.CheckPlanAccess(ctx, plan); !errors.Is(err, context.Canceled) || observed.Load() != 4 {
		t.Fatal("an already canceled context invoked a review", err, observed.Load())
	}
}
