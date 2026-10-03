"""Regression checks for environment isolation, CORS and activation rollback."""
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest.mock import patch
from botocore.exceptions import ClientError

SCRIPT = Path(__file__).with_name('publish_examples.py')

class Store:
    def __init__(self):
        self.objects = {}
        self.rules = [
            {'ID': 'unrelated', 'AllowedOrigins': ['https://other.example'], 'AllowedMethods': ['GET']},
            {'ID': 'pro2-examples-qa-browser', 'AllowedOrigins': ['https://qa.example'], 'AllowedMethods': ['HEAD']},
        ]
        self.cors_updates = 0
    def get_object(self, Bucket, Key):
        if Key not in self.objects:
            raise ClientError({'Error': {'Code': 'NoSuchKey'}}, 'GetObject')
        return {'Body': io.BytesIO(self.objects[Key]), 'ETag': 'current-etag'}
    def put_object(self, Bucket, Key, Body, **kwargs):
        assert kwargs.get('IfMatch') == 'current-etag'
        self.objects[Key] = Body
    def get_bucket_cors(self, **kwargs):
        return {'CORSRules': self.rules}
    def put_bucket_cors(self, CORSConfiguration, **kwargs):
        self.rules = CORSConfiguration['CORSRules']
        self.cors_updates += 1

class PublisherTests(unittest.TestCase):
    def test_staging_cors_and_each_activation_backup(self):
        self.check_cors_and_activation_backup('staging')

    def test_dev_cors_and_each_activation_backup(self):
        self.check_cors_and_activation_backup('dev')

    def check_cors_and_activation_backup(self, environment):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); release = root/'release'; publish = release/'publish'; publish.mkdir(parents=True)
            body = b'isolated test artifact'; (publish/'test.bin').write_bytes(body)
            manifest = {'version': 'B', 'test_only': True, 'artifacts': [{'filename': 'test.bin', 'size_bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()}]}
            (publish/'manifest.json').write_text(json.dumps(manifest))
            operator = root/environment/'operator/env'; operator.mkdir(parents=True)
            for key in ['LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID', 'LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY']:
                (operator/key).write_text('isolated-test')
            store = Store(); prefix = 'pro2-examples/'+environment+'/'
            existing_rules = list(store.rules)
            for f in publish.iterdir(): store.objects[prefix+'releases/B/'+f.name] = f.read_bytes()
            config = {'SDK_ARTIFACT_ENDPOINT': 'https://objects.example', 'SDK_ARTIFACT_REGION': 'test', 'SDK_ARTIFACT_BUCKET': 'test', 'PRO2_EXAMPLES_PREFIX': prefix}
            secret = json.dumps({'data': {k: base64.b64encode(v.encode()).decode() for k, v in config.items()}}).encode()
            def get_secret(command):
                self.assertIn('video-cloud-'+environment+'-frontend', command)
                self.assertIn(str(root/environment/'kube/kubeconfig.yaml'), command)
                return secret
            def execute(activate=False):
                args = [str(SCRIPT), '--release-dir', str(release), '--environment', environment]
                if activate: args.append('--activate')
                with patch.dict(os.environ, {'RTK_CLOUD_CONFIG_ROOT': str(root)}), patch.object(sys, 'argv', args), patch('subprocess.check_output', side_effect=get_secret), patch('boto3.client', return_value=store):
                    runpy.run_path(str(SCRIPT), run_name='__main__')
            execute()
            self.assertEqual(store.rules[:2], existing_rules)
            rule = store.rules[2]
            self.assertEqual(rule['ID'], 'pro2-examples-'+environment+'-browser')
            self.assertEqual(set(rule['AllowedOrigins']), {'https://admin.video-cloud-'+environment+'.realtekconnect.com', 'https://frontend.video-cloud-'+environment+'.realtekconnect.com'})
            self.assertEqual(rule['AllowedMethods'], ['GET', 'HEAD'])
            execute()
            self.assertEqual(store.cors_updates, 1)
            self.assertNotIn(prefix+'latest.json', store.objects)
            for previous in ['A', 'C']:
                store.objects[prefix+'latest.json'] = json.dumps({'version': previous}).encode()
                execute(True)
                self.assertEqual(json.loads((release/('previous-latest-'+environment+'.json')).read_text())['version'], previous)
                self.assertEqual(json.loads(store.objects[prefix+'latest.json'])['version'], 'B')
            other = 'dev' if environment == 'staging' else 'staging'
            self.assertFalse((release/('previous-latest-'+other+'.json')).exists())

    def test_reader_prefix_must_match_before_any_publication_side_effect(self):
        for environment in ['dev', 'staging']:
            other = 'dev' if environment == 'staging' else 'staging'
            for prefix in [None, '', 'pro2-examples/', 'pro2-examples/'+other+'/']:
                for activate in [False, True]:
                    with self.subTest(environment=environment, prefix=prefix, activate=activate), tempfile.TemporaryDirectory() as temp:
                        root = Path(temp)
                        config = {} if prefix is None else {'PRO2_EXAMPLES_PREFIX': prefix}
                        secret = json.dumps({'data': {k: base64.b64encode(v.encode()).decode() for k, v in config.items()}}).encode()
                        args = [str(SCRIPT), '--release-dir', str(root/'missing-release'), '--environment', environment]
                        if activate:
                            args.append('--activate')
                        with patch.dict(os.environ, {'RTK_CLOUD_CONFIG_ROOT': str(root)}), patch.object(sys, 'argv', args), patch('subprocess.check_output', return_value=secret), patch('boto3.client') as client, patch('requests.put') as upload:
                            with self.assertRaisesRegex(ValueError, 'Frontend PRO2_EXAMPLES_PREFIX must be pro2-examples/'+environment+'/'):
                                runpy.run_path(str(SCRIPT), run_name='__main__')
                            client.assert_not_called()
                            upload.assert_not_called()
                        self.assertEqual(list(root.iterdir()), [])

if __name__ == '__main__': unittest.main()
