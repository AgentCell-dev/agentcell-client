package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

const testToken = "act_testprefix_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

// roundTripFunc lets a test answer an HTTP request without a real socket, the same shape
// operations/http_test.go uses (unexported there, so restated rather than imported).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeJSONResponse(status int, value any) *http.Response {
	b, _ := json.Marshal(value)
	header := make(http.Header)
	header.Set(contract.APIVersionHeader, contract.APIVersion)
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(b))}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set(contract.APIVersionHeader, contract.APIVersion)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeRefusal(w http.ResponseWriter, status int, code contract.ErrorCode, reason string) {
	w.Header().Set(contract.APIVersionHeader, contract.APIVersion)
	if reason != "" {
		w.Header().Set(authRefusalHeader, reason)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(contract.APIError{Code: code, Message: "refused", Hint: "see the hint"})
}

// TestLoginLoopbackSuccess drives the loopback flow end to end against a fake control plane: the
// browser hook captures the /v1/auth/start URL (never actually opened), extracts state and port,
// and simulates the browser's redirect to the loopback callback with a matching state.
func TestLoginLoopbackSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Code, State string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Code != "the-code" {
			writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "code_not_accepted")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token": testToken, "org_id": "u-abc123", "email": "partner@example.com",
			"plan": "design-partner", "scopes": []string{"deploy", "read"}, "token_prefix": "act_testprefix",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	opened := make(chan string, 1)
	cfg := AuthConfig{
		APIBaseURL: server.URL, LoginBaseURL: "https://login.example",
		IsTTY: true, Stdout: &stdout, Stderr: &stderr,
		OpenBrowser: func(target string) error { opened <- target; return nil },
	}
	go func() {
		target := <-opened
		u, err := url.Parse(target)
		if err != nil {
			t.Errorf("bad start URL: %v", err)
			return
		}
		port := u.Query().Get("port")
		state := u.Query().Get("state")
		callback := fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&code=the-code", port, url.QueryEscape(state))
		// Give the loopback listener a moment to be Serve()-ing before the "browser" calls it.
		for i := 0; i < 50; i++ {
			if resp, err := http.Get(callback); err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("callback never reached the loopback listener")
	}()

	result, apiErr := Login(context.Background(), cfg)
	if apiErr != nil {
		t.Fatalf("login refused: %+v", apiErr)
	}
	if result.Email != "partner@example.com" || result.OrgID != "u-abc123" || result.Plan != "design-partner" {
		t.Fatalf("result=%+v", result)
	}
	if result.Token != testToken {
		t.Fatalf("token not carried through: %q", result.Token)
	}
}

// TestLoginLoopbackWrongStateRefusedNoExchange is SIGNUP.md §8's negative control for finding 1:
// a callback with the wrong state must be refused BEFORE any call to /v1/auth/exchange, not just
// refused eventually. The exchange handler fails the test if it is ever invoked.
func TestLoginLoopbackWrongStateRefusedNoExchange(t *testing.T) {
	var exchangeCalled int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCalled, 1)
		writeJSON(w, http.StatusOK, map[string]any{"token": testToken, "org_id": "u-x", "email": "x@example.com", "plan": "waitlist"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	opened := make(chan string, 1)
	cfg := AuthConfig{
		APIBaseURL: server.URL, LoginBaseURL: "https://login.example",
		IsTTY: true, OpenBrowser: func(target string) error { opened <- target; return nil },
	}
	go func() {
		target := <-opened
		u, _ := url.Parse(target)
		port := u.Query().Get("port")
		// The attacker's link: a state the CLI never generated.
		callback := fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&code=stolen-code", port, strings.Repeat("z", 32))
		for i := 0; i < 50; i++ {
			if resp, err := http.Get(callback); err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, apiErr := Login(context.Background(), cfg)
	if apiErr == nil || apiErr.Code != contract.CodeUnauthenticated {
		t.Fatalf("wrong state was not refused: %+v", apiErr)
	}
	if got := atomic.LoadInt32(&exchangeCalled); got != 0 {
		t.Fatalf("exchange was called %d times on a state mismatch; must be zero", got)
	}
}

// TestExchangeFailureIsTyped: the control plane refuses the exchange (a burned or expired code),
// and the caller must see a typed APIError, not a generic transport failure.
func TestExchangeFailureIsTyped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "code_not_accepted")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	opened := make(chan string, 1)
	cfg := AuthConfig{
		APIBaseURL: server.URL, LoginBaseURL: "https://login.example",
		IsTTY: true, OpenBrowser: func(target string) error { opened <- target; return nil },
	}
	go func() {
		target := <-opened
		u, _ := url.Parse(target)
		port := u.Query().Get("port")
		state := u.Query().Get("state")
		callback := fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&code=spent-code", port, url.QueryEscape(state))
		for i := 0; i < 50; i++ {
			if resp, err := http.Get(callback); err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, apiErr := Login(context.Background(), cfg)
	if apiErr == nil {
		t.Fatal("expected a typed refusal")
	}
	if apiErr.Code != contract.CodeUnauthenticated {
		t.Fatalf("code=%q", apiErr.Code)
	}
	if apiErr.Reason != "code_not_accepted" {
		t.Fatalf("reason not carried from AgentCell-Refusal: %q", apiErr.Reason)
	}
}

// TestDeviceFlowPendingThenReady covers the RFC 8628 shape end to end: the first poll answers
// pending, the second answers ready with the token.
func TestDeviceFlowPendingThenReady(t *testing.T) {
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deviceStartResponse{
			DeviceCode: "server-issued-device-secret", UserCode: "ABCD1234",
			VerificationURI: "/v1/auth/device/verify", ExpiresIn: 600, Interval: 1,
		})
	})
	mux.HandleFunc("/v1/auth/poll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.DeviceCode != "server-issued-device-secret" {
			writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "code_not_accepted")
			return
		}
		if atomic.AddInt32(&polls, 1) == 1 {
			writeJSON(w, http.StatusOK, pollResponse{Status: "pending"})
			return
		}
		writeJSON(w, http.StatusOK, pollResponse{
			Status: "ready", Token: testToken, OrgID: "u-device1", Email: "dev@example.com",
			Plan: "design-partner", Scopes: []string{"deploy", "read"}, TokenPrefix: "act_testprefix",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var stderr bytes.Buffer
	cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example", NoBrowser: true, Stderr: &stderr}
	result, apiErr := Login(context.Background(), cfg)
	if apiErr != nil {
		t.Fatalf("device login refused: %+v", apiErr)
	}
	if result.OrgID != "u-device1" || result.Token != testToken {
		t.Fatalf("result=%+v", result)
	}
	if got := atomic.LoadInt32(&polls); got != 2 {
		t.Fatalf("polls=%d, want exactly 2 (pending then ready)", got)
	}
	if !strings.Contains(stderr.String(), "ABCD1234") {
		t.Fatalf("user code was not printed: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "server-issued-device-secret") {
		t.Fatal("the device secret must never be printed, only the user code")
	}
}

// TestDeviceStartSendsExplicitEmptyObject: the device start POST must carry the literal two bytes
// `{}`, not an empty body -- belt and braces beside the server's own Content-Length: 0 fix, and
// the same shape every other POST in this file already sends (loginDevice's own comment).
func TestDeviceStartSendsExplicitEmptyObject(t *testing.T) {
	var gotBody []byte
	var gotContentLength int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentLength = r.ContentLength
		writeJSON(w, http.StatusOK, deviceStartResponse{DeviceCode: "d", UserCode: "u", VerificationURI: "/x", ExpiresIn: 30, Interval: 1})
	})
	mux.HandleFunc("/v1/auth/poll", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pollResponse{Status: "ready", Token: testToken, OrgID: "o", Email: "a@b.com", Plan: "p"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example", NoBrowser: true}
	if _, apiErr := Login(context.Background(), cfg); apiErr != nil {
		t.Fatalf("device login refused: %+v", apiErr)
	}
	if string(gotBody) != "{}" {
		t.Fatalf("device start body = %q, want the literal {}", gotBody)
	}
	if gotContentLength != 2 {
		t.Fatalf("device start Content-Length = %d, want 2 (for the two bytes {})", gotContentLength)
	}
}

// deviceStub is a control plane that answers POST /v1/auth/device with `start` (a raw map, so a
// test can send a field deviceStartResponse does not know about, or leave one out entirely, the
// way an older control plane does), answers the first poll `ready`, and RECORDS EVERY REQUEST it
// receives -- path, query and body -- so a test can assert on what the client sent and on what it
// did NOT fetch. Anything that is neither the start nor the poll (a GET of the verify page, which
// is what fetching the complete link would be) is recorded and answered 404, never 200: a client
// that fetched it must not be able to mistake the answer for success.
type deviceStub struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []string // "<METHOD> <path>?<query>" per request, in order
	start  []byte   // the body of the last POST /v1/auth/device
}

func newDeviceStub(t *testing.T, start map[string]any) *deviceStub {
	t.Helper()
	stub := &deviceStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.seen = append(stub.seen, r.Method+" "+r.URL.RequestURI())
		stub.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/device":
			stub.mu.Lock()
			stub.start = body
			stub.mu.Unlock()
			writeJSON(w, http.StatusOK, start)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/poll":
			writeJSON(w, http.StatusOK, pollResponse{Status: "ready", Token: testToken, OrgID: "u-device1", Email: "dev@example.com", Plan: "design-partner"})
		default:
			writeRefusal(w, http.StatusNotFound, contract.CodeNotFound, "")
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *deviceStub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// deviceSecret is the device_code every device-path test below hands out. It is what the poll
// presents and must never reach a terminal (SIGNUP.md §5; DEVICE-FLOW.md §3 "must never show").
const deviceSecret = "server-issued-device-secret-7f3a"

// TestDeviceCompleteLinkPrintedFirstThenCodeThenPlainURL is DEVICE-FLOW.md §2.6: a control plane
// that answers `verification_uri_complete` gets its complete link printed FIRST, on its own line
// (the thing to use), then the user code on its own line (RFC 8628 §3.3.1: still displayed, and
// comparing it with the page is the mitigation), then the plain URL as the fallback for a person
// who cannot follow a link. The whole stderr is compared byte for byte, because the order IS the
// property; a Contains check would pass a client that printed the lines the other way round.
// Observed failing against f67de24, which printed only its one "go to ... and enter this code"
// line and ignored the field.
func TestDeviceCompleteLinkPrintedFirstThenCodeThenPlainURL(t *testing.T) {
	stub := newDeviceStub(t, map[string]any{
		"device_code": deviceSecret, "user_code": "ABCD2345",
		"verification_uri":          "https://login.example/v1/auth/device/verify",
		"verification_uri_complete": "https://login.example/v1/auth/device/verify?user_code=ABCD2345",
		"expires_in":                600, "interval": 1,
	})
	var stdout, stderr bytes.Buffer
	cfg := AuthConfig{APIBaseURL: stub.server.URL, LoginBaseURL: "https://login.example", NoBrowser: true, Stdout: &stdout, Stderr: &stderr}
	if _, apiErr := Login(context.Background(), cfg); apiErr != nil {
		t.Fatalf("device login refused: %+v", apiErr)
	}
	want := "open this link on any device with a browser, sign in, and press Authorise:\n" +
		"  https://login.example/v1/auth/device/verify?user_code=ABCD2345\n" +
		"the page will show this code; approve only if it matches:  ABCD2345\n" +
		"no link? go to https://login.example/v1/auth/device/verify and enter the code\n" +
		"waiting for you to finish signing in ...\n"
	if got := stderr.String(); got != want {
		t.Fatalf("device path printed\n%s\nwant (complete link first, then the code, then the plain URL)\n%s", got, want)
	}
	if strings.Contains(stdout.String()+stderr.String(), deviceSecret) {
		t.Fatal("the device code must never be printed, only the user code")
	}
}

// TestDeviceWithoutCompleteLinkPrintsWhat012Prints is the regression guard for the converge window
// DEVICE-FLOW.md §2.6 names: an older control plane omits `verification_uri_complete`, and the
// client must print EXACTLY what v0.1.2 printed against it -- the field is absent, not
// empty-and-fatal. `want` is the byte string f67de24 (v0.1.2) was observed printing against this
// same stub, not a reconstruction. An empty string is treated the same as absent: a server that
// sends the key with no value has not given the client a link to print.
func TestDeviceWithoutCompleteLinkPrintsWhat012Prints(t *testing.T) {
	for name, start := range map[string]map[string]any{
		"absent": {"device_code": deviceSecret, "user_code": "ABCD1234", "verification_uri": "/v1/auth/device/verify", "expires_in": 600, "interval": 1},
		"empty":  {"device_code": deviceSecret, "user_code": "ABCD1234", "verification_uri": "/v1/auth/device/verify", "verification_uri_complete": "", "expires_in": 600, "interval": 1},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newDeviceStub(t, start)
			var stdout, stderr bytes.Buffer
			cfg := AuthConfig{APIBaseURL: stub.server.URL, LoginBaseURL: "https://login.example", NoBrowser: true, Stdout: &stdout, Stderr: &stderr}
			if _, apiErr := Login(context.Background(), cfg); apiErr != nil {
				t.Fatalf("device login refused: %+v", apiErr)
			}
			want := "go to https://login.example/v1/auth/device/verify and enter this code: ABCD1234\n" +
				"waiting for you to finish signing in ...\n"
			if got := stderr.String(); got != want {
				t.Fatalf("against a control plane without verification_uri_complete the client printed\n%q\nwant v0.1.2's bytes\n%q", got, want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("the device path wrote to stdout: %q", stdout.String())
			}
		})
	}
}

// TestDeviceCompleteLinkIsNeverOpenedOrFetched is DEVICE-FLOW.md §2.6's "the client never opens the
// complete URL itself": the person's click on the platform's page is what authorises, and a
// client that followed the link would be the loopback path with a worse redirect. The login base
// IS the stub, and the complete URI is relative, so the joined link points at the stub: a fetch
// would be recorded there, and both openers (the injected one and the package default) fail the
// test outright if called. BrowserReachable answers true so the only thing keeping this on the
// device path is --no-browser, which must keep its meaning. The positive control is in the same
// test: the joined link IS printed, and the stub DID see exactly the start and one poll.
func TestDeviceCompleteLinkIsNeverOpenedOrFetched(t *testing.T) {
	stub := newDeviceStub(t, map[string]any{
		"device_code": deviceSecret, "user_code": "WXYZ6789",
		"verification_uri":          "/v1/auth/device/verify",
		"verification_uri_complete": "/v1/auth/device/verify?user_code=WXYZ6789",
		"expires_in":                600, "interval": 1,
	})
	savedOpener := defaultOpenBrowser
	defaultOpenBrowser = func(target string) error {
		t.Errorf("the package default browser opener was called with %s on the device path", target)
		return nil
	}
	defer func() { defaultOpenBrowser = savedOpener }()
	var stderr bytes.Buffer
	cfg := AuthConfig{
		APIBaseURL: stub.server.URL, LoginBaseURL: stub.server.URL, NoBrowser: true, Stderr: &stderr,
		BrowserReachable: func() bool { return true },
		OpenBrowser: func(target string) error {
			t.Errorf("the device path opened %s; only the person's own click may", target)
			return nil
		},
	}
	if _, apiErr := Login(context.Background(), cfg); apiErr != nil {
		t.Fatalf("device login refused: %+v", apiErr)
	}
	if !strings.Contains(stderr.String(), "  "+stub.server.URL+"/v1/auth/device/verify?user_code=WXYZ6789\n") {
		t.Fatalf("positive control: the relative complete link was not joined to the login base and printed:\n%s", stderr.String())
	}
	got := stub.requests()
	for _, seen := range got {
		if strings.Contains(seen, "/v1/auth/device/verify") {
			t.Fatalf("the client fetched the verify page itself (%s); requests seen: %v", seen, got)
		}
	}
	if want := []string{"POST /v1/auth/device", "POST /v1/auth/poll"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests seen = %v, want exactly %v", got, want)
	}
	if strings.Contains(stderr.String(), deviceSecret) {
		t.Fatal("the device code must never be printed, only the user code")
	}
}

// TestDeviceStartSendsBoundedClientName is DEVICE-FLOW.md §2.6's start body: `{"client": ...}`
// when the name is within the server's §1.5 bound, and exactly v0.1.2's `{}` when it is not --
// dropped, never truncated, the same rule the server applies. Content-Length must match the bytes
// sent (the framing property TestDeviceStartSendsExplicitEmptyObject was written for: never a
// chunked or bodyless start). The first case is the positive control: the name cmd/agentcell
// actually builds, for a release version, IS sent. Observed failing with loginDevice's body put
// back to f67de24's `map[string]any{}`.
func TestDeviceStartSendsBoundedClientName(t *testing.T) {
	built := DeviceClientName("0.1.3")
	cases := []struct {
		name, client, wantBody string
	}{
		{"release build", built, `{"client":"` + built + `"}`},
		{"dev build", DeviceClientName("dev"), `{"client":"` + DeviceClientName("dev") + `"}`},
		{"64 characters", "a" + strings.Repeat("b", 63), `{"client":"a` + strings.Repeat("b", 63) + `"}`},
		{"65 characters", "a" + strings.Repeat("b", 64), `{}`},
		{"version too long for the bound", DeviceClientName(strings.Repeat("9", 60)), `{}`},
		{"markup", "<script>alert(1)</script>", `{}`},
		{"newline", "agentcell/0.1.3\nlinux/amd64", `{}`},
		{"leading space", " agentcell/0.1.3", `{}`},
		{"empty", "", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			var gotContentLength int64
			var gotTE []string
			mux := http.NewServeMux()
			mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				gotContentLength = r.ContentLength
				gotTE = r.TransferEncoding
				writeJSON(w, http.StatusOK, deviceStartResponse{DeviceCode: "d", UserCode: "u", VerificationURI: "/x", ExpiresIn: 30, Interval: 1})
			})
			mux.HandleFunc("/v1/auth/poll", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, pollResponse{Status: "ready", Token: testToken, OrgID: "o", Email: "a@b.com", Plan: "p"})
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example", NoBrowser: true, ClientName: tc.client}
			if _, apiErr := Login(context.Background(), cfg); apiErr != nil {
				t.Fatalf("device login refused: %+v", apiErr)
			}
			if string(gotBody) != tc.wantBody {
				t.Fatalf("device start body = %q, want %q", gotBody, tc.wantBody)
			}
			if gotContentLength != int64(len(gotBody)) || len(gotTE) != 0 {
				t.Fatalf("device start framing: Content-Length=%d Transfer-Encoding=%v for %d bytes; want a matching Content-Length and no chunking", gotContentLength, gotTE, len(gotBody))
			}
		})
	}
}

// TestPollTolerates3Consecutive401sThenSucceeds: a run of up to three consecutive `unauthenticated`
// poll responses (SIGNUP.md's confirmation-then-poll race, server-side fix pending) must not abort
// the device flow -- the fourth poll succeeding proves the flow kept polling through all three.
func TestPollTolerates3Consecutive401sThenSucceeds(t *testing.T) {
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deviceStartResponse{DeviceCode: "d", UserCode: "u", VerificationURI: "/x", ExpiresIn: 30, Interval: 1})
	})
	mux.HandleFunc("/v1/auth/poll", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&polls, 1) <= 3 {
			writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "")
			return
		}
		writeJSON(w, http.StatusOK, pollResponse{Status: "ready", Token: testToken, OrgID: "o", Email: "a@b.com", Plan: "p"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example", NoBrowser: true}
	result, apiErr := Login(context.Background(), cfg)
	if apiErr != nil {
		t.Fatalf("should have tolerated 3 consecutive 401s and kept polling: %+v", apiErr)
	}
	if result.Token != testToken {
		t.Fatalf("result=%+v", result)
	}
	if got := atomic.LoadInt32(&polls); got != 4 {
		t.Fatalf("polls=%d, want exactly 4 (3 tolerated + 1 that succeeded)", got)
	}
}

// TestPollGivesUpOnFourthConsecutive401: the fourth consecutive `unauthenticated` poll must abort
// the flow rather than retry forever -- the tolerance is bounded, not unconditional.
func TestPollGivesUpOnFourthConsecutive401(t *testing.T) {
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deviceStartResponse{DeviceCode: "d", UserCode: "u", VerificationURI: "/x", ExpiresIn: 30, Interval: 1})
	})
	mux.HandleFunc("/v1/auth/poll", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&polls, 1)
		writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example", NoBrowser: true}
	_, apiErr := Login(context.Background(), cfg)
	if apiErr == nil {
		t.Fatal("expected the device flow to give up after 4 consecutive 401s")
	}
	if got := atomic.LoadInt32(&polls); got != 4 {
		t.Fatalf("polls=%d, want exactly 4 (3 tolerated + 1 that gave up)", got)
	}
}

// TestServiceUnavailableHonoursRetryAfter: a 503 service_unavailable carrying Retry-After must be
// retried after that many seconds, transparently to the caller -- the same contract
// operations/http.go's doWithRetry already holds for the eleven operation verbs.
func TestServiceUnavailableHonoursRetryAfter(t *testing.T) {
	var calls int32
	start := time.Now()
	var calledAt time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			writeRefusal(w, http.StatusServiceUnavailable, contract.CodeService, "")
			return
		}
		calledAt = time.Now()
		writeJSON(w, http.StatusOK, whoamiResponse{Email: "a@example.com", OrgID: "u-1", Plan: "design-partner", Scopes: []string{"read"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL}
	result, apiErr := Whoami(context.Background(), cfg, testToken)
	if apiErr != nil {
		t.Fatalf("whoami refused: %+v", apiErr)
	}
	if result.Email != "a@example.com" {
		t.Fatalf("result=%+v", result)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls=%d, want 2 (the retried request)", got)
	}
	if calledAt.Sub(start) < 900*time.Millisecond {
		t.Fatalf("retried before Retry-After elapsed: %s", calledAt.Sub(start))
	}
}

// TestLogoutDeadTokenStillClearsLocally is SIGNUP.md §5's logout rule: a token the server no
// longer recognises must still result in the credential being gone locally.
func TestLogoutDeadTokenStillClearsLocally(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		writeRefusal(w, http.StatusUnauthorized, contract.CodeUnauthenticated, "")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL}
	alreadyDead, apiErr := Logout(context.Background(), cfg, "act_dead_token")
	if apiErr != nil {
		t.Fatalf("logout on a dead token must not be a hard error: %+v", apiErr)
	}
	if !alreadyDead {
		t.Fatal("expected alreadyDead=true for an unauthenticated refusal")
	}
}

// TestLogoutOtherFailureIsNotTreatedAsDead: a transport or service failure must not be silently
// treated as "already dead" -- that would delete a token that might still work.
func TestLogoutOtherFailureIsNotTreatedAsDead(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		writeRefusal(w, http.StatusServiceUnavailable, contract.CodeService, "")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL}
	alreadyDead, apiErr := Logout(context.Background(), cfg, testToken)
	if apiErr == nil {
		t.Fatal("expected an error for service_unavailable")
	}
	if alreadyDead {
		t.Fatal("service_unavailable must not be treated as an already-dead token")
	}
}

// TestLoginNeverPrintsTheToken is the negative control for the file comment's central claim.
// It asserts on the CONCATENATION of everything this package wrote to stdout and stderr across a
// full, successful loopback login, which is the only way this property is checkable: printing the
// token anywhere in that combined stream is the defect, wherever it would happen to be added.
func TestLoginNeverPrintsTheToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"token": testToken, "org_id": "u-secretorg", "email": "leak-check@example.com",
			"plan": "design-partner", "scopes": []string{"deploy", "read"}, "token_prefix": "act_testprefix",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	opened := make(chan string, 1)
	cfg := AuthConfig{
		APIBaseURL: server.URL, LoginBaseURL: "https://login.example",
		IsTTY: true, Stdout: &stdout, Stderr: &stderr,
		OpenBrowser: func(target string) error { opened <- target; return nil },
	}
	go func() {
		target := <-opened
		u, _ := url.Parse(target)
		port := u.Query().Get("port")
		state := u.Query().Get("state")
		callback := fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&code=leak-check", port, url.QueryEscape(state))
		for i := 0; i < 50; i++ {
			if resp, err := http.Get(callback); err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	result, apiErr := Login(context.Background(), cfg)
	if apiErr != nil {
		t.Fatalf("login refused: %+v", apiErr)
	}
	// The full text this package printed anywhere, plus the one line cmd/agentcell prints on
	// success (reproduced here rather than run as a subprocess, so the assertion covers exactly
	// what a real invocation's combined output would be).
	combined := stdout.String() + stderr.String() + LoginMessage(result) + "\n"
	if strings.Contains(combined, result.Token) {
		t.Fatalf("the token leaked into printed output:\n%s", combined)
	}
}

// A small sanity check that the retry logic in doJSON does not retry forever: an unbounded
// Retry-After is refused rather than honoured, matching operations/http.go's authBusyMaxSleep cap.
func TestRetryAfterAboveCapIsNotHonoured(t *testing.T) {
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", strconv.Itoa(int(authBusyMaxSleep.Seconds())+1))
		writeRefusal(w, http.StatusServiceUnavailable, contract.CodeService, "")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := AuthConfig{APIBaseURL: server.URL}
	_, apiErr := Whoami(context.Background(), cfg, testToken)
	if apiErr == nil || apiErr.Code != contract.CodeService {
		t.Fatalf("expected the service_unavailable to surface, not be retried away: %+v", apiErr)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls=%d, want exactly 1 (no retry above the cap)", got)
	}
}

// A shell that is not a terminal (every coding agent's shell) must still get the loopback flow
// when a browser can be opened: the person picks an account and types nothing. Observed failing
// on the pre-fix client, which chose the device path on !IsTTY and printed a code to type.
func TestNonTTYStillUsesLoopbackWhenABrowserIsReachable(t *testing.T) {
	devicePosts := int32(0)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&devicePosts, 1)
		http.Error(w, `{"code":"invalid_request","message":"device flow must not be used here","hint":""}`, 400)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	opened := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// The loopback flow waits for the browser; cancel once we have seen it open the URL.
		<-opened
		cancel()
	}()
	var stderr bytes.Buffer
	cfg := AuthConfig{APIBaseURL: server.URL, LoginBaseURL: "https://login.example",
		IsTTY: false, BrowserReachable: func() bool { return true },
		OpenBrowser: func(target string) error { opened <- target; return nil }, Stderr: &stderr}
	_, _ = Login(ctx, cfg)
	if got := atomic.LoadInt32(&devicePosts); got != 0 {
		t.Fatalf("the device flow was used from a non-TTY shell with a reachable browser (%d device posts); the person would be asked to type a code", got)
	}
}

// TestPlaintextPublicBaseIsRefusedWithNoRequestSent is the negative control for the fix folded
// into doJSON/checkBase: a plain-http base outside the Tailscale overlay and loopback must be
// refused BEFORE any request is sent, the same property loginLoopback's wrong-state test holds
// for /v1/auth/exchange. The RoundTripper fails the test outright if it is ever invoked, which is
// how "zero hits" is checked -- not by counting after the fact, which a bug that also drops the
// counter would defeat.
func TestPlaintextPublicBaseIsRefusedWithNoRequestSent(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("a request was sent to %s despite the base URL failing the plaintext check", r.URL)
		return nil, nil
	})
	cfg := AuthConfig{APIBaseURL: "http://203.0.113.9", HTTPClient: &http.Client{Transport: transport}}
	_, apiErr := Whoami(context.Background(), cfg, testToken)
	if apiErr == nil || apiErr.Code != contract.CodeUsage {
		t.Fatalf("expected a usage refusal for a plaintext public base, got %+v", apiErr)
	}
}

// TestPlaintextTailnetBaseIsAllowed: 100.64.0.0/10 is the Tailscale overlay range and plain http
// there is not plaintext on any network (operations/http.go's checkBaseURL comment). The base is
// http:// and the request must still go out.
func TestPlaintextTailnetBaseIsAllowed(t *testing.T) {
	var reached bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached = true
		return fakeJSONResponse(http.StatusOK, whoamiResponse{Email: "a@example.com", OrgID: "u-1", Plan: "design-partner", Scopes: []string{"read"}}), nil
	})
	cfg := AuthConfig{APIBaseURL: "http://100.64.0.10:4680", HTTPClient: &http.Client{Transport: transport}}
	_, apiErr := Whoami(context.Background(), cfg, testToken)
	if apiErr != nil {
		t.Fatalf("a Tailscale overlay base over http must be allowed: %+v", apiErr)
	}
	if !reached {
		t.Fatal("the request was never sent")
	}
}

// TestHTTPSBaseIsAllowed: https is always allowed, any host.
func TestHTTPSBaseIsAllowed(t *testing.T) {
	var reached bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached = true
		return fakeJSONResponse(http.StatusOK, whoamiResponse{Email: "a@example.com", OrgID: "u-1", Plan: "design-partner", Scopes: []string{"read"}}), nil
	})
	cfg := AuthConfig{APIBaseURL: "https://api.agentcell.cloud", HTTPClient: &http.Client{Transport: transport}}
	_, apiErr := Whoami(context.Background(), cfg, testToken)
	if apiErr != nil {
		t.Fatalf("an https base must be allowed: %+v", apiErr)
	}
	if !reached {
		t.Fatal("the request was never sent")
	}
}

// TestWhoamiResultJSONShapeMatchesWire: `agentcell whoami --output=json`, or the non-TTY default,
// must encode the same field names a scripted caller expects (email, org_id, ... -- the wire
// shape whoamiResponse already carries), not Go's capitalised defaults.
func TestWhoamiResultJSONShapeMatchesWire(t *testing.T) {
	result := &WhoamiResult{
		Email: "a@b.com", OrgID: "o1", OrgKind: "personal", Plan: "design-partner",
		Scopes: []string{"deploy", "read"}, IssuedVia: "login", TokenPrefix: "act_abc",
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, field := range []string{`"email"`, `"org_id"`, `"org_kind"`, `"plan"`, `"scopes"`, `"issued_via"`, `"token_prefix"`} {
		if !strings.Contains(got, field) {
			t.Errorf("whoami JSON %s missing field %s", got, field)
		}
	}
	if strings.Contains(got, `"Email"`) || strings.Contains(got, `"OrgID"`) {
		t.Fatalf("whoami JSON uses Go field names instead of the wire shape: %s", got)
	}
}
