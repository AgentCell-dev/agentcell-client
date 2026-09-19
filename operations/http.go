package operations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

// expectContinueTimeout bounds how long a deploy waits for the service to invite the upload.
//
// A deploy asks `Expect: 100-continue`, so a refusal (a bad token, a read-only one, a rate limit)
// arrives as a typed response before the archive is sent. Without it the service refuses before
// reading, closes, and the client saw either the typed error or "connection reset by peer"
// depending on timing: 6 of 20 real 24 MB deploys with a bad token got the reset. The service
// answers 100 only after its authentication, which can include a database round trip, so this is
// generous; if it elapses, Go sends the body anyway, which is exactly the behaviour before.
const expectContinueTimeout = 30 * time.Second

// DefaultBaseURL is where the CLI and the MCP server look for the control plane when nothing says
// otherwise: the public hostname, reachable from any machine on the internet.
//
// It is here rather than in cmd/agentcell so there is ONE of it. Every caller that builds an
// HTTPClient — the CLI, the MCP server, and whatever embeds this package next — gets the same
// answer, and a second copy written into a second entry point is the drift that makes `agentcell`
// and `agentcell mcp` talk to different services.
//
// WHY A PUBLIC HOSTNAME IS THE DEFAULT AND AN OVERLAY ADDRESS IS NOT. Until now the default named
// a host on a Tailscale network that no customer is on, so the honest description of this client
// was that it worked for the operator. A design partner running a coding session has no tailnet
// and cannot be given one; the default has to be the door they can reach.
//
// THE OVERLAY IS STILL SELECTABLE, and that is deliberate rather than a leftover: AGENTCELL_API_URL
// in the environment and --api-url= on the command line both override this, which is how an
// operator reaches the service directly when the public path is the thing that is broken. That
// ordering — flag, then environment, then this — is in cmd/agentcell.
//
// NOTHING ABOUT AUTHENTICATION CHANGES WITH THE ROUTE. There is no browser gate on this hostname;
// the service authenticates the bearer token itself and answers a typed {code, message, hint}
// either way, which is why the same client code works over both and why an agent driving it can
// still branch on the code.
const DefaultBaseURL = "https://api.agentcell.cloud"

var defaultClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ExpectContinueTimeout = expectContinueTimeout
	return &http.Client{Transport: transport}
}()

type HTTPClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (c *HTTPClient) Execute(ctx context.Context, request contract.Request) (contract.Response, error) {
	definition, ok := contract.Lookup(request.Operation())
	if !ok {
		return nil, &contract.APIError{Code: contract.CodeUsage, Message: "unknown operation", Hint: "run agentcell help to list operations"}
	}
	if definition.Streaming {
		return nil, &contract.APIError{Code: contract.CodeUsage, Message: "streaming operation used as request/response", Hint: "use Operations.Stream"}
	}
	response, err := c.doWithRetry(ctx, definition, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err := decodeError(response, c.Token); err != nil {
		return nil, err
	}
	result := contract.NewResponse(definition)
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		return nil, &contract.APIError{Code: contract.CodeTransport, Message: "service returned an invalid response", Hint: "check client and service API versions"}
	}
	return result, nil
}

func (c *HTTPClient) Stream(ctx context.Context, request contract.Request, emit func(contract.Response) error) error {
	definition, ok := contract.Lookup(request.Operation())
	if !ok || !definition.Streaming {
		return &contract.APIError{Code: contract.CodeUsage, Message: "operation does not stream", Hint: "use Execute for non-streaming operations"}
	}
	response, err := c.do(ctx, definition, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := decodeError(response, c.Token); err != nil {
		return err
	}
	scanner := bufio.NewScanner(response.Body)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		item := contract.NewResponse(definition)
		if err := json.Unmarshal(scanner.Bytes(), item); err != nil {
			return &contract.APIError{Code: contract.CodeTransport, Message: "service returned an invalid stream record", Hint: "retry without --follow and report the response"}
		}
		if err := emit(item); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return &contract.APIError{Code: contract.CodeTransport, Message: "log stream was interrupted", Hint: "retry; log records are resumable by timestamp"}
	}
	return nil
}

// tailnetRanges are the blocks Tailscale assigns overlay addresses from: the CGNAT block for IPv4
// and the fd7a:115c:a1e0::/48 ULA for IPv6.
//
// THE IPv6 HALF WAS MISSING AND THAT IS NOT A THEORETICAL GAP. Every unit on this platform has
// both; `tailscale status` shows a 100.x address and an fd7a:115c:a1e0:: address for each. An
// operator who reached the control plane over IPv6 -- which is what happens the moment a hostname
// resolves to the ULA, or somebody pastes the address the admin console shows -- would have been
// told to use https for a destination already inside the WireGuard mesh, on the exact path this
// exemption exists to allow.
var tailnetRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func inTailnet(address netip.Addr) bool {
	for _, prefix := range tailnetRanges {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// checkBaseURL refuses to send a bearer token in clear text to anywhere it could be read.
//
// THE TOKEN IS THE WHOLE CREDENTIAL. It is presented on every request, it is not bound to a
// session, and over plaintext it is readable by anything between here and the service — after
// which it deploys, reads logs and, with the right scope, changes who can reach a cell. Now that
// the default base URL is a public hostname, an operator who overrides it is overriding it to
// something, and `http://` is one keystroke away from `https://`.
//
// TWO EXCEPTIONS, AND BOTH ARE ADDRESSES THAT CANNOT LEAVE A TRUSTED PATH. The overlay is a
// WireGuard mesh: traffic to 100.64.0.0/10 is encrypted and authenticated by Tailscale before it
// reaches a wire, so plaintext HTTP inside it is not plaintext on any network — and this is the
// path operators use to reach the control plane directly when the public one is what is broken.
// Loopback is the test and development case and never crosses a network at all.
//
// A NAME IS NOT ENOUGH, deliberately: `cp-1.example.com` may resolve into the tailnet today and
// somewhere else tomorrow, and this check would then be approving a route it cannot see. Only a
// literal address in those ranges, or `localhost`, is accepted.
func checkBaseURL(base *url.URL) error {
	switch base.Scheme {
	case "https":
		return nil
	case "http":
	default:
		return &contract.APIError{Code: contract.CodeUsage, Message: "the API URL must be an https URL", Hint: "set AGENTCELL_API_URL or --api-url= to an https:// address"}
	}
	host := base.Hostname()
	if host == "localhost" {
		return nil
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if address.IsLoopback() || inTailnet(address) {
			return nil
		}
	}
	return &contract.APIError{
		Code:    contract.CodeUsage,
		Message: "refusing to send your API token over plain HTTP to " + host,
		Hint:    "Use https://. Plain HTTP is accepted only for a Tailscale overlay address (100.64.0.0/10 or fd7a:115c:a1e0::/48) or loopback, where the transport is already encrypted; your token is presented on every request and is readable by anything in between.",
	}
}

// How hard the client tries when the service says it is busy. See doWithRetry.
const (
	busyRetries  = 2
	busyMaxWait  = 15 * time.Second
	busyMaxSleep = 10 * time.Second
)

// doWithRetry re-sends a request the service refused for being BUSY, once the service says it is
// worth trying again.
//
// WHY THIS IS NEEDED AND WHAT IT IS NOT. The control plane bounds several things and refuses
// rather than queues when it hits one: concurrent authentications, connections, deploys. Those
// refusals are `service_unavailable` with a `Retry-After`, and they are DELIBERATELY not errors
// about the request — the same request a moment later succeeds. Until now this client surfaced
// them straight to the caller, so a perfectly good deploy failed because something else was busy.
//
// THE CASE THAT FORCED IT [observed on cp-1, 19 September 2026]. A token the service has never
// seen queues at a narrow door until its first lookup completes. An agent fires several tool calls
// at once — Claude Code does exactly this — so the FIRST parallel burst on a new token has every
// request carrying an unknown prefix, and some are refused. One sequential call would have warmed
// it; an agent does not know that and should not have to. With this retry the partner sees a pause
// and then their answer.
//
// ONLY ON A TYPED `service_unavailable` CARRYING Retry-After. A bare 503, an HTML page from an
// intermediary, or any other code is surfaced unchanged: this client must not turn "the service is
// down" into three times as many requests, and a refusal with no Retry-After is not this service
// saying come back.
//
// SAFE BY CONSTRUCTION FOR EVERY OPERATION IT COVERS. `deploy` carries an Idempotency-Key, which is
// a hash of the source, so a re-send is the same deploy and the service answers the first one's
// result rather than building twice. Everything else Execute handles is a read or a refusal.
// STREAMS ARE EXCLUDED — Stream calls `do` directly — because a stream hands records to a callback
// the caller may already have acted on, so resuming one is a question about timestamps rather than
// a re-send; contract/errors.go already says so in its hint.
//
// THE TOTAL WAIT IS CAPPED so a busy service cannot hold a CLI session open indefinitely, and each
// sleep is cancellable: an operator pressing ctrl-C during a retry must be obeyed.
func (c *HTTPClient) doWithRetry(ctx context.Context, definition contract.Definition, request contract.Request) (*http.Response, error) {
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		response, err := c.do(ctx, definition, request)
		if err != nil {
			return nil, err
		}
		if attempt >= busyRetries {
			return response, nil
		}
		delay, retryable := busyRetryAfter(response)
		if !retryable || waited+delay > busyMaxWait {
			return response, nil
		}
		// The body was read to classify it and is of no further use: this response is being
		// replaced, not returned.
		response.Body.Close()
		waited += delay
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &contract.APIError{Code: contract.CodeTransport, Message: "cancelled while waiting to retry", Hint: "the service asked for a retry after " + delay.String()}
		case <-timer.C:
		}
	}
}

// busyRetryAfter reports whether this response is the service asking to be retried, and when.
//
// It CONSUMES AND RESTORES the body, because the decision needs the typed code and the caller
// needs the bytes: a response that turns out not to be retryable must reach decodeError exactly as
// it arrived, hint included. The read is bounded — a typed error is a few hundred bytes, and this
// must not buffer a large body just to decide not to retry it.
func busyRetryAfter(response *http.Response) (time.Duration, bool) {
	if response.StatusCode != http.StatusServiceUnavailable {
		return 0, false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return 0, false
	}
	var api contract.APIError
	if json.Unmarshal(raw, &api) != nil || api.Code != contract.CodeService {
		return 0, false
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After")))
	if err != nil || seconds < 0 {
		// Only the delta-seconds form. This service sends an integer; an HTTP-date would mean the
		// response came from something else, which is not a thing to retry against.
		return 0, false
	}
	delay := time.Duration(seconds) * time.Second
	if delay > busyMaxSleep {
		return 0, false
	}
	return delay, true
}

func (c *HTTPClient) do(ctx context.Context, definition contract.Definition, request contract.Request) (*http.Response, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, &contract.APIError{Code: contract.CodeUsage, Message: "invalid API URL", Hint: "set AGENTCELL_API_URL to an https URL"}
	}
	// Before the request is built, so the token is never written into anything that could be
	// sent. A refusal here has not touched the network.
	if err := checkBaseURL(base); err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/operations/" + definition.Name
	body, err := json.Marshal(request)
	if err != nil {
		return nil, &contract.APIError{Code: contract.CodeInvalid, Message: "request could not be encoded", Hint: "check the command arguments"}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &contract.APIError{Code: contract.CodeTransport, Message: "request could not be created", Hint: "check the API URL"}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/x-ndjson, application/json")
	httpRequest.Header.Set(contract.APIVersionHeader, contract.APIVersion)
	httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	if deploy, ok := request.(*contract.DeployRequest); ok {
		httpRequest.Header.Set("Idempotency-Key", deploy.IdempotencyKey)
		// The one request whose body is large enough to matter; see expectContinueTimeout.
		httpRequest.Header.Set("Expect", "100-continue")
	}
	client := c.Client
	if client == nil {
		client = defaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, &contract.APIError{Code: contract.CodeTransport, Message: contract.Redact("cannot reach AgentCell service: "+err.Error(), c.Token), Hint: "check the network, API URL and overlay connection"}
	}
	return response, nil
}

func decodeError(response *http.Response, secret string) error {
	serverVersion := response.Header.Get(contract.APIVersionHeader)
	if serverVersion != "" && serverVersion != contract.APIVersion {
		io.Copy(io.Discard, response.Body)
		return &contract.APIError{Code: contract.CodeUnsupportedVersion, Message: "client and service API versions are incompatible", Hint: fmt.Sprintf("client=%s service=%s; upgrade the older component", contract.APIVersion, serverVersion)}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	var api contract.APIError
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&api); err != nil || api.Code == "" {
		return &contract.APIError{Code: contract.CodeService, Message: fmt.Sprintf("service returned HTTP %d", response.StatusCode), Hint: "retry; if it persists, report the status code"}
	}
	api.Message = contract.Redact(api.Message, secret)
	api.Hint = contract.Redact(api.Hint, secret)
	return &api
}
