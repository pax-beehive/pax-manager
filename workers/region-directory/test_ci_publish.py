"""BDD: upload cannot activate traffic or erase production configuration."""
import copy
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch
from ci_publish import upload, register, digest, main, request, NoRedirect
VERSION='11111111-1111-4111-8111-111111111111'
OLD='22222222-2222-4222-8222-222222222222'
class PublishTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
  self.root.joinpath('wrangler.browser.jsonc').write_text(Path('wrangler.browser.jsonc').read_text())
  self.env=dict(GITHUB_SHA='a'*40,GITHUB_REPOSITORY='pax-beehive/pax-manager',GITHUB_RUN_ID='123',CLOUDFLARE_API_TOKEN='private',PAX_RELEASE_PUBLISHER_TOKEN='publisher')
  self.resources={'bindings':[{'name':'DB','type':'d1','id':'db'},{'name':'RELEASE_PROBE_TOKEN','type':'secret_text'}],'script_runtime':{'compatibility_date':'2026-07-01'}}
  self.candidate=copy.deepcopy(self.resources);self.candidate['bindings'].append({'name':'WORKER_VERSION','type':'version_metadata'})
  self.calls=[];self.run=Mock(return_value=Mock(stdout='Worker Version ID: '+VERSION))
 def http(self,method,url,headers,body=None):
  self.calls.append((method,url,body))
  if url.endswith('/artifacts'):return dict(body,id=1)
  if url.endswith('/deployments'):result={'deployments':[{'id':'active','versions':[{'version_id':OLD,'percentage':100}]}]}
  elif url.endswith('/'+OLD):result={'resources':copy.deepcopy(self.resources)}
  else:result={'id':VERSION,'annotations':{'workers/message':'commit:'+'a'*40},'resources':self.candidate}
  return {'success':True,'result':result}
 def test_given_upload_when_registered_then_immutable_manifest_and_no_deploy_call(self):
  manifest=upload(self.env,http=self.http,run=self.run,root=self.root)
  self.assertEqual(VERSION,manifest['version_id']);self.assertEqual(digest(self.candidate),manifest['config_sha256'])
  self.assertTrue(all(c[0]=='GET' for c in self.calls));self.assertIn('--keep-vars',self.run.call_args.args[0]);self.assertNotIn('deploy',self.run.call_args.args[0])
  self.assertEqual(1,register(self.env,http=self.http,root=self.root));self.assertEqual('POST',self.calls[-1][0])
 def test_given_binding_drift_when_uploaded_then_not_registered(self):
  self.candidate['bindings'][0]['id']='other'
  with self.assertRaisesRegex(ValueError,'bindings'):upload(self.env,http=self.http,run=self.run,root=self.root)
  self.assertFalse((self.root/'release-artifact/worker.json').exists())
 def test_given_missing_probe_secret_then_upload_never_runs(self):
  self.resources['bindings']=[]
  with self.assertRaisesRegex(ValueError,'probe'):upload(self.env,http=self.http,run=self.run,root=self.root)
  self.run.assert_not_called()
 def test_given_invalid_ci_context_or_upload_output_then_rejected(self):
  for key,value in [('GITHUB_SHA','main'),('GITHUB_REPOSITORY','other/repo'),('GITHUB_RUN_ID','bad')]:
   with self.assertRaises(ValueError):upload(dict(self.env,**{key:value}),http=self.http,run=self.run,root=self.root)
  self.run.return_value.stdout='unexpected'
  with self.assertRaisesRegex(ValueError,'version ID'):upload(self.env,http=self.http,run=self.run,root=self.root)
 def test_given_access_credentials_then_forward_only_on_registration(self):
  upload(self.env,http=self.http,run=self.run,root=self.root)
  env=dict(self.env,PAX_RELEASE_CF_CLIENT_ID='id')
  with self.assertRaises(ValueError):register(env,http=self.http,root=self.root)
  http=Mock(side_effect=self.http);register(dict(env,PAX_RELEASE_CF_CLIENT_SECRET='secret'),http=http,root=self.root)
  self.assertEqual('secret',http.call_args.args[2]['CF-Access-Client-Secret'])
  with self.assertRaises(ValueError):register(self.env,http=lambda *a:{'id':1},root=self.root)
 def test_given_changed_deployment_or_commit_then_reject_registration(self):
  for mode in ['commit','deployment','split','api']:
   def http(*args):
    result=self.http(*args)
    if mode=='api':result['success']=False
    if args[1].endswith('/'+VERSION) and mode=='commit':result['result']['annotations']={}
    if args[1].endswith('/deployments'):
     if mode=='split':result['result']['deployments'][0]['versions'][0]['percentage']=50
     if mode=='deployment' and self.run.called:result['result']['deployments'][0]['id']='changed'
    return result
   self.run.reset_mock()
   with self.subTest(mode=mode),self.assertRaises(ValueError):upload(self.env,http=http,run=self.run,root=self.root)
 def test_given_cli_failure_then_no_private_exception_is_printed(self):
  with patch('ci_publish.upload',side_effect=ValueError('secret body')),patch('sys.stderr',new_callable=io.StringIO) as output:
   self.assertEqual(1,main(['upload'],self.env));self.assertNotIn('secret body',output.getvalue())
  with patch('ci_publish.upload'),patch('ci_publish.register'),patch('sys.stdout',new_callable=io.StringIO):
   self.assertEqual(0,main(['upload'],self.env));self.assertEqual(0,main(['register'],self.env))
 def test_given_json_transport_then_bounded_response_and_no_redirect(self):
  response=Mock();response.__enter__=Mock(return_value=response);response.__exit__=Mock(return_value=False);response.read.return_value=b'{"ok":true}'
  with patch('urllib.request.build_opener') as opener:
   opener.return_value.open.return_value=response
   self.assertEqual({'ok':True},request('POST','https://example.com',{},{}))
   response.read.return_value=b'x'*(1024*1024+1)
   with self.assertRaises(ValueError):request('GET','https://example.com',{})
  self.assertIsNone(NoRedirect().redirect_request())
if __name__=='__main__':unittest.main()
