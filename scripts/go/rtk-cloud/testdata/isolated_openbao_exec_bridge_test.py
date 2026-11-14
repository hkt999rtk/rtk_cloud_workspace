import os
import pathlib
import subprocess
import tempfile
import unittest

BRIDGE = pathlib.Path(__file__).with_name('isolated_openbao_exec_bridge.py')


class BridgeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = pathlib.Path(self.tmp.name)
        self.kubeconfig = root / 'kubeconfig'
        self.kubeconfig.write_text('isolated test kubeconfig')
        self.calls = root / 'calls'
        self.docker = root / 'docker'
        self.docker.write_text('''#!/usr/bin/env python3
import os,sys
with open(os.environ['BRIDGE_CALLS'], 'a') as f: f.write(' '.join(sys.argv[1:]) + '\\n')
if sys.argv[1] == 'inspect':
 print(os.environ.get('BRIDGE_FAKE_LABEL', 'fixture-owner')); sys.exit(0)
if sys.argv[1] == 'exec':
 sys.stdout.write(sys.stdin.read()); sys.exit(int(os.environ.get('BRIDGE_FAKE_EXIT', '0')))
sys.exit(3)
''')
        self.docker.chmod(0o700)
        self.fallback = root / 'fallback'
        self.fallback.write_text('#!/bin/sh\nprintf fallback')
        self.fallback.chmod(0o700)
        self.env = dict(os.environ, RTK_TEST_BRIDGE_DOCKER=str(self.docker),
                        RTK_TEST_BRIDGE_FALLBACK=str(self.fallback),
                        RTK_TEST_BRIDGE_KUBECONFIG=str(self.kubeconfig),
                        RTK_TEST_BRIDGE_CONTAINER='a'*64,
                        RTK_TEST_BRIDGE_OWNER='fixture-owner',
                        RTK_TEST_BRIDGE_NAMESPACE='isolated-secrets',
                        BRIDGE_CALLS=str(self.calls))

    def call(self, args=None, env=None, data='native-input'):
        if args is None:
            args = ['--namespace', 'isolated-secrets', 'exec', '-i', 'openbao-0',
                    '--container', 'openbao', '--', 'bao', 'status']
        return subprocess.run(['python3', str(BRIDGE), '--kubeconfig', str(self.kubeconfig),
                               '--request-timeout=90s', *args], input=data, text=True,
                              capture_output=True, env=env or self.env)

    def test_owner_checked_and_stdin_reaches_exact_container(self):
        result = self.call()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'native-input')
        calls = self.calls.read_text().splitlines()
        self.assertEqual(len(calls), 2)
        self.assertIn('exec -i ' + 'a'*64 + ' bao status', calls[-1])

    def test_wrong_owner_or_wrong_target_fails_without_exec(self):
        self.env['BRIDGE_FAKE_LABEL'] = 'someone-else'
        result = self.call()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('exec ', self.calls.read_text())
        self.env['BRIDGE_FAKE_LABEL'] = 'fixture-owner'
        result = self.call(['--namespace', 'foreign', 'exec', 'openbao-0', '--container', 'openbao', '--', 'bao', 'status'])
        self.assertNotEqual(result.returncode, 0)

    def test_real_exit_code_propagates_and_other_commands_use_fallback(self):
        self.env['BRIDGE_FAKE_EXIT'] = '42'
        self.assertEqual(self.call().returncode, 42)
        self.assertEqual(self.call(['get', 'Namespace', 'fixture']).stdout, 'fallback')

    def test_foreign_kubeconfig_never_reaches_docker(self):
        foreign = pathlib.Path(self.tmp.name) / 'foreign'
        foreign.write_text('foreign')
        result = subprocess.run(['python3', str(BRIDGE), '--kubeconfig', str(foreign), '--request-timeout=90s',
                                 '--namespace', 'isolated-secrets', 'exec', 'openbao-0', '--container', 'openbao', '--', 'bao', 'status'],
                                capture_output=True, env=self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.calls.exists())


if __name__ == '__main__':
    unittest.main()
