package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Exercise the native command boundary through exact publication ZIP evidence
// and isolated Docker credential handling; no provider or registry is contacted.
func TestServiceAuthorizationRepairPublicationRequiresExactSourceAndProvenance(t *testing.T) {
	for _, mode := range []string{"published", "wrong-parent", "unrelated-source", "missing-publication", "wrong-artifact-source", "pull-fails", "wrong-image-revision", "ci-fails"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			cfg := deploymentConfig{Environment: "staging", Workspace: t.TempDir()}
			for key, value := range map[string]string{"GHCR_PULL_USERNAME": "isolated-user", "GHCR_PULL_TOKEN": "isolated-token"} {
				if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
					t.Fatal(err)
				}
			}
			base, revision := strings.Repeat("a", 40), strings.Repeat("b", 40)
			image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("c", 64)
			manifest := map[string]any{"schema": "rtk-service-image/v1", "repository": "hkt999rtk/rtk_video_cloud", "source_commit": revision, "run_id": "77", "run_attempt": "2", "image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", "digest": "sha256:" + strings.Repeat("c", 64)}
			if mode == "wrong-artifact-source" {
				manifest["source_commit"] = base
			}
			archive := publicationZIP(t, "image-manifest.json", mustProviderRecoveryJSON(manifest))
			dir := t.TempDir()
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			state := map[string]any{"mode": mode, "base": base, "revision": revision, "image": image, "archive": base64.StdEncoding.EncodeToString(archive), "size": len(archive), "store": store.Root, "workspace": cfg.Workspace}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), mustProviderRecoveryJSON(state), 0600); err != nil {
				t.Fatal(err)
			}
			script := "#!" + python + "\n" + `import base64,json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_SERVICE_AUTH_PUBLICATION'])
s=json.loads((root/'state.json').read_text());mode=s['mode'];a=sys.argv[1:];tool=Path(sys.argv[0]).name
with (root/'calls').open('a') as f:f.write(json.dumps([tool]+a)+'\n')
if tool=='git':
 assert a[:2]==['-C',str(Path(s['workspace'])/'repos/rtk_video_cloud')]
 if a[2:]==['rev-list','--parents','-n','1',s['revision']]: print(s['revision']+' '+('f'*40 if mode=='wrong-parent' else s['base']));sys.exit()
 assert a[2:]==['diff','--name-only',s['base'],s['revision']]
 print('internal/pkicontrollerapp/provider_transport.go\n'+('unrelated.go' if mode=='unrelated-source' else 'internal/pkicontrollerapp/provider_transport_test.go'));sys.exit()
if tool=='docker':
 if a[0]=='--config':
  assert a[2:]==['pull','--platform','linux/amd64',s['image']]
  path=Path(a[1]);assert path.is_relative_to(Path(s['store'])/'deployment/tmp')
  auth=json.loads((path/'config.json').read_text());assert auth['auths']['ghcr.io']['auth']==base64.b64encode(b'isolated-user:isolated-token').decode()
  sys.exit(1 if mode=='pull-fails' else 0)
 assert a==['image','inspect',s['image'],'--format','{{json .Config.Labels}}']
 print(json.dumps({'org.opencontainers.image.revision':s['base'] if mode=='wrong-image-revision' else s['revision']}));sys.exit()
assert tool=='gh' and len(a)==2 and a[0]=='api' and os.environ['GH_TOKEN']=='isolated-token'
prefix='repos/hkt999rtk/rtk_video_cloud/actions/';assert a[1].startswith(prefix);q=a[1][len(prefix):]
run={'id':77,'head_sha':s['revision'],'head_branch':'main','event':'push','status':'completed','conclusion':'success','run_attempt':2}
if q.startswith('workflows/'):
 assert q in ['workflows/'+w+'/runs?head_sha='+s['revision']+'&per_page=100' for w in ['ci.yml','release.yml']]
 if mode=='missing-publication' or mode=='ci-fails' and q.startswith('workflows/ci.yml'):run['conclusion']='failure'
 print(json.dumps({'total_count':1,'workflow_runs':[run]}))
elif q=='runs/77':print(json.dumps(run))
elif q=='runs/77/artifacts?per_page=100':print(json.dumps({'total_count':1,'artifacts':[{'id':99,'name':'image-manifest','expired':False,'size_in_bytes':s['size']}]}))
elif q=='artifacts/99/zip':sys.stdout.buffer.write(base64.b64decode(s['archive']))
elif q=='runs/77/jobs?per_page=100':print(json.dumps({'total_count':1,'jobs':[{'name':'Publish LKE image','status':'completed','conclusion':'success'}]}))
else:sys.exit(1)
`
			for _, tool := range []string{"git", "gh", "docker"} {
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_SERVICE_AUTH_PUBLICATION", dir)
			release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {SourceRevision: base}}}
			before := createBootstrapStateSnapshot(t, store.Root)
			got, err := qualifyServiceAuthorizationControllerImage(t.Context(), cfg, release, revision)
			if mode == "published" {
				if err != nil || got.Reference != image || got.SourceRevision != revision {
					t.Fatal("formal exact-source repair rejected", got, err)
				}
			} else if err == nil {
				t.Fatal("unverified publication accepted")
			}
			after := createBootstrapStateSnapshot(t, store.Root)
			for path, value := range before {
				if after[path] != value {
					t.Fatal("publication changed original environment data", path)
				}
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			var commands [][]string
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				var args []string
				if json.Unmarshal([]byte(line), &args) != nil {
					t.Fatal("invalid native command trace")
				}
				commands = append(commands, args)
			}
			if !reflect.DeepEqual(commands[0], []string{"git", "-C", filepath.Join(cfg.Workspace, "repos/rtk_video_cloud"), "rev-list", "--parents", "-n", "1", revision}) {
				t.Fatal("source provenance used wrong repository or revision")
			}
			if mode == "wrong-parent" && len(commands) != 1 || mode == "unrelated-source" && len(commands) != 2 {
				t.Fatal("unreviewed source reached artifact or image operations")
			}
			if matches, err := filepath.Glob(filepath.Join(store.Root, "deployment/tmp/image-pull-*")); err != nil || len(matches) != 0 {
				t.Fatal("registry credentials retained after qualification", matches, err)
			}
		})
	}
}
