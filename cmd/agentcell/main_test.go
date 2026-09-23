package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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

// ---------------------------------------------------------------------------------------------
// AGENTCELL_TOKEN WITHOUT A CONFIG DIRECTORY (infra NEXT.md "Item: the client must honour
// AGENTCELL_TOKEN without a config directory"). The infra onboarding canary's first live run, 23
// September 2026, was a systemd oneshot with the token in its environment and no $HOME, and it was
// refused with `cannot locate token storage` before the token was ever looked at: run() resolved
// the token STORE first, and only commands that write or delete a stored token need one. The
// tests below run the real run() in a child process (TestMain's child branch), because the defect
// is in the ORDER run() does things in, which no unit below run() can see, and because the
// environment each case needs (no HOME, no XDG_CONFIG_HOME) must not leak into the parent.
//
// THE TOKEN IS A FIXED FAKE, AND IT IS STILL NEVER PRINTED: every failure message below goes
// through redacted(), so a regression that leaks the token into an error does not also leak it
// into the test log -- the same discipline the client holds in production.
// ---------------------------------------------------------------------------------------------

const (
	childEnv     = "AGENTCELL_MAIN_TEST_CHILD"
	childArgsEnv = "AGENTCELL_MAIN_TEST_ARGS"
	argSeparator = "\x1f"
	fakeToken    = "act_wp62test_" + "0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Args = append([]string{"agentcell"}, strings.Split(os.Getenv(childArgsEnv), argSeparator)...)
		os.Exit(run())
	}
	os.Exit(m.Run())
}

type childResult struct {
	exit           int
	stdout, stderr string
}

// runChild runs `agentcell <args...>` in a child process whose environment has NO HOME, NO
// XDG_CONFIG_HOME and no AGENTCELL_* variable inherited from the parent, plus exactly the extra
// variables given. Windows locates its config directory through other variables entirely, and the
// defect this file holds is the Unix one, so the tests skip there rather than assert something
// that does not apply.
func runChild(t *testing.T, extra map[string]string, args ...string) childResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the config directory on Windows is not located through HOME")
	}
	env := []string{childEnv + "=1", childArgsEnv + "=" + strings.Join(args, argSeparator)}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || name == "XDG_CONFIG_HOME" || strings.HasPrefix(name, "AGENTCELL_") {
			continue
		}
		env = append(env, kv)
	}
	for name, value := range extra {
		env = append(env, name+"="+value)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := childResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.exit = exitErr.ExitCode()
	default:
		t.Fatalf("could not run the child: %v", err)
	}
	return result
}

func redacted(s string) string { return contract.Redact(s, fakeToken) }

// assertNoToken is the negative control on the token: it holds for every refusal below, and
// TestAssertNoTokenCatchesALeak proves it is a check that can fail.
func assertNoToken(t *testing.T, r childResult) {
	t.Helper()
	if leaked(r) {
		t.Fatalf("the token value appears in the output: stdout=%q stderr=%q", redacted(r.stdout), redacted(r.stderr))
	}
}

func leaked(r childResult) bool {
	return strings.Contains(r.stdout, fakeToken) || strings.Contains(r.stderr, fakeToken)
}

func TestAssertNoTokenCatchesALeak(t *testing.T) {
	if !leaked(childResult{stderr: `{"hint":"` + fakeToken + `"}`}) {
		t.Fatal("leaked() did not see a token planted in stderr; assertNoToken would pass a leak")
	}
	if leaked(childResult{stderr: `{"hint":"[REDACTED]"}`}) {
		t.Fatal("leaked() fired on text with no token in it")
	}
}

func decodeStartupError(t *testing.T, r childResult) contract.APIError {
	t.Helper()
	var apiErr contract.APIError
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stderr)), &apiErr); err != nil {
		t.Fatalf("stderr is not one typed error (exit %d): %q", r.exit, redacted(r.stderr))
	}
	return apiErr
}

// authServer answers /v1/auth/whoami and /v1/auth/logout, and records whether each request
// carried exactly the fake token as its bearer -- a boolean, so nothing below ever holds or prints
// the header's value.
func authServer(t *testing.T) (url string, bearerOK func() bool, logoutCalls func() int) {
	t.Helper()
	var mu sync.Mutex
	sawRightBearer, logouts := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":"unauthenticated","message":"token refused","hint":"test server"}`)
			return
		}
		sawRightBearer = true
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/whoami":
			fmt.Fprint(w, `{"email":"wp62@example.test","org_id":"org_wp62","org_kind":"partner","plan":"design-partner","scopes":["read"],"issued_via":"operator","token_prefix":"act_wp62test"}`)
		case "/v1/auth/logout":
			logouts++
			fmt.Fprint(w, `{"revoked":true,"token_prefix":"act_wp62test"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":"not_found","message":"no route","hint":"test server"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL,
		func() bool { mu.Lock(); defer mu.Unlock(); return sawRightBearer },
		func() int { mu.Lock(); defer mu.Unlock(); return logouts }
}

// The defect itself: a token-only command with the token in the environment and no HOME.
func TestWhoamiWithEnvironmentTokenNeedsNoConfigDirectory(t *testing.T) {
	url, bearerOK, _ := authServer(t)
	r := runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken, "AGENTCELL_API_URL": url}, "whoami", "--output=json")
	assertNoToken(t, r)
	if r.exit != 0 || !bearerOK() {
		t.Fatalf("whoami with AGENTCELL_TOKEN and no HOME: exit %d, server saw the token: %v, stderr=%q", r.exit, bearerOK(), redacted(r.stderr))
	}
	if !strings.Contains(r.stdout, `"org_id":"org_wp62"`) {
		t.Fatalf("whoami did not print the server's answer: %q", redacted(r.stdout))
	}
}

// Whitespace is not a token (Resolver.Load trims): a blank AGENTCELL_TOKEN is the no-token case,
// and must not be taken as a reason to skip the store.
func TestBlankEnvironmentTokenIsNoToken(t *testing.T) {
	r := runChild(t, map[string]string{"AGENTCELL_TOKEN": "  ", "HOME": t.TempDir()}, "whoami")
	if got := decodeStartupError(t, r); got.Message != "AgentCell token is unavailable" {
		t.Fatalf("blank AGENTCELL_TOKEN: got %+v", got)
	}
}

// Without the token, the same command is "not logged in" -- and byte-for-byte the same refusal
// whether there is no config directory at all or an empty one (the positive control: the
// existing, tested message for the empty-store case is what the no-store case now also says).
func TestWhoamiWithoutTokenOrConfigDirectoryIsNotLoggedIn(t *testing.T) {
	url, _, _ := authServer(t)
	noHome := runChild(t, map[string]string{"AGENTCELL_API_URL": url}, "whoami")
	emptyHome := runChild(t, map[string]string{"AGENTCELL_API_URL": url, "HOME": t.TempDir()}, "whoami")
	if emptyHome.exit != contract.ExitCode(contract.CodeUnauthenticated) {
		t.Fatalf("positive control: whoami with an empty HOME: exit %d, stderr=%q", emptyHome.exit, emptyHome.stderr)
	}
	if noHome.exit != emptyHome.exit || noHome.stderr != emptyHome.stderr {
		t.Fatalf("no HOME must refuse exactly as an empty store does:\n no HOME (exit %d): %q\n empty   (exit %d): %q", noHome.exit, noHome.stderr, emptyHome.exit, emptyHome.stderr)
	}
	if got := decodeStartupError(t, noHome); !strings.HasPrefix(got.Hint, "Not logged in: run `agentcell login`") {
		t.Fatalf("not the not-logged-in shape: %+v", got)
	}
}

const storeRepairs = "set HOME, or use AGENTCELL_TOKEN for a read-only session"

func assertStoreRefusal(t *testing.T, r childResult, what string) {
	t.Helper()
	assertNoToken(t, r)
	got := decodeStartupError(t, r)
	if got.Code != contract.CodeUnauthenticated || got.Message != "cannot locate token storage" || !strings.Contains(got.Hint, storeRepairs) {
		t.Fatalf("%s without a config directory must name both repairs (%q): exit %d, %+v", what, storeRepairs, r.exit, got)
	}
}

// login writes the store, so it needs one; the refusal names both ways out. Positive control: with
// HOME set, the same process gets PAST the store and on to login's own flag check (an unknown
// flag, so no browser opens and nothing is contacted).
func TestLoginWithoutConfigDirectoryNamesBothRepairs(t *testing.T) {
	assertStoreRefusal(t, runChild(t, nil, "login"), "login")
	assertStoreRefusal(t, runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken}, "login"), "login with AGENTCELL_TOKEN")
	r := runChild(t, map[string]string{"HOME": t.TempDir()}, "login", "--not-a-flag")
	if got := decodeStartupError(t, r); got.Code != contract.CodeUsage || got.Message != "unknown flag --not-a-flag" {
		t.Fatalf("positive control: login with HOME set should reach its flag check: %+v", got)
	}
}

// logout deletes the store, so it needs one too -- and it refuses BEFORE revoking, so a logout
// that cannot finish does not half-happen. Positive control: with HOME set the same logout
// revokes and succeeds.
func TestLogoutWithoutConfigDirectoryRefusesBeforeRevoking(t *testing.T) {
	url, _, logouts := authServer(t)
	assertStoreRefusal(t, runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken, "AGENTCELL_API_URL": url}, "logout"), "logout")
	if logouts() != 0 {
		t.Fatalf("logout revoked the token server-side (%d calls) and then could not clear the store", logouts())
	}
	r := runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken, "AGENTCELL_API_URL": url, "HOME": t.TempDir()}, "logout")
	assertNoToken(t, r)
	if r.exit != 0 || logouts() != 1 {
		t.Fatalf("positive control: logout with HOME set: exit %d, revokes %d, stderr=%q", r.exit, logouts(), redacted(r.stderr))
	}
}

// `auth token` writes the store as login does, and is refused the same way.
func TestAuthTokenWithoutConfigDirectoryNamesBothRepairs(t *testing.T) {
	assertStoreRefusal(t, runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken}, "auth", "token"), "auth token")
}

// A token the SERVER refuses, with no HOME, still never echoes the token back.
func TestRefusedEnvironmentTokenIsNeverEchoed(t *testing.T) {
	url, _, _ := authServer(t)
	r := runChild(t, map[string]string{"AGENTCELL_TOKEN": fakeToken + "x", "AGENTCELL_API_URL": url}, "whoami")
	if r.exit != contract.ExitCode(contract.CodeUnauthenticated) {
		t.Fatalf("a refused token should exit unauthenticated: exit %d, stderr=%q", r.exit, redacted(r.stderr))
	}
	assertNoToken(t, r)
}
