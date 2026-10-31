"""Synthetic GitHub service for CLI acceptance; never makes network requests."""
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import zipfile

root=Path(os.environ['GHENV_TEST_STATE'])
mode=os.environ.get('GHENV_TEST_MODE','success')
args=sys.argv[1:]
if args[:2]==['auth','status']:
    sys.exit(1 if mode=='auth-required' else 0)
index=args.index('--method')
method,path=args[index+1:index+3]
with (root/'calls.jsonl').open('a') as log: log.write(json.dumps([method,path])+'\n')
if mode.startswith('error-'):
    print('synthetic-private-sentinel HTTP '+mode.removeprefix('error-'),file=sys.stderr)
    sys.exit(1)
if mode=='oversized':
    sys.stdout.buffer.write(b'x'*((4<<20)+1))
    sys.exit(0)
if mode=='hang': time.sleep(30)
if path.endswith('/workflows/ghenv.yml'):
    print(json.dumps(dict(state='disabled' if mode=='disabled' else 'active')))
elif method=='POST':
    request=json.load(sys.stdin)
    assert request['return_run_details'] is True
    assert request['ref']=='main'
    inputs=request['inputs']
    assert inputs['environment']=='Preview'
    assert len(inputs['request_id'])==32
    (root/'request.json').write_text(json.dumps(inputs))
    print(json.dumps(dict(workflow_run_id=42)))
elif method=='DELETE':
    (root/'deleted').touch()
elif path.endswith('/runs/42'):
    polls=root/'polled'
    status='completed'
    if mode in ('queued','waiting') and not polls.exists(): status=mode
    polls.touch()
    print(json.dumps(dict(status=status,conclusion='failure' if mode=='failed-run' else 'success')))
elif '/runs/42/artifacts' in path:
    inputs=json.loads((root/'request.json').read_text())
    artifacts=[] if mode=='missing-artifact' else [dict(id=7,name='ghenv-'+inputs['request_id'],expired=mode=='expired')]
    print(json.dumps(dict(artifacts=artifacts)))
elif path.endswith('/artifacts/7/zip'):
    if mode=='download-hang':
        (root/'download-started').touch()
        time.sleep(30)
    if mode=='download-failed': sys.exit(1)
    inputs=json.loads((root/'request.json').read_text())
    secrets=dict(TOKEN="synthetic line\n'quoted' $()=😀")
    variables=dict(URL='http://localhost:3000')
    requested_secrets='TOKEN'
    requested_variables='URL'
    if mode=='extra-name': secrets['EXTRA']='do not export';requested_secrets+=',EXTRA'
    if mode=='missing-name': requested_variables=''
    env=dict(os.environ,GHENV_PREVIEW_BUNDLE=json.dumps(dict(version=1,environment='Preview',repository='owner/repo',secrets=secrets,variables=variables)),GITHUB_REPOSITORY='owner/repo',GHENV_SECRET_NAMES=requested_secrets,GHENV_VAR_NAMES=requested_variables,GHENV_PUBLIC_KEY=inputs['public_key'],GHENV_REQUEST_ID=inputs['request_id'] if mode!='wrong-request' else 'a'*32)
    result=subprocess.run([os.environ['GHENV_TEST_BINARY'],'seal'],env=env,capture_output=True,timeout=10)
    assert result.returncode==0,'synthetic seal failed'
    payload=result.stdout
    if mode=='tampered': payload=b'{"version":"bad","key":"","nonce":"","ciphertext":""}'
    if mode=='invalid-zip': sys.stdout.buffer.write(b'not a ZIP');sys.exit(0)
    output=io.BytesIO()
    with zipfile.ZipFile(output,'w',zipfile.ZIP_DEFLATED) as package:
        package.writestr('../payload.json' if mode=='traversal' else 'payload.json',payload)
        if mode=='duplicate': package.writestr('payload.json',payload)
    sys.stdout.buffer.write(output.getvalue())
else: raise AssertionError('unexpected synthetic request')
