package userapi

// Harness ids in the user API are PAX agent types, the agent_type of
// registered agents (for example claude-code, codex, kimi). paxd's native
// login and usage protocol names Claude Code "claude", so Manager translates
// before querying the node and reports the id the caller requested.

const maxHarnessIDBytes = 32

func validHarnessID(id string) bool {
	if id == "" || len(id) > maxHarnessIDBytes {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// nativeHarness returns the harness name paxd's harness_auth and
// harness_usage protocol uses. "claude" stays accepted as a legacy alias.
func nativeHarness(id string) string {
	if id == "claude-code" {
		return "claude"
	}
	return id
}

// nativeLoginHarness reports whether paxd versions with harness_auth support
// native login for the harness, so invalid_argument means a different problem.
func nativeLoginHarness(native string) bool {
	return native == "claude" || native == "codex"
}

func harnessFallbackLabel(native string) string {
	switch native {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	}
	return ""
}
