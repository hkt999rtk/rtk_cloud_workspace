package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// unifiedSessionNativeBackend routes the production kubectl adapter to an
// isolated file-backed process. execResponses maps exact joined kubectl exec
// arguments (without the kubeconfig/request-timeout prefix) to stdout. A test
// may atomically replace exec-responses.json to model registry progress.
// This is a deliberately small API simulator, not a Kubernetes implementation.
func unifiedSessionNativeBackend(t *testing.T, store secretStore, objects map[string]deploymentplan.Object, execResponses map[string]string) (environmentKubernetesBackend, string, string) {
	t.Helper()
	b, dir, calls := lateNativeReadContractBackend(t, store, objects)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(execResponses)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exec-responses.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	script := `import fcntl, glob, json, os, re, sys, time, urllib.parse, uuid
root = os.environ['RTK_LATE_NATIVE_OBJECTS']
a = sys.argv[1:]
if len(a) < 4 or a[0] != '--kubeconfig' or not a[2].startswith('--request-timeout='):
    sys.exit(71)
with open(a[1], 'rb') as f:
    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)
a = a[3:]
with open(os.environ['RTK_LATE_NATIVE_CALLS'], 'a') as f: f.write(' '.join(a) + '\n')
with open(os.path.join(root, '.lock'), 'a') as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    def fail(message):
        print(message, file=sys.stderr); sys.exit(1)
    def path(kind, namespace, name):
        for component in (kind, namespace, name):
            if '/' in component or component in ('.', '..'): fail('invalid resource')
        return os.path.join(root, kind+'_'+namespace+'_'+name+'.json')
    def load(file):
        try:
            with open(file) as f: return json.load(f)
        except FileNotFoundError: return None
    def save(file, obj):
        temp = file + '.tmp'
        with open(temp, 'w') as f: json.dump(obj, f)
        os.chmod(temp, 0o600); os.replace(temp, file)
    def all_objects():
        return [o for p in sorted(glob.glob(os.path.join(root, '*_*_*.json'))) if (o := load(p)) is not None]
    if a[0] == 'get':
        kind = a[1]
        if len(a) >= 4 and a[-3:] == ['--ignore-not-found=true', '-o', 'json']:
            name = a[2]; ns = a[a.index('--namespace')+1] if '--namespace' in a else ''
            obj = load(path(kind, ns, name))
            if obj is not None: print(json.dumps(obj))
        else:
            ns = a[a.index('--namespace')+1] if '--namespace' in a else None
            objs = all_objects()
            if kind == 'namespace': items = [o for o in objs if o.get('kind') == 'Namespace']
            elif kind == 'validatingwebhookconfiguration': items = [o for o in objs if o.get('kind') == 'ValidatingWebhookConfiguration']
            elif ns is not None: items = [o for o in objs if o.get('metadata', {}).get('namespace', '') == ns]
            elif '-l' in a:
                label = a[a.index('-l')+1].split('=', 1)
                items = [o for o in objs if not o.get('metadata', {}).get('namespace') and o.get('metadata', {}).get('labels', {}).get(label[0]) == label[1]]
            else: fail('unsupported inventory')
            print(json.dumps({'items': items}))
    elif a[0] in ('create', 'replace') and a[1:5] == ['-f', '-', '-o', 'json']:
        obj = json.load(sys.stdin); meta = obj['metadata']; kind = obj['kind']; ns = meta.get('namespace', ''); name = meta['name']
        if kind == 'Deployment':
            obj.setdefault('spec', {}).setdefault('template', {}).setdefault('spec', {}).setdefault('dnsPolicy', 'ClusterFirst')
        file = path(kind, ns, name); old = load(file)
        if a[0] == 'create' and old is not None: fail('AlreadyExists')
        if a[0] == 'replace':
            if old is None: fail('NotFound')
            oldmeta = old['metadata']
            if meta.get('uid') != oldmeta.get('uid') or meta.get('resourceVersion') != oldmeta.get('resourceVersion'): fail('Conflict')
            meta['uid'] = oldmeta['uid']
            meta['resourceVersion'] = uuid.uuid4().hex
        else:
            meta['uid'] = 'fixture-' + uuid.uuid4().hex
            meta['resourceVersion'] = uuid.uuid4().hex
        if '--dry-run=server' not in a: save(file, obj)
        print(json.dumps(obj))
    elif a[0] == 'delete' and len(a) == 4 and a[1].startswith('--raw=') and a[2:] == ['-f', '-']:
        url = urllib.parse.urlparse(a[1][6:]); parts = url.path.split('/')
        if len(parts) != 8 or parts[1:4] != ['apis','coordination.k8s.io','v1'] or parts[4] != 'namespaces' or parts[6] != 'leases': fail('unsupported delete')
        file = path('Lease', urllib.parse.unquote(parts[5]), urllib.parse.unquote(parts[7])); old = load(file); options = json.load(sys.stdin)
        if old is None: fail('NotFound')
        pre = options.get('preconditions', {})
        if pre.get('uid') != old['metadata'].get('uid') or pre.get('resourceVersion') != old['metadata'].get('resourceVersion'): fail('Conflict')
        if 'dryRun' not in options and 'dryRun' not in urllib.parse.parse_qs(url.query): os.unlink(file)
        print('{}')
    elif 'exec' in a:
        ackdir = os.environ.get('RTK_UNIFIED_ACK_DIR', '')
        if ackdir and 'registry-read' in a:
            query = a[-1]
            ids = re.findall(r"session_id='([0-9a-f-]{36})'", query)
            if len(ids) != 1: fail('unexpected native registry query')
            ident = uuid.uuid4().hex
            request = {'id':ident, 'action':'select', 'session':ids[0], 'subject':''}
            tmp = os.path.join(ackdir, 'request-kube.tmp')
            with open(tmp, 'w') as f: json.dump(request, f)
            os.chmod(tmp, 0o600); os.replace(tmp, os.path.join(ackdir, 'request.json'))
            deadline = time.monotonic()+15
            while time.monotonic() < deadline:
                response = load(os.path.join(ackdir, 'response.json')) or {}
                if response.get('id') == ident:
                    if response.get('error'): fail('isolated native registry query failed')
                    print(json.dumps(response.get('result'))); break
                time.sleep(0.05)
            else: fail('isolated native registry query timed out')
        else:
            responses = load(os.path.join(root, 'exec-responses.json')) or {}
            key = ' '.join(a)
            if key not in responses: fail('unexpected exec')
            sys.stdout.write(responses[key])
    elif ('rollout' in a and 'status' in a) or 'wait' in a:
        if os.path.exists(os.path.join(root, 'fail-ready')): fail('not ready')
    else: fail('unsupported kubectl command')
`
	path := filepath.Join(dir, "unified-kubectl.py")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nexec \"$RTK_UNIFIED_PYTHON\" \"$RTK_UNIFIED_KUBECTL\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_UNIFIED_PYTHON", python)
	t.Setenv("RTK_UNIFIED_KUBECTL", path)
	return b, dir, calls
}
