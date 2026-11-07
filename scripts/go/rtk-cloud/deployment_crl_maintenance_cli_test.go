package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This fake is a Kubernetes/process boundary. The production CLI still resolves
// configuration, freezes its kubeconfig, locks, owns the native Lease, discovers
// actual manifests/identities, installs through pkitrust, observes ACKs, and
// cleans up. No production dependency is replaced or bypassed.
const maintenanceCLIKubectl = `#!/usr/bin/env python3
import json, os, pathlib, stat, sys
root=pathlib.Path(os.environ['RTK_CRL_CLI_FIXTURE'])
fixture=json.loads((root/'fixture.json').read_text())
args=sys.argv[1:]
kube=pathlib.Path(args[args.index('--kubeconfig')+1])
if not str(kube).startswith(fixture['store']+'/deployment/tmp/') or stat.S_IMODE(kube.stat().st_mode)!=0o600:
 sys.exit(71)
if 'must-not-execute' in kube.read_text(): sys.exit(72)
filtered=[]
n=0
while n<len(args):
 if args[n] in ('--kubeconfig','--namespace'): n+=2;continue
 if args[n].startswith('--request-timeout='): n+=1;continue
 filtered.append(args[n]);n+=1
args=filtered
joined=' '.join(args)
lease=root/'lease.json'
def emit(value): print(json.dumps(value,separators=(',',':')))
def event(value):
 with (root/'events').open('a') as f:f.write(value+'\n')
if args[0]=='get':
 kind=args[1].lower()
 if kind=='lease':
  if lease.exists():print(lease.read_text())
  sys.exit(0)
 if kind=='pods':emit({'items':[fixture['pod']]});sys.exit(0)
 names={'pod':'pod','configmap':'cm','secret':'secret','deployment':'controller'}
 if kind in names:emit(fixture[names[kind]]);sys.exit(0)
 sys.exit(73)
if args[0]=='create':
 body=json.load(sys.stdin)
 if body.get('kind')!='Lease':sys.exit(74)
 if '--dry-run=server' in args:event('lease-admit');emit(body);sys.exit(0)
 if lease.exists():sys.exit(75)
 body['metadata']['uid']='original-cli-lease';body['metadata']['resourceVersion']='1'
 lease.write_text(json.dumps(body));event('lease-acquire');emit(body);sys.exit(0)
if args[0]=='delete':
 body=json.load(sys.stdin)
 if body.get('preconditions')!={'uid':'original-cli-lease','resourceVersion':'1'}:sys.exit(76)
 event('lease-release')
 if fixture['mode']=='cleanup-failure':sys.exit(77)
 lease.unlink();emit({'status':'Success'});sys.exit(0)
if args[0]=='exec':
 if 'json_agg(document' in joined:
  if fixture['mode']=='query-failure':sys.exit(78)
  emit([fixture['issuer']]);sys.exit(0)
 if "json_build_object('prior'" in joined:emit(fixture['snapshot']);sys.exit(0)
 if 'SELECT EXISTS' in joined:
  if 'pki_service_client_issuances' in joined and ("status='succeeded'" not in joined or "fingerprint='" not in joined):sys.exit(79)
  print('f' if fixture['mode']=='missing-ack' and 'pki_crl_acknowledgments' in joined else 't');sys.exit(0)
 if args[-2:]==['cat','/state/client.json']:print(fixture['identity']);sys.exit(0)
 if args[-2:]==['cat','/state/root-crl.json']:
  emit({'issuer_fingerprint':fixture['issuer']['certificate_fingerprint_sha256'],'crl':fixture['snapshot']['prior']});sys.exit(0)
 if args[-2:]==['cat','/public/client.crl']:sys.stdout.write(fixture['public']);sys.exit(0)
 if '/app/pkitrust apply-crl' in joined:
  import base64
  lines=sys.stdin.read().splitlines()
  if len(lines)!=2:sys.exit(80)
  record=json.loads(base64.b64decode(lines[1]))
  if record!=fixture['snapshot']['prior']:sys.exit(81)
  event('native-install');sys.exit(0)
 sys.exit(82)
sys.exit(83)
`

type maintenanceLockedStatusWriter struct {
	io.Writer
	directory string
	observed  bool
}

func (w *maintenanceLockedStatusWriter) Write(raw []byte) (int, error) {
	lock, err := deploymentplan.LockExecutionOwner(w.directory, false)
	if err == nil {
		_ = lock.Close()
		return 0, fmt.Errorf("maintenance host lock was released before final status emission")
	}
	w.observed = true
	return w.Writer.Write(raw)
}

func TestCRLMaintenanceCLIOrchestratesLeaseStatusAndCleanup(t *testing.T) {
	for _, mode := range []string{"read-only", "healthy", "query-failure", "missing-ack", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			kube, _ := yaml.Marshal(frozenKubernetesFixture(t))
			store := makeIsolatedTestSecretStore(t, "staging")
			if err := store.write("kube/kubeconfig.yaml", kube, false); err != nil {
				t.Fatal(err)
			}
			f := maintenanceRuntimeTestFixture(t)
			ns := "video-cloud-staging-video-cloud"
			for _, entry := range []struct {
				o               deploymentplan.Object
				kind, name, uid string
			}{{f.cm, "ConfigMap", "trust", "original-cm"}, {f.secret, "Secret", "original-public", "secret-original"}, {f.controller, "Deployment", "pki-controller", "original-controller"}} {
				entry.o["apiVersion"] = "v1"
				entry.o["kind"] = entry.kind
				meta := deploymentplan.Map(entry.o["metadata"])
				if len(meta) == 0 {
					meta = deploymentplan.Object{}
					entry.o["metadata"] = meta
				}
				meta["name"] = entry.name
				meta["namespace"] = ns
				meta["uid"] = entry.uid
			}
			fixture := map[string]any{"store": store.Root, "mode": mode, "issuer": f.item.Issuer.Document, "snapshot": f.item.Snapshot, "pod": f.pod, "cm": f.cm, "secret": f.secret, "controller": f.controller, "identity": string(f.identity), "public": f.public}
			dir := t.TempDir()
			raw, _ := json.Marshal(fixture)
			if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			fake := filepath.Join(dir, "kubectl")
			if err := os.WriteFile(fake, []byte(maintenanceCLIKubectl), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CRL_CLI_FIXTURE", dir)
			t.Setenv("RTK_CLOUD_KUBECTL", fake)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			previous := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			if mode != "read-only" {
				if err := maintenanceWrite(store, crlMaintenanceBase+"/status.json", crlMaintenanceStatus{Environment: "staging", LastSuccess: previous, Completed: true}); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostics bytes.Buffer
			var output io.Writer = &out
			var lockedOutput *maintenanceLockedStatusWriter
			if mode == "healthy" {
				lockedOutput = &maintenanceLockedStatusWriter{Writer: &out, directory: filepath.Join(store.Root, crlMaintenanceBase)}
				output = lockedOutput
			}
			args := []string{"--workspace", workspace, "--environment", "staging", "--timeout", "20s"}
			if mode != "read-only" {
				args = append(args, "--execute", "--confirm", "video-cloud-staging")
			}
			if mode == "missing-ack" {
				args = append(args, "--timeout", "3s")
			}
			err := runCRLMaintenanceCLI(context.Background(), args, output, &diagnostics)
			wantSuccess := mode == "read-only" || mode == "healthy"
			if (err == nil) != wantSuccess {
				t.Fatalf("result %v status=%s diagnostics=%s", err, out.String(), diagnostics.String())
			}
			if lockedOutput != nil && !lockedOutput.observed {
				t.Fatal("final status was not emitted while the host lock remained owned")
			}
			var status crlMaintenanceStatus
			if e := json.Unmarshal(out.Bytes(), &status); e != nil {
				t.Fatalf("missing final structured status: %s: %v", out.String(), e)
			}
			if status.Completed != (mode == "healthy") {
				t.Fatalf("incorrect completion after %s: %+v", mode, status)
			}
			events, _ := os.ReadFile(filepath.Join(dir, "events"))
			if mode == "read-only" {
				if len(events) != 0 {
					t.Fatalf("read-only mutated: %s", events)
				}
				if _, e := os.Stat(filepath.Join(store.Root, crlMaintenanceBase, "status.json")); !os.IsNotExist(e) {
					t.Fatal("read-only wrote status")
				}
			} else {
				if !strings.Contains(string(events), "lease-admit\nlease-acquire\n") || !strings.HasSuffix(string(events), "lease-release\n") {
					t.Fatalf("lease lifecycle incomplete: %s", events)
				}
				var saved crlMaintenanceStatus
				if e := maintenanceRead(store, crlMaintenanceBase+"/status.json", &saved); e != nil {
					t.Fatal(e)
				}
				if saved.Completed != status.Completed || saved.Failure != status.Failure {
					t.Fatal("persisted status disagrees with final result")
				}
				if mode == "healthy" {
					if !saved.LastSuccess.After(previous) || saved.Due {
						t.Fatal("healthy run did not update success")
					}
				} else {
					if saved.Failure == "" || !saved.LastSuccess.Equal(previous) {
						t.Fatal("failed run erased last real success or reported success")
					}
				}
				lock, e := deploymentplan.LockExecutionOwner(filepath.Join(store.Root, crlMaintenanceBase), false)
				if e != nil {
					t.Fatalf("host lock retained after return: %v", e)
				}
				_ = lock.Close()
			}
			_, leaseErr := os.Stat(filepath.Join(dir, "lease.json"))
			if mode == "cleanup-failure" {
				if leaseErr != nil {
					t.Fatal("failed cleanup erased original lease")
				}
			} else if !os.IsNotExist(leaseErr) {
				t.Fatal("successful cleanup retained lease")
			}
			temporary, e := filepath.Glob(filepath.Join(store.Root, "deployment/tmp/kubernetes-api-*"))
			if e != nil || len(temporary) != 0 {
				t.Fatalf("frozen credential files leaked: %v %v", temporary, e)
			}
		})
	}
}
