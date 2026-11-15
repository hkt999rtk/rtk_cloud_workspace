package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Synthetic external publication input. Image keys are exact repository names;
// these never represent proof that a real image has been pulled or executed.
type isolatedPublicationInput struct {
	Repository, Revision, Primary string
	RunID                         int64
	Attempt                       int
	Images                        map[string]string
}

type isolatedPublicationDNSFixture struct {
	ciDir    string
	dns      *httptest.Server
	mu       sync.Mutex
	records  map[string][]map[string]any
	requests []string
}

// installIsolatedPublicationDNS creates only localhost responses and temp
// executable stubs. Native certificate/key operations deliberately fail in the
// docker stub and must be provided by a separately verified real-image bridge.
func installIsolatedPublicationDNS(t *testing.T, fakeBin, zone string, publications []isolatedPublicationInput, providerImages map[string]string) *isolatedPublicationDNSFixture {
	t.Helper()
	if !isolatedScriptTempPath(fakeBin) || zone != "example.test" || len(publications) == 0 {
		t.Fatal("isolated CI/DNS selection required")
	}
	f := &isolatedPublicationDNSFixture{ciDir: t.TempDir(), records: map[string][]map[string]any{}}
	responses := map[string]string{}
	images := map[string]map[string]string{}
	write := func(key string, raw []byte) {
		if _, exists := responses[key]; exists {
			t.Fatal("ambiguous synthetic CI endpoint", key)
		}
		name := fmt.Sprintf("response-%04d", len(responses))
		if err := os.WriteFile(filepath.Join(f.ciDir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		responses[key] = name
	}
	for _, p := range publications {
		if !strings.HasPrefix(p.Repository, "hkt999rtk/") || !deploymentUpgradeSHA.MatchString(p.Revision) || p.RunID <= 0 || p.Attempt <= 0 || len(p.Images) == 0 || p.Images[p.Primary] == "" {
			t.Fatal("invalid isolated publication input")
		}
		roles := map[string]any{}
		for repo, digest := range p.Images {
			ref := repo + "@" + digest
			if !strings.HasPrefix(repo, "ghcr.io/"+p.Repository+"/") || !immutableEnvironmentImage.MatchString(ref) {
				t.Fatal("invalid isolated image role")
			}
			roles[repo] = map[string]string{"image": repo, "digest": digest}
			images[ref] = map[string]string{"digest": ref, "source": "https://github.com/" + p.Repository, "revision": p.Revision}
		}
		manifest := mustProviderRecoveryJSON(map[string]any{"schema": "rtk-service-image/v1", "repository": p.Repository, "source_commit": p.Revision, "run_id": fmt.Sprint(p.RunID), "run_attempt": fmt.Sprint(p.Attempt), "image": p.Primary, "digest": p.Images[p.Primary], "images": roles})
		archive := publicationZIP(t, "image-manifest.json", manifest)
		artifactID := p.RunID + 100000
		run := map[string]any{"id": p.RunID, "head_sha": p.Revision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": p.Attempt}
		for _, workflow := range []string{"release.yml", "ci.yml"} {
			write(fmt.Sprintf("repos/%s/actions/workflows/%s/runs?head_sha=%s&per_page=100", p.Repository, workflow, p.Revision), mustProviderRecoveryJSON(map[string]any{"total_count": 1, "workflow_runs": []any{run}}))
		}
		write(fmt.Sprintf("repos/%s/actions/runs/%d", p.Repository, p.RunID), mustProviderRecoveryJSON(run))
		write(fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100", p.Repository, p.RunID), mustProviderRecoveryJSON(map[string]any{"total_count": 1, "jobs": []any{map[string]string{"name": "Publish LKE image", "status": "completed", "conclusion": "success"}}}))
		write(fmt.Sprintf("repos/%s/actions/runs/%d/artifacts?per_page=100", p.Repository, p.RunID), mustProviderRecoveryJSON(map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": artifactID, "name": "image-manifest", "expired": false, "size_in_bytes": len(archive)}}}))
		write(fmt.Sprintf("repos/%s/actions/artifacts/%d/zip", p.Repository, artifactID), archive)
	}
	for reference, digestRef := range providerImages {
		if !immutableEnvironmentImage.MatchString(digestRef) || environmentImageRepository(reference) != environmentImageRepository(digestRef) {
			t.Fatal("provider image pin mismatch")
		}
		images[reference] = map[string]string{"digest": digestRef}
		images[digestRef] = map[string]string{"digest": digestRef}
	}
	if err := os.WriteFile(filepath.Join(f.ciDir, "responses.json"), mustProviderRecoveryJSON(responses), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ciDir, "images.json"), mustProviderRecoveryJSON(images), 0600); err != nil {
		t.Fatal(err)
	}
	gh := `#!/usr/bin/env python3
import json,os,sys
if len(sys.argv)!=3 or sys.argv[1]!='api' or os.environ.get('GH_TOKEN')!='isolated-fake-gh-only':sys.exit(71)
root=os.environ['RTK_ISOLATED_CI_DIR']
with open(os.path.join(root,'responses.json')) as f:m=json.load(f)
name=m.get(sys.argv[2])
if not name:sys.exit(72)
with open(os.path.join(root,name),'rb') as f:sys.stdout.buffer.write(f.read())
`
	docker := `#!/usr/bin/env python3
import json,os,sys
with open(os.path.join(os.environ['RTK_ISOLATED_CI_DIR'],'images.json')) as f:m=json.load(f)
a=sys.argv[1:]
if len(a)>=2 and a[0]=='--config':
 root=os.path.realpath(os.environ.get('RTK_CLOUD_CONFIG_ROOT',''))
 selected=os.path.realpath(a[1])
 if not root or os.path.commonpath([root,selected])!=root or not os.path.isfile(os.path.join(selected,'config.json')):sys.exit(74)
 a=a[2:]
if len(a)==4 and a[:3]==['pull','--platform','linux/amd64'] and a[3] in m:sys.exit(0)
if len(a)==5 and a[:2]==['image','inspect'] and a[3]=='--format' and a[2] in m:
 item=m[a[2]]
 if a[4]=='{{json .RepoDigests}}':print(json.dumps([item['digest']]));sys.exit(0)
 if a[4]=='{{json .Config.Labels}}' and item.get('source') and item.get('revision'):
  print(json.dumps({'org.opencontainers.image.source':item['source'],'org.opencontainers.image.revision':item['revision']}));sys.exit(0)
# Never fake bootstrap capabilities, native crypto, or a real Kubernetes job.
sys.exit(73)
`
	for name, body := range map[string]string{"gh": gh, "docker": docker} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.dns = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "sso-key isolated-dns-key:isolated-dns-secret" {
			http.Error(w, "synthetic DNS credentials required", 403)
			return
		}
		prefix := "/v1/domains/" + zone + "/records/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "unexpected DNS zone", 404)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.Split(key, "/")
		if len(parts) != 2 || parts[1] == "" || (parts[0] != "A" && parts[0] != "AAAA" && parts[0] != "CNAME" && parts[0] != "TXT") {
			http.Error(w, "unexpected record", 400)
			return
		}
		f.requests = append(f.requests, r.Method+" "+key)
		switch r.Method {
		case http.MethodGet:
			v := f.records[key]
			if v == nil {
				v = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(v)
		case http.MethodPut:
			var v []map[string]any
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v) != nil || v == nil {
				http.Error(w, "bad synthetic records", 400)
				return
			}
			f.records[key] = v
			w.WriteHeader(200)
		case http.MethodDelete:
			delete(f.records, key)
			w.WriteHeader(200)
		default:
			http.Error(w, "unsupported method", 405)
		}
	}))
	t.Cleanup(f.dns.Close)
	return f
}

func (f *isolatedPublicationDNSFixture) environment() map[string]string {
	return map[string]string{"RTK_ISOLATED_CI_DIR": f.ciDir, "RTK_CLOUD_GODADDY_API_ROOT": f.dns.URL}
}
func (f *isolatedPublicationDNSFixture) port(t *testing.T) int {
	t.Helper()
	_, p, err := net.SplitHostPort(f.dns.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if _, err = fmt.Sscan(p, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIsolatedPublicationDNSFixtureNativeReaders(t *testing.T) {
	bin := t.TempDir()
	repo := "hkt999rtk/rtk_video_cloud"
	revision := strings.Repeat("a", 40)
	primary := "ghcr.io/" + repo + "/video-cloud-api"
	emqx := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	bao := "ghcr.io/" + repo + "/video-cloud-openbao-pki"
	input := isolatedPublicationInput{Repository: repo, Revision: revision, Primary: primary, RunID: 77, Attempt: 1, Images: map[string]string{primary: "sha256:" + strings.Repeat("1", 64), emqx: "sha256:" + strings.Repeat("2", 64), bao: "sha256:" + strings.Repeat("3", 64)}}
	f := installIsolatedPublicationDNS(t, bin, "example.test", []isolatedPublicationInput{input}, map[string]string{"postgres:16": "docker.io/library/postgres@sha256:" + strings.Repeat("4", 64)})
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, filepath.Join(bin, name), args...)
		c.Env = []string{"PATH=/usr/bin:/bin", "RTK_ISOLATED_CI_DIR=" + f.ciDir, "GH_TOKEN=isolated-fake-gh-only"}
		return c.Output()
	}
	raw, id, attempt, err := latestEnvironmentPublication(t.Context(), repo, "release.yml", revision, false, run)
	if err != nil || id != 77 || attempt != 1 {
		t.Fatal("native publication reader", err)
	}
	for image, digest := range input.Images {
		if err := verifyPublicationJSON(raw, repo, id, attempt, environmentReleaseImage{Reference: image + "@" + digest, SourceRevision: revision}, false); err != nil {
			t.Fatal("three-role publication", err)
		}
	}
	pinned, err := resolveConfiguredProviderImage(t.Context(), "postgres:16", run)
	if err != nil || pinned != "docker.io/library/postgres@sha256:"+strings.Repeat("4", 64) {
		t.Fatal("synthetic provider image pin", err)
	}
	if _, err := run(t.Context(), "docker", "run", "--entrypoint", "/app/pkiceremony", primary); err == nil {
		t.Fatal("fixture falsely supplied real crypto")
	}
	adapter := &goDaddyDNSAdapter{client: f.dns.Client(), key: "isolated-dns-key", secret: "isolated-dns-secret", apiRoot: f.dns.URL}
	local := dnsAdapterContext{RootDomain: "example.test", Values: map[string]string{"DNS_RECORD_TTL": "600"}}
	zone := dnsZone{Name: "example.test"}
	record := dnsRecordSet{Name: "api.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}
	if initial, err := adapter.GetRecordSet(t.Context(), local, zone, record.Name, record.Type); err != nil || len(initial.Values) != 0 {
		t.Fatal("initial DNS", err)
	}
	if err := adapter.UpsertRecordSet(t.Context(), local, zone, record); err != nil {
		t.Fatal("DNS write", err)
	}
	if current, err := adapter.GetRecordSet(t.Context(), local, zone, record.Name, record.Type); err != nil || len(current.Values) != 1 || current.Values[0] != "192.0.2.1" {
		t.Fatal("DNS readback", err)
	}
	if err := adapter.DeleteRecordValues(t.Context(), local, zone, record); err != nil {
		t.Fatal("DNS removal", err)
	}
	if current, err := adapter.GetRecordSet(t.Context(), local, zone, record.Name, record.Type); err != nil || len(current.Values) != 0 {
		t.Fatal("DNS cleanup", err)
	}
}
