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
        self.rules = [{'ID': 'unrelated', 'AllowedOrigins': ['https://other.example'], 'AllowedMethods': ['GET']}]
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

class PublisherTests(unittest.TestCase):
    def test_staging_cors_and_each_activation_backup(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); release = root/'release'; publish = release/'publish'; publish.mkdir(parents=True)
            body = b'isolated test artifact'; (publish/'test.bin').write_bytes(body)
            manifest = {'version': 'B', 'test_only': True, 'artifacts': [{'filename': 'test.bin', 'size_bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()}]}
            (publish/'manifest.json').write_text(json.dumps(manifest))
            operator = root/'staging/operator/env'; operator.mkdir(parents=True)
            for key in ['LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID', 'LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY']:
                (operator/key).write_text('isolated-test')
            store = Store(); prefix = 'pro2-examples/staging/'
            for f in publish.iterdir(): store.objects[prefix+'releases/B/'+f.name] = f.read_bytes()
            config = {'SDK_ARTIFACT_ENDPOINT': 'https://objects.example', 'SDK_ARTIFACT_REGION': 'test', 'SDK_ARTIFACT_BUCKET': 'test'}
            secret = json.dumps({'data': {k: base64.b64encode(v.encode()).decode() for k, v in config.items()}}).encode()
            def get_secret(command):
                self.assertIn('video-cloud-staging-frontend', command)
                self.assertIn(str(root/'staging/kube/kubeconfig.yaml'), command)
                return secret
            def execute(activate=False):
                args = [str(SCRIPT), '--release-dir', str(release), '--environment', 'staging']
                if activate: args.append('--activate')
                with patch.dict(os.environ, {'RTK_CLOUD_CONFIG_ROOT': str(root)}), patch.object(sys, 'argv', args), patch('subprocess.check_output', side_effect=get_secret), patch('boto3.client', return_value=store):
                    runpy.run_path(str(SCRIPT), run_name='__main__')
            execute()
            self.assertEqual(store.rules[0]['ID'], 'unrelated')
            self.assertEqual(set(store.rules[1]['AllowedOrigins']), {'https://admin.video-cloud-staging.realtekconnect.com', 'https://frontend.video-cloud-staging.realtekconnect.com'})
            self.assertNotIn(prefix+'latest.json', store.objects)
            for previous in ['A', 'C']:
                store.objects[prefix+'latest.json'] = json.dumps({'version': previous}).encode()
                execute(True)
                self.assertEqual(json.loads((release/'previous-latest-staging.json').read_text())['version'], previous)
                self.assertEqual(json.loads(store.objects[prefix+'latest.json'])['version'], 'B')
            self.assertFalse((release/'previous-latest-dev.json').exists())

if __name__ == '__main__': unittest.main()
