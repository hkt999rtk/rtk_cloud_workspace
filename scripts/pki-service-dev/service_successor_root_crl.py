#!/usr/bin/env python3
"""Publish the initial CRL for the activated Dev Service Root successor."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys
import subprocess
import uuid
spec=importlib.util.spec_from_file_location('service',Path(__file__).with_name('run.py'))
s=importlib.util.module_from_spec(spec);spec.loader.exec_module(s)
m,NS=s.m,s.NS
AM_NS='video-cloud-dev-account-manager'
class Run(s.ServiceRun):
 def __init__(self,args):
  super().__init__(args);self.report['foundation_scope']='Dev successor Service Root initial CRL publication';self.save('report.json',self.report)
 def go(self):
  m.require(self.kube(['config','current-context']).strip()==self.context,'canonical dev context mismatch')
  build=subprocess.run(['go','build','-o',str(self.ceremony),'./cmd/pkiceremony'],cwd=str(m.WORKSPACE/'repos/rtk_video_cloud'),env=dict(os.environ,GOWORK='off'),capture_output=True,timeout=180)
  m.require(build.returncode==0,'successor CRL ceremony build failed')
  self.forward('am',AM_NS,'account-manager',80);self.accounts=m.read(self.foundation/'accounts.json')
  source=Path(self.args.successor);saved=m.read(source/'root-ready.json');root=self.api('/issuers/'+saved['issuer_id'])
  m.require(root['status']=='active' and root['environment']=='dev' and root['trust_domain']=='service' and root['kind']=='root' and root['certificate_fingerprint_sha256']==saved['certificate_fingerprint_sha256'],'active successor root differs')
  count=self.kube(['-n','video-cloud-dev-platform','exec','-i','postgresql-0','--','psql','-X','-v','ON_ERROR_STOP=1','-U','postgres','-d','video_cloud','-At'],"SELECT count(*) FROM pki_crls WHERE issuer_id='"+root['issuer_id']+"';").strip()
  m.require(count in ('0','1'),'unexpected successor CRL history')
  if count=='1':
   record=self.api('/issuers/'+root['issuer_id']+'/crl');self.save('root-crl.json',record);self.check('successor_root_crl_present',{'issuer_id':root['issuer_id'],'crl_sha256':record['crl_sha256'],'reused':True});return
  now=dt.datetime.now(dt.timezone.utc).replace(microsecond=0);request={'issuer_id':root['issuer_id'],'issuer_fingerprint_sha256':root['certificate_fingerprint_sha256'],'crl_number':'1','this_update':m.stamp(now),'next_update':m.stamp(now+dt.timedelta(days=1)),'revocations':[]};self.save('root-crl-request.json',request)
  digest=self.ceremony_call(['crl-digest',self.output/'root-crl-request.json']).strip();passfile=m.read(source/'passphrase-reference.json')['path']
  self.save('successor-root.json',root)
  self.ceremony_call(['crl','--issuer',self.output/'successor-root.json','--crl-request',self.output/'root-crl-request.json','--expected-request-sha256',digest,'--key',source/'root-offline-simulation/ca-key.encrypted.pem','--passphrase-file',passfile,'--out',self.output/'root-crl'])
  manifest=m.read(self.output/'root-crl/public-manifest.json');raw=(self.output/'root-crl/revocations.pem').read_text();der=base64.b64decode(''.join(raw.splitlines()[1:-1]),validate=True)
  m.require(manifest['request']==request and manifest['request_sha256']==digest and manifest['crl_sha256']==m.digest(der),'successor CRL evidence differs')
  record=self.api('/issuers/'+root['issuer_id']+'/crl',{'crl_pem':raw});m.require(record['crl_sha256']==manifest['crl_sha256'],'published successor CRL differs');self.save('root-crl.json',record);self.check('successor_root_crl_published',{'issuer_id':root['issuer_id'],'crl_sha256':record['crl_sha256'],'next_update':record['next_update'],'resigned':False})
def main():
 p=argparse.ArgumentParser();p.add_argument('--config-root',default=os.environ.get('RTK_CLOUD_CONFIG_ROOT',str(Path.home()/'.config/rtk_cloud')));p.add_argument('--successor',required=True);p.add_argument('--output',required=True);a=p.parse_args();a.phase='publish-successor-root-crl';fd=os.open(Path(a.config_root).expanduser()/'dev/pki/service-rollout.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600);fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);r=Run(a)
 try:r.go();r.report['status']='passed'
 except Exception as e:r.report['status']='failed';r.report['failure']=str(e);raise
 finally:r.save('report.json',r.report);r.close();os.close(fd);print(json.dumps({'status':r.report['status'],'report':str(r.output/'report.json')}))
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'error':str(e)}),file=sys.stderr);sys.exit(1)
