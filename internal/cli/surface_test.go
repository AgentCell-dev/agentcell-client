package cli

import (
	"strings"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

// TestHelpListsSessionCommands: `agentcell help` used to list only the eleven contract operations
// -- login, logout, whoami, auth token, mcp and version exist but appeared nowhere. This is the
// negative control's positive-fix pair: every one of the six must be named in the listing.
func TestHelpListsSessionCommands(t *testing.T) {
	out := Help(contract.Definitions)
	if !strings.Contains(out, "Getting started / session:") {
		t.Fatalf("help output has no session section:\n%s", out)
	}
	for _, name := range []string{"login", "logout", "whoami", "auth token", "mcp", "version"} {
		if !strings.Contains(out, name) {
			t.Errorf("help output does not mention %q:\n%s", name, out)
		}
	}
}

// TestSessionCommandHelpCoversEverySessionCommand: `agentcell help <name>` for each of the six
// dispatch names (auth, not "auth token") must return per-command usage, not fall through to the
// generic operations listing.
func TestSessionCommandHelpCoversEverySessionCommand(t *testing.T) {
	for _, name := range []string{"login", "logout", "whoami", "auth", "mcp", "version"} {
		usage, ok := SessionCommandHelp(name)
		if !ok {
			t.Errorf("SessionCommandHelp(%q) not found", name)
			continue
		}
		if !strings.HasPrefix(usage, "Usage: agentcell "+name) {
			t.Errorf("SessionCommandHelp(%q) = %q, want it to start with its own usage line", name, usage)
		}
	}
	if _, ok := SessionCommandHelp("deploy"); ok {
		t.Fatal("SessionCommandHelp(\"deploy\") should not exist -- deploy is a contract operation, not a session command")
	}
}
