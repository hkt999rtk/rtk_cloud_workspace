#!/usr/bin/env python3
"""Activate the reviewed final Dev Service authority after its receipt gates pass."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys
import time

spec = importlib.util.spec_from_file_location(
    'final_authority', Path(__file__).with_name('service_successor_final_authority.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)

def retry_reads(runner):
    original = runner.api
    def api(path, body=None, *args, **kwargs):
        if body is not None:
            return original(path, body, *args, **kwargs)
        for attempt in range(3):
            try:
                return original(path, None, *args, **kwargs)
            except RuntimeError as error:
                if 'status 503' not in str(error) or attempt == 2:
                    raise
                time.sleep(attempt + 1)
    runner.api = api


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--prepared', required=True,
                        help='reconciled final-authority evidence directory')
    parser.add_argument('--authority', required=True,
                        help='active successor-root evidence directory')
    parser.add_argument('--phase', choices=('full', 'after-controller'), default='full')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.phase = 'activate-final-successor-intermediate'
    args.intermediate = args.image = args.activation = None
    args.resume = False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = f.FinalAuthority(args)
    try:
        retry_reads(runner)
        runner.preflight()
        if args.phase == 'full':
            runner.install_controller()
        runner.install_certissuer()
        runner.activate_v2()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
