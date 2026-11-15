#!/usr/bin/env python3
"""Test-only kubectl exec bridge; never infer readiness or generate receipts."""
import hmac
import os
import re
import subprocess
import sys


def fail():
    sys.stderr.write('isolated OpenBao bridge rejected command\n')
    sys.exit(1)


a = sys.argv[1:]
if len(a) < 4 or a[0] != '--kubeconfig' or not a[2].startswith('--request-timeout='):
    fail()
try:
    with open(a[1], 'rb') as selected, open(os.environ['RTK_TEST_BRIDGE_KUBECONFIG'], 'rb') as canonical:
        if not hmac.compare_digest(selected.read(), canonical.read()):
            fail()
except (KeyError, OSError):
    fail()
args = a[3:]
if 'exec' not in args:
    os.execv(os.environ['RTK_TEST_BRIDGE_FALLBACK'], [os.environ['RTK_TEST_BRIDGE_FALLBACK']] + a)
try:
    ns = args[args.index('--namespace') + 1]
    i = args.index('exec')
    sep = args.index('--', i + 1)
    before = args[i + 1:sep]
    target = next(x for x in before if x in ('openbao-0', 'pod/openbao-0', 'statefulset/openbao'))
    container = before[before.index('--container') + 1]
    if ns != os.environ['RTK_TEST_BRIDGE_NAMESPACE'] or container != 'openbao' or not args[sep + 1:]:
        fail()
except (KeyError, ValueError, IndexError, StopIteration):
    # Exec for unrelated resources belongs to the separate Kubernetes adapter.
    if any('openbao' in x for x in args):
        fail()
    os.execv(os.environ['RTK_TEST_BRIDGE_FALLBACK'], [os.environ['RTK_TEST_BRIDGE_FALLBACK']] + a)
container_id = os.environ.get('RTK_TEST_BRIDGE_CONTAINER', '')
owner = os.environ.get('RTK_TEST_BRIDGE_OWNER', '')
docker = os.environ.get('RTK_TEST_BRIDGE_DOCKER', '')
if not re.fullmatch(r'[a-f0-9]{64}', container_id) or not owner or not os.path.isabs(docker):
    fail()
try:
    inspection = subprocess.run([docker, 'inspect', '--format', '{{ index .Config.Labels "rtk.test.owner" }}', container_id],
                                check=False, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
except (OSError, subprocess.TimeoutExpired):
    fail()
if inspection.returncode or not hmac.compare_digest(inspection.stdout.strip(), owner.encode()):
    fail()
os.execv(docker, [docker, 'exec', '-i', container_id] + args[sep + 1:])
