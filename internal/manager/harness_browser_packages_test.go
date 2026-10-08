//go:build !windows

package manager

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Only npm and the downloaded packages are fixtures. The real paxl stages,
// verifies, switches and restores these executable installations.
func writeHarnessBrowserPackages(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "initial", "bin")
	require.NoError(t, os.MkdirAll(bin, 0755))
	script := `#!/usr/bin/env python3
import json, pathlib, sys
prefix = pathlib.Path(sys.argv[sys.argv.index('--prefix') + 1])
name, version = sys.argv[-1].rsplit('@', 1)
bins = {'@anthropic-ai/claude-code':'claude', '@openai/codex':'codex', '@agentclientprotocol/claude-agent-acp':'claude-agent-acp', '@agentclientprotocol/codex-acp':'codex-acp', '@ccgv2/pi-acp':'pi-acp', '@earendil-works/pi-coding-agent':'pi'}
binary = bins[name]
pkg = prefix / 'lib' / 'node_modules' / name
(pkg / 'bin').mkdir(parents=True, exist_ok=True)
(prefix / 'bin').mkdir(parents=True, exist_ok=True)
(pkg / 'package.json').write_text(json.dumps({'name':name,'version':version,'bin':{binary:'bin/entry.js'}}))
source = '''#!/usr/bin/env node
const version = VERSION;
const name = NAME;
if (process.argv.includes('--version')) { console.log(name === '@openai/codex' ? 'codex-cli '+version : version); process.exit(0); }
const nativeKey = name.includes('codex-acp') ? 'CODEX_PATH' : name.includes('claude-agent-acp') ? 'CLAUDE_CODE_EXECUTABLE' : undefined;
let runtime = {name:'pi',version:'9.0.0'};
if (nativeKey) {
  const executable = process.env[nativeKey];
  if (!executable || !require('node:path').isAbsolute(executable)) process.exit(20);
  const result = require('node:child_process').spawnSync(executable, ['--version'], {encoding:'utf8'});
  if (result.status !== 0) process.exit(21);
  const observed = result.stdout.trim().split(' ').pop();
  runtime = {name: nativeKey === 'CODEX_PATH' ? 'codex' : 'claude-code', version: observed === '1.2.4' ? '0.0.0' : observed};
}
const rl = require('node:readline').createInterface({input:process.stdin});
rl.on('line', line => {
  const req = JSON.parse(line);
  if (req.id === undefined) return;
  const result = req.method === 'initialize' ? {protocolVersion:1,agentCapabilities:{loadSession:true},agentInfo:{name,version:version === '1.2.4' ? '0.0.0' : version},authMethods:[],_meta:{pax:{runtime}}} : {};
  process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:req.id,result})+'\\n');
});
'''.replace('VERSION', json.dumps(version)).replace('NAME', json.dumps(name))
entry = pkg / 'bin' / 'entry.js'
entry.write_text(source)
entry.chmod(0o755)
(prefix / 'bin' / binary).symlink_to(entry)
`
	npm := filepath.Join(bin, "npm")
	require.NoError(t, os.WriteFile(npm, []byte(script), 0755))
	for _, name := range []string{"@anthropic-ai/claude-code", "@openai/codex", "@agentclientprotocol/claude-agent-acp", "@agentclientprotocol/codex-acp", "@ccgv2/pi-acp", "@earendil-works/pi-coding-agent"} {
		cmd := exec.Command(
			npm,
			"install",
			"--global",
			"--prefix",
			filepath.Join(root, "initial"),
			"--",
			name+"@1.2.2",
		)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
}
