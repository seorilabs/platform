#!/usr/bin/env python3
"""Prepare/issue one reviewed mailbox payload. OIDC token is read from a file, never printed."""
import argparse
import json
import os
import urllib.request
from pathlib import Path

p=argparse.ArgumentParser()
p.add_argument('--base-url',required=True)
p.add_argument('--app-id',required=True)
p.add_argument('--payload',type=Path,required=True)
p.add_argument('--execute',action='store_true')
p.add_argument('--token-file',type=Path)
a=p.parse_args()
body=json.loads(a.payload.read_text())
expected='ISSUE INBOX '+a.app_id+' '+body['platformUserId']
if body.get('confirmation')!=expected: p.error('confirmation does not match target')
if not a.base_url.startswith('https://') and not a.base_url.startswith('http://127.0.0.1:'):p.error('HTTPS or local emulator only')
if not a.execute:
 print(json.dumps({'prepared':True,'appId':a.app_id,'requestId':body['requestId'],'rewards':body['rewards'],'expiresAt':body.get('expiresAt',0)},ensure_ascii=False))
else:
 if not a.token_file: p.error('--token-file is required for execution')
 token=a.token_file.read_text().strip()
 req=urllib.request.Request(a.base_url.rstrip('/')+'/v1/admin/apps/'+a.app_id+'/inbox',data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
 try:
  with urllib.request.urlopen(req,timeout=30) as response:
   result=json.load(response)
  print(json.dumps({'ok':result.get('ok'),'id':result.get('result',{}).get('id')}))
 except Exception:
  raise SystemExit('Request failed. Read back state before retry; token and payload are omitted.')
