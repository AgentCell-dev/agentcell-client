// Package cli's auth.go implements `agentcell login`, `logout` and `whoami` against the control
// plane's /v1/auth/* endpoints (SIGNUP.md §2, §5, §8). It is deliberately separate from
// operations/http.go: those eleven verbs are POST /v1/operations/<verb> with a bearer token this
// code does not yet have when it starts, and none of them opens a browser, binds a loopback
// listener, or polls a device code -- shapes operations.HTTPClient was never built to hold.
//
// THE TOKEN IS NEVER PRINTED, ANYWHERE IN THIS FILE. Every function that returns one puts it in a
// struct field a caller can choose to store; nothing here calls fmt.Print* with a *LoginResult's
// Token, a *pollResult's token, or a bearer header value. That is the property auth_test.go's
// TestLoginNeverPrintsTheToken exists to hold, and the property SIGNUP.md §8.4 names: "the token
// never appears in a URL, a browser history, or a redirect".
package cli

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AgentCell-dev/agentcell-client/contract"
	"github.com/AgentCell-dev/agentcell-client/operations"
)

// DefaultLoginBaseURL is where `agentcell login` sends a person's browser, and where a device
// flow's verification link is rooted, unless AGENTCELL_LOGIN_URL or --login-url says otherwise
// (SIGNUP.md §1: "the login application is ONE static Access application").
const DefaultLoginBaseURL = "https://login.agentcell.cloud"

// loopbackTimeout is the CLI's own budget for the loopback flow (SIGNUP.md §5's browser path):
// ten minutes for a person to pick an identity provider, sign in, and have the browser follow the
// redirect back. The device flow's budget is not a client constant -- it is DEVICE_CODE_TTL_SECONDS,
// returned by the server as `expires_in`, because that window is the server's to own (it is also
// the window the pending row lives for).
const loopbackTimeout = 10 * time.Minute

// authBusyRetries, authBusyMaxWait and authBusyMaxSleep mirror operations/http.go's busyRetries,
// busyMaxWait and busyMaxSleep. They are NOT imported from there -- doWithRetry and its
// constants are unexported, and this file does not own operations/http.go -- so the numbers are
// restated rather than shared, with the same argument recorded there: only a typed
// `service_unavailable` carrying `Retry-After` is retried, bounded, so a busy service is not
// turned into three times as many requests and a session is not held open indefinitely.
const (
	authBusyRetries  = 2
	authBusyMaxWait  = 15 * time.Second
	authBusyMaxSleep = 10 * time.Second
)

// authRefusalHeader mirrors control-plane.py's REFUSAL_REASON_HEADER (named "AgentCell-Refusal"
// there): a typed refusal from /v1/auth/* carries this beside the {code, message, hint} body, one
// of the fixed vocabulary in SIGNUP.md's own service (e.g. `code_not_accepted`). It stays local to
// this file rather than joining contract/, which infra's scripts/contract-pin.py pins byte for
// byte: a header only this login flow reads is not worth moving that pin (and its dated,
// reviewable bump commit in infra) for. AuthError below carries it.
const authRefusalHeader = "AgentCell-Refusal"

// AuthError is a *contract.APIError plus the reason authRefusalHeader carried, when the service
// sent one -- so a caller can branch on it (SIGNUP.md's device path answers a different reason for
// "no confirmation from our own page" than for every other unauthenticated refusal, both of which
// share contract.CodeUnauthenticated). It embeds rather than extends contract.APIError for the
// reason recorded above; code that only wants the wire shape uses its .APIError.
type AuthError struct {
	*contract.APIError
	Reason string
}

func authErr(code contract.ErrorCode, message, hint string) *AuthError {
	return &AuthError{APIError: &contract.APIError{Code: code, Message: message, Hint: hint}}
}

// AuthConfig configures the login/logout/whoami flows. Every field that matters for a test is
// overridable so the six scenarios auth_test.go drives (loopback success, wrong state, exchange
// failure, device pending/ready, 503+Retry-After, logout with a dead token) run against an
// httptest server rather than the real control plane.
type AuthConfig struct {
	APIBaseURL   string // POST /v1/auth/{exchange,device,poll,logout}, GET /v1/auth/whoami
	LoginBaseURL string // GET /v1/auth/start (browser only); verification_uri is joined to this

	HTTPClient *http.Client // nil uses http.DefaultClient

	NoBrowser bool // --no-browser: always use the device path
	IsTTY     bool // only shapes messages; flow selection is BrowserReachable, not the TTY
	// BrowserReachable answers whether the loopback flow can open a browser from here. nil uses
	// defaultBrowserReachable (an opener on PATH, and a display where one is needed). Tests inject.
	BrowserReachable func() bool

	// OpenBrowser is called with the /v1/auth/start URL. nil uses the platform opener. An error
	// (including "no opener on this platform") does not abort the loopback flow -- the URL is
	// printed instead, per "opens the browser (or print the URL when it cannot)".
	OpenBrowser func(string) error

	Stdout, Stderr io.Writer // nil uses io.Discard; cmd/agentcell wires os.Stdout/os.Stderr
}

func (c AuthConfig) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
func (c AuthConfig) out() io.Writer {
	if c.Stdout != nil {
		return c.Stdout
	}
	return io.Discard
}
func (c AuthConfig) err() io.Writer {
	if c.Stderr != nil {
		return c.Stderr
	}
	return io.Discard
}

// LoginResult is what a completed login carries. Token is the one field cmd/agentcell must write
// to the token store and MUST NOT log, print or include in an error -- see the file comment.
type LoginResult struct {
	Token  string
	Email  string
	OrgID  string
	Plan   string
	Scopes []string
}

// LoginMessage is SIGNUP.md §2 step 6's line, verbatim for a design-partner plan and with the
// waitlist hint §6 specifies for a waitlisted one. It never mentions Token.
func LoginMessage(result *LoginResult) string {
	msg := fmt.Sprintf("logged in as %s, org %s, plan %s", result.Email, result.OrgID, result.Plan)
	if result.Plan == "waitlist" {
		msg += " — deploys open when the owner approves this org"
	}
	return msg
}

// WhoamiResult is /v1/auth/whoami's body (control-plane.py's auth_whoami). JSON tags match the
// wire shape (whoamiResponse below) so `agentcell whoami --output=json` -- or the non-TTY default,
// the same way `ps` and every other operation pick json over a terminal (README "Get started") --
// encodes the same field names a caller scripting against this command already expects, rather
// than Go's capitalised defaults.
type WhoamiResult struct {
	Email       string   `json:"email"`
	OrgID       string   `json:"org_id"`
	OrgKind     string   `json:"org_kind"`
	Plan        string   `json:"plan"`
	Scopes      []string `json:"scopes"`
	IssuedVia   string   `json:"issued_via"`
	TokenPrefix string   `json:"token_prefix"`
}

// WhoamiText renders a WhoamiResult for a person. It prints the PREFIX, never a token value --
// there is nothing else it could print, because /v1/auth/whoami's response has no token field at
// all (control-plane.py never re-serves a secret it minted once).
func WhoamiText(w *WhoamiResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "email: %s\n", w.Email)
	fmt.Fprintf(&b, "org: %s (%s)\n", w.OrgID, w.OrgKind)
	if w.Plan == "waitlist" {
		fmt.Fprintf(&b, "plan: %s — deploy is refused until the owner approves this org\n", w.Plan)
	} else {
		fmt.Fprintf(&b, "plan: %s\n", w.Plan)
	}
	fmt.Fprintf(&b, "scopes: %s\n", strings.Join(w.Scopes, ", "))
	fmt.Fprintf(&b, "issued via: %s\n", w.IssuedVia)
	fmt.Fprintf(&b, "token: %s...\n", w.TokenPrefix)
	return b.String()
}

// ---------------------------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------------------------

// Login runs the loopback flow by default, and the device flow when cfg.NoBrowser is set or no
// browser can be opened from this process (SIGNUP.md §5). WHETHER STDIN IS A TERMINAL IS NOT THE
// QUESTION: the first fresh-session test (20 September 2026) ran `agentcell login` from a coding
// agent's shell, which is never a TTY, on a Mac with a browser one `open` away -- and was sent to
// the device path and asked to type a code, which is exactly the step that flow exists to avoid
// when a browser is at hand. The question is whether an opener exists and a display is reachable:
// over SSH with no display, or in a container with no opener, the device path is right; on a
// desktop, whatever is driving the shell, the browser opens and the person only picks an account.
func Login(ctx context.Context, cfg AuthConfig) (*LoginResult, *AuthError) {
	// THE LOGIN BASE NEVER GOES THROUGH doJSON -- the loopback flow only ever OPENS it in a
	// browser (there is no bearer to leak on that particular request, since none exists yet),
	// and the device flow only joins it into a URL it prints. Neither call site is doJSON's
	// per-request check below, so it is checked once, here, before either flow starts: a
	// plaintext login base is still a misconfiguration worth refusing with the same typed hint
	// operations.CheckBaseURL gives every other caller-supplied base in this client.
	if apiErr := checkBase(cfg.LoginBaseURL); apiErr != nil {
		return nil, apiErr
	}
	reachable := cfg.BrowserReachable
	if reachable == nil {
		reachable = defaultBrowserReachable
	}
	if cfg.NoBrowser || !reachable() {
		return loginDevice(ctx, cfg)
	}
	return loginLoopback(ctx, cfg)
}

// loopbackOutcome carries the callback handler's result across the goroutine boundary.
type loopbackOutcome struct {
	result *LoginResult
	apiErr *AuthError
}

// loginLoopback implements SIGNUP.md §2: a random state, a loopback listener, a browser sent to
// `<login base>/v1/auth/start`, and a callback that checks the state before it ever calls
// /v1/auth/exchange.
func loginLoopback(ctx context.Context, cfg AuthConfig) (*LoginResult, *AuthError) {
	state, apiErr := randomState()
	if apiErr != nil {
		return nil, apiErr
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, authErr(contract.CodeService, "could not open a loopback listener", contract.Redact(err.Error(), state))
	}
	port := listener.Addr().(*net.TCPAddr).Port

	loginBase := strings.TrimRight(cfg.LoginBaseURL, "/")
	startURL := fmt.Sprintf("%s/v1/auth/start?state=%s&port=%d", loginBase, url.QueryEscape(state), port)

	outcome := make(chan loopbackOutcome, 1)
	var once sync.Once
	send := func(o loopbackOutcome) { once.Do(func() { outcome <- o }) }

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		gotState := query.Get("state")
		// CONSTANT-TIME, and checked BEFORE anything else on this request -- SIGNUP.md §2 step
		// 5 and the design instruction this package was built against both say so explicitly:
		// "the callback handler checks state with constant-time comparison, then POSTs...". A
		// mismatch here must cost this process nothing: no exchange call, no network round trip
		// to the control plane, nothing but a page telling the browser to go away.
		if len(gotState) != len(state) || !hmac.Equal([]byte(gotState), []byte(state)) {
			writeClosePage(w, http.StatusForbidden, "This sign-in could not be completed: the callback did not carry the state this CLI generated.")
			send(loopbackOutcome{apiErr: authErr(
				contract.CodeUnauthenticated, "the callback state did not match",
				"run agentcell login again; a mismatch here means the redirect did not come from the login this CLI started",
			)})
			return
		}
		code := query.Get("code")
		if code == "" {
			writeClosePage(w, http.StatusBadRequest, "This sign-in could not be completed: no code was returned.")
			send(loopbackOutcome{apiErr: authErr(contract.CodeInvalid, "the callback carried no code", "run agentcell login again")})
			return
		}
		result, apiErr := exchange(r.Context(), cfg, code, state)
		if apiErr != nil {
			writeClosePage(w, http.StatusForbidden, "This sign-in could not be completed. You can close this tab and try again from your terminal.")
			send(loopbackOutcome{apiErr: apiErr})
			return
		}
		writeClosePage(w, http.StatusOK, "You are signed in. You can close this tab.")
		send(loopbackOutcome{result: result})
	})
	server := &http.Server{Handler: mux}
	serverDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serverDone)
	}()
	defer func() {
		server.Close()
		<-serverDone
	}()

	if cfg.OpenBrowser != nil || defaultOpenBrowser != nil {
		open := cfg.OpenBrowser
		if open == nil {
			open = defaultOpenBrowser
		}
		if err := open(startURL); err != nil {
			fmt.Fprintf(cfg.err(), "could not open a browser automatically: %s\nopen this URL to continue signing in:\n%s\n", err, startURL)
		} else {
			fmt.Fprintf(cfg.err(), "opening %s to continue signing in ...\n", startURL)
		}
	} else {
		fmt.Fprintf(cfg.err(), "open this URL to continue signing in:\n%s\n", startURL)
	}

	timer := time.NewTimer(loopbackTimeout)
	defer timer.Stop()
	select {
	case o := <-outcome:
		return o.result, o.apiErr
	case <-timer.C:
		return nil, authErr(contract.CodeService, "timed out waiting for the browser sign-in", "run agentcell login again, or use --no-browser for the device path")
	case <-ctx.Done():
		return nil, authErr(contract.CodeTransport, "cancelled while waiting for the browser sign-in", "run agentcell login again")
	}
}

func writeClosePage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><title>agentcell</title><p>%s</p>", htmlEscape(message))
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")
	return replacer.Replace(s)
}

// randomState is the CLI's own secret, 32 random bytes as SIGNUP.md §2 step 1 specifies, encoded
// so it satisfies the server's _check_state (24-200 chars of [A-Za-z0-9_-]): unpadded base64url
// of 32 bytes is 43 characters from exactly that alphabet.
func randomState() (string, *AuthError) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", authErr(contract.CodeService, "could not generate a login state", err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ---------------------------------------------------------------------------------------------
// Device flow
// ---------------------------------------------------------------------------------------------

type deviceStartResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type pollResponse struct {
	Status      string   `json:"status"`
	Token       string   `json:"token"`
	OrgID       string   `json:"org_id"`
	Email       string   `json:"email"`
	Plan        string   `json:"plan"`
	Scopes      []string `json:"scopes"`
	TokenPrefix string   `json:"token_prefix"`
}

// loginDevice implements SIGNUP.md §5's device path, RFC 8628 shape: a server-issued device_code
// that never enters a URL, a user_code the person types, and a poll at the returned interval.
func loginDevice(ctx context.Context, cfg AuthConfig) (*LoginResult, *AuthError) {
	// SENDS AN EXPLICIT `{}` RATHER THAN NO BODY, belt and braces: the server is being fixed to
	// accept `Content-Length: 0` too, but every other POST in this file and in operations/http.go
	// carries a JSON body, and a device start with none was observed reaching an intermediary
	// that treats a bodyless POST differently from a POST with an empty object. `map[string]any{}`
	// marshals to the literal two bytes `{}`, with Content-Length set from those bytes the normal
	// way encoding/json + doJSON already do for every other call.
	var started deviceStartResponse
	if apiErr := postJSON(ctx, cfg, cfg.APIBaseURL, "/v1/auth/device", map[string]any{}, "", &started); apiErr != nil {
		return nil, apiErr
	}
	loginBase := strings.TrimRight(cfg.LoginBaseURL, "/")
	verificationURL := started.VerificationURI
	if strings.HasPrefix(verificationURL, "/") {
		verificationURL = loginBase + verificationURL
	}
	fmt.Fprintf(cfg.err(), "go to %s and enter this code: %s\n", verificationURL, started.UserCode)
	fmt.Fprintf(cfg.err(), "waiting for you to finish signing in ...\n")

	interval := time.Duration(started.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresIn := time.Duration(started.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = loopbackTimeout
	}
	deadline := time.Now().Add(expiresIn)

	// consecutive401s TOLERATES a single mid-flow race rather than aborting the whole device flow
	// on it. The server is being fixed for the race that produces this (a confirmation landing
	// and a poll reading stale state within the same window), but the client's own tolerance is
	// cheap and belongs here regardless: up to three consecutive polls answering `unauthenticated`
	// are treated as "not ready yet" and retried at the server's own interval, the same as a
	// `pending` status. Any OTHER typed error (code_not_accepted, expired, ...) still aborts
	// immediately -- only unauthenticated is a race symptom -- and a poll that succeeds resets the
	// counter, so a flaky window never accumulates toward the cap across a long-lived poll.
	consecutive401s := 0
	const max401Retries = 3
	for {
		var poll pollResponse
		apiErr := postJSON(ctx, cfg, cfg.APIBaseURL, "/v1/auth/poll", map[string]string{"device_code": started.DeviceCode}, "", &poll)
		if apiErr != nil {
			if apiErr.Code == contract.CodeUnauthenticated && consecutive401s < max401Retries {
				consecutive401s++
			} else {
				return nil, apiErr
			}
		} else {
			consecutive401s = 0
			if poll.Status == "ready" {
				return &LoginResult{Token: poll.Token, Email: poll.Email, OrgID: poll.OrgID, Plan: poll.Plan, Scopes: poll.Scopes}, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, authErr(contract.CodeService, "timed out waiting for the device sign-in", "run agentcell login --no-browser again")
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return nil, authErr(contract.CodeTransport, "cancelled while waiting for the device sign-in", "run agentcell login --no-browser again")
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Exchange, whoami, logout
// ---------------------------------------------------------------------------------------------

type exchangeResponse struct {
	Token       string   `json:"token"`
	OrgID       string   `json:"org_id"`
	Email       string   `json:"email"`
	Plan        string   `json:"plan"`
	Scopes      []string `json:"scopes"`
	TokenPrefix string   `json:"token_prefix"`
}

// exchange is SIGNUP.md §2 step 5: POST {code, state} to <api base>/v1/auth/exchange. Called only
// after the callback's own constant-time state check has passed -- see loginLoopback.
func exchange(ctx context.Context, cfg AuthConfig, code, state string) (*LoginResult, *AuthError) {
	var resp exchangeResponse
	body := map[string]string{"code": code, "state": state}
	if apiErr := postJSON(ctx, cfg, cfg.APIBaseURL, "/v1/auth/exchange", body, "", &resp); apiErr != nil {
		return nil, apiErr
	}
	return &LoginResult{Token: resp.Token, Email: resp.Email, OrgID: resp.OrgID, Plan: resp.Plan, Scopes: resp.Scopes}, nil
}

type whoamiResponse struct {
	Email       string   `json:"email"`
	OrgID       string   `json:"org_id"`
	OrgKind     string   `json:"org_kind"`
	Plan        string   `json:"plan"`
	Scopes      []string `json:"scopes"`
	IssuedVia   string   `json:"issued_via"`
	TokenPrefix string   `json:"token_prefix"`
}

// Whoami reads /v1/auth/whoami with the given bearer.
func Whoami(ctx context.Context, cfg AuthConfig, token string) (*WhoamiResult, *AuthError) {
	var resp whoamiResponse
	if apiErr := getJSON(ctx, cfg, cfg.APIBaseURL, "/v1/auth/whoami", token, &resp); apiErr != nil {
		return nil, apiErr
	}
	return &WhoamiResult{
		Email: resp.Email, OrgID: resp.OrgID, OrgKind: resp.OrgKind, Plan: resp.Plan,
		Scopes: resp.Scopes, IssuedVia: resp.IssuedVia, TokenPrefix: resp.TokenPrefix,
	}, nil
}

type logoutResponse struct {
	Revoked     bool   `json:"revoked"`
	TokenPrefix string `json:"token_prefix"`
}

// Logout revokes the given token server-side. AlreadyDead is true when the server refused because
// the presented token does not work any more (revoked, expired, unknown) -- in that case the
// error is nil, because from the caller's point of view a dead credential and a freshly revoked
// one both end the same way: nothing left to delete locally. Any OTHER failure (transport,
// service_unavailable, ...) is returned as an error and AlreadyDead is false, so the caller does
// not delete a token that might still work.
func Logout(ctx context.Context, cfg AuthConfig, token string) (alreadyDead bool, apiErr *AuthError) {
	var resp logoutResponse
	apiErr = postJSON(ctx, cfg, cfg.APIBaseURL, "/v1/auth/logout", nil, token, &resp)
	if apiErr == nil {
		return false, nil
	}
	if apiErr.Code == contract.CodeUnauthenticated {
		return true, nil
	}
	return false, apiErr
}

// ---------------------------------------------------------------------------------------------
// HTTP plumbing shared by exchange/device/poll/whoami/logout.
// ---------------------------------------------------------------------------------------------

func postJSON(ctx context.Context, cfg AuthConfig, base, path string, body any, bearer string, out any) *AuthError {
	return doJSON(ctx, cfg, http.MethodPost, base, path, body, bearer, out)
}
func getJSON(ctx context.Context, cfg AuthConfig, base, path, bearer string, out any) *AuthError {
	return doJSON(ctx, cfg, http.MethodGet, base, path, nil, bearer, out)
}

// checkBase refuses a caller-supplied base URL the same way operations.CheckBaseURL refuses one
// for the eleven operation verbs: https always allowed, plain http only for a Tailscale overlay
// address or loopback. /v1/auth/exchange and /v1/auth/poll HAND OVER a token in the response body
// and /v1/auth/whoami and /v1/auth/logout PRESENT one as a bearer, so this base is exactly as
// sensitive as operations.HTTPClient's, and it is checked before any request is built -- a
// refusal here costs no socket and touches no network, the same property checkBaseURL's own
// caller relies on.
func checkBase(raw string) *AuthError {
	parsed, err := url.Parse(raw)
	if err != nil {
		return authErr(contract.CodeUsage, "invalid URL", "set AGENTCELL_API_URL/--api-url or AGENTCELL_LOGIN_URL/--login-url to a valid https:// address")
	}
	if err := operations.CheckBaseURL(parsed); err != nil {
		return &AuthError{APIError: contract.AsAPIError(err)}
	}
	return nil
}

// doJSON sends one request and, on a 503 typed `service_unavailable` carrying a valid
// `Retry-After`, retries -- bounded the same way operations/http.go's doWithRetry is bounded. See
// the constants above for why the numbers are restated rather than imported.
func doJSON(ctx context.Context, cfg AuthConfig, method, base, path string, body any, bearer string, out any) *AuthError {
	if apiErr := checkBase(base); apiErr != nil {
		return apiErr
	}
	var encoded []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return authErr(contract.CodeInvalid, "request could not be encoded", "report this client bug")
		}
		encoded = b
	}
	target := strings.TrimRight(base, "/") + path

	var waited time.Duration
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(encoded))
		if err != nil {
			return authErr(contract.CodeTransport, "request could not be created", "check the API URL")
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set(contract.APIVersionHeader, contract.APIVersion)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := cfg.client().Do(req)
		if err != nil {
			return authErr(contract.CodeTransport, contract.Redact("cannot reach AgentCell service: "+err.Error(), bearer), "check the network, the login/API URLs, and the overlay connection")
		}
		apiErr := decodeAuthResponse(resp, bearer, out)
		if apiErr == nil {
			return nil
		}
		if apiErr.Code == contract.CodeService && attempt < authBusyRetries {
			if delay, ok := retryAfter(resp); ok && waited+delay <= authBusyMaxWait {
				waited += delay
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return authErr(contract.CodeTransport, "cancelled while waiting to retry", "the service asked for a retry after "+delay.String())
				case <-timer.C:
				}
				continue
			}
		}
		return apiErr
	}
}

// retryAfter reads the header the same way operations/http.go's busyRetryAfter does: only the
// delta-seconds form, bounded by authBusyMaxSleep.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || seconds < 0 {
		return 0, false
	}
	delay := time.Duration(seconds) * time.Second
	if delay > authBusyMaxSleep {
		return 0, false
	}
	return delay, true
}

// decodeAuthResponse checks the API version the way operations/http.go's decodeError does, then
// either decodes `out` on success or builds an *AuthError -- with Reason populated from
// authRefusalHeader when the service sent one -- on failure. The bearer is redacted out of
// anything that reaches an error message, the same rule http.go's decodeError follows.
func decodeAuthResponse(resp *http.Response, bearer string, out any) *AuthError {
	defer resp.Body.Close()
	serverVersion := resp.Header.Get(contract.APIVersionHeader)
	if serverVersion != "" && serverVersion != contract.APIVersion {
		io.Copy(io.Discard, resp.Body)
		return authErr(
			contract.CodeUnsupportedVersion, "client and service API versions are incompatible",
			fmt.Sprintf("client=%s service=%s; upgrade the older component", contract.APIVersion, serverVersion),
		)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var api contract.APIError
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&api); err != nil || api.Code == "" {
			return authErr(contract.CodeService, fmt.Sprintf("service returned HTTP %d", resp.StatusCode), "retry; if it persists, report the status code")
		}
		api.Message = contract.Redact(api.Message, bearer)
		api.Hint = contract.Redact(api.Hint, bearer)
		return &AuthError{APIError: &api, Reason: resp.Header.Get(authRefusalHeader)}
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return authErr(contract.CodeTransport, "service returned an invalid response", "check client and service API versions")
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// The platform browser opener. Best-effort: if the platform has no known opener, or launching it
// fails, loginLoopback prints the URL instead of aborting -- see its call site.
// ---------------------------------------------------------------------------------------------

// defaultBrowserReachable: the opener this platform uses is on PATH, and -- where a display is a
// separate thing from the machine -- one is reachable. SSH without a forwarded display is the
// common "no" on a desktop OS; a container without xdg-open is the common "no" on Linux.
func defaultBrowserReachable() bool {
	if os.Getenv("SSH_CONNECTION") != "" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" && runtime.GOOS != "darwin" {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("open")
		return err == nil && os.Getenv("SSH_CONNECTION") == ""
	case "windows":
		return true
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
		_, err := exec.LookPath("xdg-open")
		return err == nil
	}
}

var defaultOpenBrowser = func(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("no browser opener (%s) on this platform", name)
	}
	return exec.Command(name, args...).Start()
}
