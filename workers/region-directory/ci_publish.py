"""Upload production-config Worker versions; never activate traffic or log bindings."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.request

UUID = r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
CAPABILITIES = ['provision-v1', 'browser-v1', 'paxl-login-v1', 'customer-analytics-v1']

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def request(method, url, headers, body=None):
    data = None if body is None else json.dumps(body, allow_nan=False).encode()
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={**headers, 'Content-Type': 'application/json', 'User-Agent': 'pax-worker-release/1'})
    with urllib.request.build_opener(NoRedirect).open(req, timeout=30) as response:
        raw = response.read(1024 * 1024 + 1)
        if len(raw) > 1024 * 1024:
            raise ValueError('oversized response')
        return json.loads(raw)


def digest(resources):
    value = {'bindings': sorted(resources['bindings'], key=lambda b: b['name']), 'runtime': resources['script_runtime']}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def upload(env, *, http=request, run=subprocess.run, root=Path('.')):
    commit = env['GITHUB_SHA']
    if not re.fullmatch('[0-9a-f]{40}', commit) or env['GITHUB_REPOSITORY'] != 'pax-beehive/pax-manager':
        raise ValueError('invalid source')
    run_id = env['GITHUB_RUN_ID']
    if not re.fullmatch('[0-9]+', run_id):
        raise ValueError('invalid run')
    # Checked-in production config is JSON with trailing commas, no comments.
    config = json.loads(re.sub(r',\s*([}\]])', r'\1', (root / 'wrangler.browser.jsonc').read_text()))
    account, script = config['account_id'], config['name']
    if not re.fullmatch('[0-9a-f]{32}', account) or script != 'pax-region-directory':
        raise ValueError('invalid target')
    base = f'https://api.cloudflare.com/client/v4/accounts/{account}/workers/scripts/{script}'
    def cf(path):
        response = http('GET', base + path, {'Authorization': 'Bearer ' + env['CLOUDFLARE_API_TOKEN']})
        if response.get('success') is not True:
            raise ValueError('Cloudflare request failed')
        return response['result']
    current = cf('/deployments')['deployments'][0]
    if len(current['versions']) != 1 or current['versions'][0]['percentage'] != 100:
        raise ValueError('split deployment requires manual review')
    baseline = cf('/versions/' + current['versions'][0]['version_id'])['resources']
    if not any(b['name'] == 'RELEASE_PROBE_TOKEN' and b['type'] == 'secret_text' for b in baseline['bindings']):
        raise ValueError('release probe secret must be provisioned before enabling CI')
    if not any(b['name'] == 'WORKER_VERSION' for b in baseline['bindings']):
        baseline['bindings'].append({'type': 'version_metadata', 'name': 'WORKER_VERSION'})
    result = run(['npx', '--no-install', 'wrangler', 'versions', 'upload', '--config', 'wrangler.browser.jsonc',
                  '--keep-vars', '--tag', commit[:12], '--message', 'commit:' + commit],
                 cwd=root, env=dict(env, WRANGLER_SEND_METRICS='false'), capture_output=True, text=True, timeout=300, check=True)
    match = re.search(r'Worker Version ID:\s*(' + UUID + r')', result.stdout)
    if not match:
        raise ValueError('missing uploaded version ID')
    version = match[1]
    candidate = cf('/versions/' + version)
    if candidate.get('id') != version or candidate.get('annotations', {}).get('workers/message') != 'commit:' + commit:
        raise ValueError('version source mismatch')
    if digest(candidate['resources']) != digest(baseline):
        raise ValueError('bindings or runtime changed; uploaded version remains inactive')
    if cf('/deployments')['deployments'][0]['id'] != current['id']:
        raise ValueError('production changed during upload; review before registration')
    manifest = dict(product='region-directory', kind='worker', platform='cloudflare', commit=commit,
                    reference=f'cloudflare://{account}/{script}/{version}', version_id=version,
                    config_sha256=digest(candidate['resources']), requires=CAPABILITIES,
                    ci_url=f'https://github.com/pax-beehive/pax-manager/actions/runs/{run_id}')
    output = root / 'release-artifact' / 'worker.json'
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(manifest, indent=2) + '\n')
    return manifest


def register(env, *, http=request, root=Path('.')):
    manifest = json.loads((root / 'release-artifact' / 'worker.json').read_text())
    headers = {'Authorization': 'Bearer ' + env['PAX_RELEASE_PUBLISHER_TOKEN']}
    client_id, secret = env.get('PAX_RELEASE_CF_CLIENT_ID'), env.get('PAX_RELEASE_CF_CLIENT_SECRET')
    if bool(client_id) != bool(secret):
        raise ValueError('both Access credentials are required')
    if client_id:
        headers.update({'CF-Access-Client-Id': client_id, 'CF-Access-Client-Secret': secret})
    result = http('POST', 'https://release.paxworkspace.net/api/v1/artifacts', headers, manifest)
    if type(result.get('id')) is not int or result['id'] <= 0 or any(result.get(k) != v for k, v in manifest.items()):
        raise ValueError('registration not confirmed')
    return result['id']


def main(args, env):
    try:
        if args == ['upload']:
            upload(env)
            print('Worker version uploaded and verified; traffic unchanged.')
        elif args == ['register']:
            register(env)
            print('Worker version registered in PAX Release.')
        else:
            raise ValueError('invalid command')
        return 0
    except Exception:
        print('Worker publication failed. No traffic activation was requested. Check configuration or retry the saved manifest.', file=sys.stderr)
        return 1

if __name__ == '__main__':
    sys.exit(main(sys.argv[1:], os.environ))
