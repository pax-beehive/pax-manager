import json,pathlib,subprocess,tempfile,time

def request(host,path,method='POST',data='{}'):
 with tempfile.TemporaryDirectory(prefix='kev37-') as d:
  h=pathlib.Path(d)/'headers'; b=pathlib.Path(d)/'body'
  args=['curl','--http1.1','--max-time','10','-sS','-D',str(h),'-o',str(b),'-w','%{http_code}','-X',method]
  if method=='POST': args+=['-H','Content-Type: application/json','--data-binary',data]
  r=subprocess.run(args+['https://'+host+path],capture_output=True,text=True,check=True)
  headers={}
  for line in h.read_text().splitlines():
   if ':' in line:
    key,value=line.split(':',1);headers[key.lower()]=value.strip()
  body=b.read_text()
  return {'host':host,'path':path,'status':int(r.stdout),'retry_after':headers.get('retry-after'),'cf_ray':headers.get('cf-ray'),'content_type':headers.get('content-type'),'app_validation': 'hostname is required' in body,'edge_error_1015': '1015' in body,'challenge':headers.get('cf-mitigated')=='challenge'}
results=[]
for host in ['api.lakeward.net','wsapi.lakeward.net']:
 blocked=False
 for i in range(9):
  result=request(host,'/api/v1/node/registration/start');results.append(result);print(json.dumps(result),flush=True)
  if result['status']==429: blocked=True;break
  assert result['status']==400 and result['app_validation'], 'Unexpected response; stop probe'
 assert blocked,'No edge rejection observed in bounded burst'
 for path,method in [('/api/v1/node/registration/poll','POST'),('/api/v1/agent/tunnel','GET'),('/api/v1/user/self/me','GET')]:
  result=request(host,path,method);results.append(result);print(json.dumps(result),flush=True)
  assert result['status'] in [400,401,302,404] and not result['challenge']
 time.sleep(12)
 result=request(host,'/api/v1/node/registration/start');results.append(result);print(json.dumps(result),flush=True)
 assert result['status']==400 and result['app_validation'],'Registration path did not recover'
 time.sleep(11)
pathlib.Path('/tmp/kev37-edge-results.json').write_text(json.dumps(results,indent=2)+'\n')
