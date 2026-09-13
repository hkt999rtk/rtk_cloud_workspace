#!/usr/bin/env python3
"""Move the Dev API MQTT controller transport onto its managed Service key."""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

NAME = 'video-cloud-api'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
STATIC_SETTINGS = (
    'VIDEO_CLOUD_MQTT_MANAGEMENT_CERT',
    'VIDEO_CLOUD_MQTT_MANAGEMENT_KEY',
    'VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_CERT',
    'VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_KEY',
)


def managed_template(owner, image):
    m.require(re.fullmatch(
        r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}',
        image), 'immutable Dev Video Cloud image required')
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec'].get('containers', [])
    m.require(len(containers) == 1 and containers[0].get('name') == 'app',
              'API container ownership changed')
    container = containers[0]
    values = {entry['name']: entry.get('value') for entry in
              container.get('env', [])}
    m.require(values.get('VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE')
              and values.get('VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256') == ROOT,
              'managed API Service identity is unavailable')
    configured = [bool(values.get(name)) for name in STATIC_SETTINGS]
    m.require(all(configured) or not any(configured),
              'partial static MQTT controller credential configured')
    container['env'] = [entry for entry in container['env']
                        if entry['name'] not in STATIC_SETTINGS]
    container['image'] = image
    mounts = container.get('volumeMounts', [])
    mount_count = len([item for item in mounts
                       if item.get('name') == 'mqtt-pki-management'])
    m.require(mount_count == (1 if all(configured) else 0),
              'static MQTT management mount differs from settings')
    container['volumeMounts'] = [item for item in mounts
                                 if item.get('name') != 'mqtt-pki-management']
    volumes = template['spec'].get('volumes', [])
    volume_count = len([item for item in volumes
                        if item.get('name') == 'mqtt-pki-management'])
    m.require(volume_count == (1 if all(configured) else 0),
              'static MQTT management volume differs from settings')
    template['spec']['volumes'] = [item for item in volumes
                                   if item.get('name') != 'mqtt-pki-management']
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/r2-mqtt-controller-managed-identity'] = ROOT[:12]
    return template


def normalized_deployment(owner):
    return {'apiVersion': owner['apiVersion'], 'kind': owner['kind'],
            'metadata': {'name': owner['metadata']['name'],
                         'namespace': owner['metadata']['namespace']},
            'spec': owner['spec']}


class ManagedController(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev API MQTT controller transport managed Service identity')
        self.report['service_mqtt_controller_identity_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def go(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical Dev context mismatch')
        owner = self.obj('deployment', NAME)
        m.require(owner['spec'].get('replicas') == 1
                  and owner.get('status', {}).get('readyReplicas') == 1
                  and owner.get('status', {}).get('observedGeneration') ==
                  owner['metadata']['generation'], 'API is not ready')
        template = managed_template(owner, self.args.image)
        patches = [
            {'op': 'test', 'path': '/metadata/resourceVersion',
             'value': owner['metadata']['resourceVersion']},
            {'op': 'test', 'path': '/spec/template',
             'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ]
        if template != owner['spec']['template']:
            self.kube(['-n', NS, 'patch', 'deployment', NAME, '--type=json',
                       '--patch-file=/dev/stdin'], json.dumps(patches))
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME,
                   '--timeout=300s'], timeout=310)
        current = self.obj('deployment', NAME)
        m.require(current.get('status', {}).get('readyReplicas') == 1
                  and current.get('status', {}).get('updatedReplicas') == 1
                  and current.get('status', {}).get('observedGeneration') ==
                  current['metadata']['generation'], 'updated API is not ready')
        app = current['spec']['template']['spec']['containers'][0]
        names = {entry['name'] for entry in app.get('env', [])}
        m.require(not names.intersection(STATIC_SETTINGS)
                  and not any(item.get('name') == 'mqtt-pki-management'
                              for item in app.get('volumeMounts', []))
                  and not any(item.get('name') == 'mqtt-pki-management'
                              for item in current['spec']['template']['spec'].get('volumes', [])),
                  'static MQTT controller key remains mounted')
        time.sleep(22)
        logs = self.kube(['-n', NS, 'logs', 'deployment/' + NAME, '-c', 'app',
                          '--since=20s', '--tail=2000'])
        failures = ('tls: certificate required', 'pki authorization denied')
        m.require(not any(value in logs for value in failures),
                  'API MQTT controller or broker connection remains denied')
        rollout = self.base / 'pki/controller-bootstrap/rollout'
        m.write(rollout / (NAME + '-deployment.json'),
                normalized_deployment(current))
        self.check('api_mqtt_controller_managed_identity', {
            'image': app['image'], 'managed_identity':
            'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE',
            'static_controller_key_mounted': False,
            'recent_controller_and_mqtt_denials': 0,
            'persisted_deployment_matches_live': True,
            'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.phase = 'api-mqtt-controller-managed-identity'
    args.authority = args.activation = args.intermediate = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ManagedController(args)
    try:
        runner.go()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}),
              flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
