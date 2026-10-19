package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeAuthBrowserFixtures(t *testing.T, root string) {
	t.Helper()
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte(body), mode))
	}
	write(filepath.Join(root, "initial", "bin", "claude"), `#!/bin/sh
[ "$1 $2" = 'auth login' ] || exit 43
printf 'launch\n' >> "$HOME/claude-launches"
printf '%s\n' 'https://example.invalid/native-authorization'
IFS= read -r code
[ "$code" = 'fixture-private-code' ] || exit 44
`, 0700)
	write(
		filepath.Join(root, "initial", "bin", "codex"),
		"#!/bin/sh\n[ \"$1 $2\" = 'login --device-auth' ] || exit 45\nprintf '%s\\n' 'https://example.invalid/device Enter code ABCD-EFGH'\nsleep 60\n",
		0700,
	)
	write(
		filepath.Join(root, "pi-sdk", "package.json"),
		`{"name":"@earendil-works/pi-coding-agent","type":"module","version":"1.3.0","bin":{"pi":"dist/cli.js"}}`,
		0600,
	)
	write(filepath.Join(root, "pi-sdk", "dist", "cli.js"), "#!/usr/bin/env node\n", 0700)
	write(filepath.Join(root, "pi-sdk", "dist", "index.js"), `
import {writeFile} from 'node:fs/promises';
export const VERSION='1.3.0';
const providers=[{id:'example',name:'Fixture provider',auth:{oauth:{login(){}},apiKey:{name:'API key',login(){}}}}];
export class ModelRuntime {
 static async create(){return new ModelRuntime();}
 getProviders(){return providers;}
 async listCredentials(){return [];}
 async login(provider,method,interaction){
  if(provider!=='example'||method!=='api_key')throw Error('Unexpected selection');
  const key=await interaction.prompt({type:'secret',message:'Fixture API key'});
  await writeFile(new URL('../stored-test-key',import.meta.url),key);
  interaction.notify({type:'progress',message:'Saved '+key});
 }
}
`, 0600)
}
