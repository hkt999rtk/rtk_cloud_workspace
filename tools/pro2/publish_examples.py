#!/usr/bin/env python3
"""Publish isolated PRO2 evaluation releases to the canonical Dev artifact store."""
import argparse,base64,hashlib,json,subprocess,os
from pathlib import Path
import boto3
import requests
from botocore.exceptions import ClientError
from botocore.config import Config
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--release-dir',type=Path,required=True);p.add_argument('--activate',action='store_true');a=p.parse_args()
dev_store=Path(os.environ.get('RTK_CLOUD_CONFIG_ROOT',str(Path.home()/'.config/rtk_cloud')))/'dev'
k=['kubectl','--kubeconfig',str(dev_store/'kube/kubeconfig.yaml')]
raw=json.loads(subprocess.check_output(k+['-n','video-cloud-dev-frontend','get','secret','frontend-sdk-downloads','-o','json']))['data']
c={n:base64.b64decode(v).decode() for n,v in raw.items()}
operator=dev_store/'operator/env'
c['SDK_ARTIFACT_ACCESS_KEY_ID']=(operator/'LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID').read_text().strip()
c['SDK_ARTIFACT_SECRET_ACCESS_KEY']=(operator/'LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY').read_text().strip()
s=boto3.client('s3',config=Config(request_checksum_calculation='when_required',response_checksum_validation='when_required'),endpoint_url=c['SDK_ARTIFACT_ENDPOINT'],region_name=c['SDK_ARTIFACT_REGION'],aws_access_key_id=c['SDK_ARTIFACT_ACCESS_KEY_ID'],aws_secret_access_key=c['SDK_ARTIFACT_SECRET_ACCESS_KEY'])
bucket=c['SDK_ARTIFACT_BUCKET'];prefix='pro2-examples/dev/';release=a.release_dir.resolve();manifest=json.loads((release/'publish/manifest.json').read_text());version=manifest['version']
import re
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,40}',version) or manifest.get('test_only') is not True:raise ValueError('Not an isolated evaluation release')
expected={x['filename'] for x in manifest['artifacts']}|{'manifest.json'}
if {f.name for f in (release/'publish').iterdir()}!=expected:raise ValueError('Unexpected files in publication directory')
for artifact in manifest['artifacts']:
 f=release/'publish'/artifact['filename']
 if f.parent!=release/'publish' or f.is_symlink() or len(f.read_bytes())!=artifact['size_bytes'] or hashlib.sha256(f.read_bytes()).hexdigest()!=artifact['sha256']:raise ValueError('Artifact validation failed')
for f in sorted((release/'publish').iterdir()):
 b=f.read_bytes();key=prefix+'releases/'+version+'/'+f.name;hash=hashlib.sha256(b).hexdigest()
 try:
  existing=s.get_object(Bucket=bucket,Key=key)['Body'].read()
  if hashlib.sha256(existing).hexdigest()!=hash:raise RuntimeError('immutable version collision: '+f.name)
 except ClientError as e:
  if e.response['Error']['Code'] not in ['NoSuchKey','404']:raise
  content_type='application/json' if f.suffix=='.json' else 'application/octet-stream'
  u=s.generate_presigned_url('put_object',Params={'Bucket':bucket,'Key':key,'ContentType':content_type,'IfNoneMatch':'*'},ExpiresIn=600)
  try: response=requests.put(u,data=b,headers={'Content-Type':content_type,'If-None-Match':'*'},timeout=90)
  except requests.RequestException: raise RuntimeError('Object upload transport failed') from None
  if response.status_code not in [200,201]:raise RuntimeError('upload HTTP '+str(response.status_code))
 assert hashlib.sha256(s.get_object(Bucket=bucket,Key=key)['Body'].read()).hexdigest()==hash
 print('VERIFIED',f.name,len(b))
if a.activate:
 key=prefix+'latest.json';args={}
 try:
  old=s.get_object(Bucket=bucket,Key=key);oldbody=old['Body'].read()
  if json.loads(oldbody).get('version')==version:
   print('ALREADY_ACTIVE',version);raise SystemExit(0)
  if not (release/'previous-latest.json').exists():(release/'previous-latest.json').write_bytes(oldbody)
  args['IfMatch']=old['ETag']
 except ClientError as e:
  if e.response['Error']['Code'] not in ['NoSuchKey','404']:raise
  args['IfNoneMatch']='*'
 s.put_object(Bucket=bucket,Key=key,Body=json.dumps({'version':version}).encode(),ContentType='application/json',**args)
 print('ACTIVATED',version)
else:
 try:rules=s.get_bucket_cors(Bucket=bucket)['CORSRules']
 except ClientError as e:
  if e.response['Error']['Code'] not in ['NoSuchCORSConfiguration','404']:raise
  rules=[]
 if not (release/'previous-cors.json').exists():(release/'previous-cors.json').write_text(json.dumps(rules))
 rule={'ID':'pro2-examples-dev-browser','AllowedOrigins':['https://admin.video-cloud-dev.realtekconnect.com'],'AllowedMethods':['GET','HEAD'],'AllowedHeaders':['*'],'ExposeHeaders':['Content-Length','ETag'],'MaxAgeSeconds':300}
 desired=[r for r in rules if r.get('ID')!=rule['ID']]+[rule]
 if rules!=desired:s.put_bucket_cors(Bucket=bucket,CORSConfiguration={'CORSRules':desired})
 print('CORS_READY; latest remains unchanged')
