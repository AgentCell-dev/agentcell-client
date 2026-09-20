package main

import (
	"strings"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

// TestUnauthenticatedHintPrependsLoginOnlyWhenNoTokenStored: fix for the "unauthenticated" hint
// that used to lead with the machine alternative ("set AGENTCELL_TOKEN or write the token file")
// even for the common case of a person who never ran `agentcell login`. The prepend must fire only
// when noTokenStored is true, and it must never drop the original hint text.
func TestUnauthenticatedHintPrependsLoginOnlyWhenNoTokenStored(t *testing.T) {
	original := &contract.APIError{Code: contract.CodeUnauthenticated, Message: "AgentCell token is unavailable", Hint: "set AGENTCELL_TOKEN or write the token file in the platform config directory"}

	got := unauthenticatedHint(original, true)
	if !strings.HasPrefix(got.Hint, "Not logged in: run `agentcell login`") {
		t.Fatalf("hint does not lead with the login line: %q", got.Hint)
	}
	if !strings.Contains(got.Hint, "set AGENTCELL_TOKEN or write the token file in the platform config directory") {
		t.Fatalf("hint dropped the server/machine text: %q", got.Hint)
	}

	unchanged := unauthenticatedHint(original, false)
	if unchanged.Hint != original.Hint {
		t.Fatalf("hint changed when a token IS stored: %q", unchanged.Hint)
	}

	other := &contract.APIError{Code: contract.CodeForbidden, Message: "no", Hint: "nope"}
	if got := unauthenticatedHint(other, true); got.Hint != "nope" {
		t.Fatalf("a non-unauthenticated code must be untouched: %q", got.Hint)
	}
}
