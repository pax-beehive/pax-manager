"""Execute workflow shell with a fake curl; never contact a registry or Release."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


def step_script(name):
    lines = (Path(__file__).resolve().parents[1] / 'workflows/publish-image.yml').read_text().splitlines()
    start = lines.index('      - name: ' + name)
    for i in range(start + 1, len(lines)):
        if lines[i].startswith('        run:'):
            value = lines[i].split('run:', 1)[1].strip()
            if value != '|':
                return value
            body = []
            for line in lines[i + 1:]:
                if line and not line.startswith('          '):
                    break
                body.append(line)
            return textwrap.dedent('\n'.join(body))
    raise AssertionError('Missing workflow step')


class RegistrationTests(unittest.TestCase):
    def test_given_machine_credentials_when_registering_then_headers_stay_private_and_failures_preserve_record(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'release-artifact').mkdir()
            record = root / 'release-artifact/artifact.json'
            record.write_text(json.dumps(dict(product='console', commit='a'*40, kind='image', platform='linux/amd64', reference='ghcr.io/pax-beehive/fixture@sha256:'+'b'*64)))
            original = record.read_bytes()
            fake = root / 'curl'
            fake.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
assert args[-1] == 'https://release.paxworkspace.net/api/v1/artifacts'
assert not any(a in args for a in ('-L', '--location', '--location-trusted', '-v', '--verbose'))
headers = ''
for i, arg in enumerate(args):
    if arg == '--header' and args[i+1].startswith('@'):
        headers += Path(args[i+1][1:]).read_text()
assert 'Authorization: Bearer '+os.environ['PAX_RELEASE_PUBLISHER_TOKEN'] in headers
for key, name in [('PAX_RELEASE_CF_CLIENT_ID', 'CF-Access-Client-Id'), ('PAX_RELEASE_CF_CLIENT_SECRET', 'CF-Access-Client-Secret')]:
    value = os.environ.get(key, '')
    assert (name+': '+value in headers) if value else (name not in headers)
    assert not value or all(value not in arg for arg in args)
case = os.environ['CASE']
status = {'fresh':'201','duplicate':'200','redirect':'302','denied':'403','server_error':'503'}.get(case,'201')
response = dict(json.loads(Path('release-artifact/artifact.json').read_text()), id=7)
if case == 'wrong_digest': response['reference'] = 'wrong'
if case == 'invalid_id': response['id'] = 1.5
Path(args[args.index('--output')+1]).write_text('private-response' if case == 'html' else json.dumps(response))
print(status, end='')
''')
            fake.chmod(0o700)
            env = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'], RUNNER_TEMP=tmp,
                       GITHUB_STEP_SUMMARY=str(root/'summary'), PAX_RELEASE_PUBLISHER_TOKEN='p'*40,
                       PAX_BEEHIVE_READ_TOKEN='g'*40, PAX_RELEASE_CF_CLIENT_ID='', PAX_RELEASE_CF_CLIENT_SECRET='')
            preflight = step_script('Check registration credential')
            register = step_script('Register image in Release')
            for script in (preflight, register):
                subprocess.run(['bash','-n'], input=script, text=True, check=True)
            modes = [('', '', True), ('i'*32+'.access', 's'*64, True), ('i'*32, '', False),
                     ('', 's'*64, False), ('i'*32+'\nInjected: yes', 's'*64, False)]
            for client_id, secret, valid in modes:
                credentials = dict(env, PAX_RELEASE_CF_CLIENT_ID=client_id, PAX_RELEASE_CF_CLIENT_SECRET=secret)
                checked = subprocess.run(['bash','-euo','pipefail','-c',preflight], cwd=root, env=credentials, capture_output=True, text=True)
                self.assertEqual(valid, checked.returncode == 0)
                for value in (client_id, secret, env['PAX_RELEASE_PUBLISHER_TOKEN']):
                    if value: self.assertNotIn(value, checked.stdout+checked.stderr)
                if not valid:
                    continue
                for case in ('fresh','duplicate','redirect','denied','server_error','wrong_digest','invalid_id','html'):
                    with self.subTest(auth=bool(secret), case=case):
                        result = subprocess.run(['bash','-c',register], cwd=root, env=dict(credentials, CASE=case), capture_output=True, text=True)
                        self.assertEqual(case in ('fresh','duplicate'), result.returncode == 0)
                        for value in (client_id, secret, env['PAX_RELEASE_PUBLISHER_TOKEN'], 'private-response'):
                            if value: self.assertNotIn(value, result.stdout+result.stderr)
                        self.assertEqual(original, record.read_bytes())


if __name__ == '__main__':
    unittest.main()
