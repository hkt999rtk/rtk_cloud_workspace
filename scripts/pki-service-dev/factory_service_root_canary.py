#!/usr/bin/env python3
"""Run the existing Dev Factory end-to-end canary after Service trust changes."""
import argparse
import importlib.util
import os
from pathlib import Path
import sys

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m = s.m


class CanaryRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Factory Service-root post-rollout end-to-end canary'
        self.save('report.json', self.report)

    def run(self):
        self.preflight()
        source = Path(__file__).with_name('factory_identity.py')
        factory_spec = importlib.util.spec_from_file_location('factory_identity', source)
        factory = importlib.util.module_from_spec(factory_spec)
        factory_spec.loader.exec_module(factory)
        factory.FactoryIdentityRun.factory_canary(self)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'canary'
    runner = CanaryRun(args)
    try:
        runner.run(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close()


if __name__ == '__main__':
    try: main()
    except Exception as error:
        print(str(error), file=sys.stderr); sys.exit(1)
