package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

func TestDeployCarriesVersionTokenAndIdempotencyKey(t *testing.T) {
	clientTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get(contract.APIVersionHeader); got != contract.APIVersion {
			t.Errorf("version=%q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "deploy-v1:digest" {
			t.Errorf("idempotency=%q", got)
		}
		return jsonResponse(http.StatusOK, contract.APIVersion, contract.DeployResponse{CellID: "cell-1", Status: "ready"}), nil
	})
	client := &HTTPClient{BaseURL: "https://api.example", Token: "test-token", Client: &http.Client{Transport: clientTransport}}
	response, err := client.Execute(context.Background(), &contract.DeployRequest{IdempotencyKey: "deploy-v1:digest"})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.(*contract.DeployResponse).CellID; got != "cell-1" {
		t.Fatalf("cell=%q", got)
	}
}

func TestVersionMismatchIsTypedAndLegible(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, "2099-01-01", contract.PSResponse{}), nil
	})
	client := &HTTPClient{BaseURL: "https://api.example", Token: "secret", Client: &http.Client{Transport: transport}}
	_, err := client.Execute(context.Background(), &contract.PSRequest{})
	api := contract.AsAPIError(err)
	if api.Code != contract.CodeUnsupportedVersion || api.Hint == "" {
		t.Fatalf("error=%+v", api)
	}
}

func TestServiceErrorRedactsToken(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, contract.APIVersion, contract.APIError{Code: contract.CodeDeploy, Message: "token secret-token failed", Hint: "replace secret-token"}), nil
	})
	client := &HTTPClient{BaseURL: "https://api.example", Token: "secret-token", Client: &http.Client{Transport: transport}}
	_, err := client.Execute(context.Background(), &contract.PSRequest{})
	api := contract.AsAPIError(err)
	if api.Message == "token secret-token failed" || api.Hint == "replace secret-token" {
		t.Fatalf("secret leaked: %+v", api)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, version string, value any) *http.Response {
	b, _ := json.Marshal(value)
	header := make(http.Header)
	header.Set(contract.APIVersionHeader, version)
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(b)))}
}

// A deploy asks Expect: 100-continue and nothing else does. See expectContinueTimeout.
func TestOnlyDeployAsksToContinue(t *testing.T) {
	seen := map[string]string{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen[r.URL.Path] = r.Header.Get("Expect")
		if strings.HasSuffix(r.URL.Path, "/deploy") {
			return jsonResponse(http.StatusOK, contract.APIVersion, contract.DeployResponse{CellID: "cell-1"}), nil
		}
		return jsonResponse(http.StatusOK, contract.APIVersion, contract.PSResponse{}), nil
	})
	client := &HTTPClient{BaseURL: "https://api.example", Token: "t", Client: &http.Client{Transport: transport}}
	if _, err := client.Execute(context.Background(), &contract.DeployRequest{IdempotencyKey: "deploy-v1:d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Execute(context.Background(), &contract.PSRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := seen["/v1/operations/deploy"]; got != "100-continue" {
		t.Errorf("deploy Expect=%q, want 100-continue", got)
	}
	if got := seen["/v1/operations/ps"]; got != "" {
		t.Errorf("ps Expect=%q, want none", got)
	}
}

// TestPlaintextURLsAreRefusedUnlessTheTransportIsAlreadyEncrypted asserts the property rather than
// the message: does the bearer token reach the wire?
//
// A test that compared error strings would pass against a checkBaseURL that returned the right
// error AFTER building the request, which is the one way to get this wrong that matters — the
// token is the whole credential and the question is whether it was ever handed to a transport.
// So every case drives the real client through a RoundTripper that records the Authorization
// header, and the assertion is on that record.
func TestPlaintextURLsAreRefusedUnlessTheTransportIsAlreadyEncrypted(t *testing.T) {
	for _, testCase := range []struct {
		url  string
		sent bool
		why  string
	}{
		{"https://api.agentcell.cloud", true, "the public hostname, TLS"},
		{"https://api.agentcell.cloud/", true, "a trailing slash is still https"},
		{"http://100.122.58.11:4680", true, "a Tailscale CGNAT address: WireGuard underneath"},
		{"http://[fd7a:115c:a1e0::1234]:4680", true, "the Tailscale IPv6 ULA, the other half of the same mesh"},
		{"http://127.0.0.1:4680", true, "loopback never leaves the machine"},
		{"http://[::1]:4680", true, "loopback, v6"},
		{"http://localhost:4680", true, "loopback by name, which the tests and dev use"},
		{"http://api.agentcell.cloud", false, "the public hostname in clear text"},
		{"http://cp-1.someone-elses-host.example", false, "a name that may resolve anywhere tomorrow"},
		{"http://10.10.10.22:4680", false, "an RFC1918 address is NOT the overlay; this platform has both"},
		{"http://100.63.255.255:4680", false, "one address below the CGNAT block"},
		{"http://[fd7a:115c:a1e1::1]:4680", false, "one prefix outside the Tailscale ULA"},
		{"ftp://api.agentcell.cloud", false, "not a scheme this client speaks"},
	} {
		t.Run(testCase.url, func(t *testing.T) {
			var sawToken bool
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				sawToken = r.Header.Get("Authorization") != ""
				return jsonResponse(http.StatusOK, contract.APIVersion, contract.PSResponse{}), nil
			})
			client := &HTTPClient{BaseURL: testCase.url, Token: "act_test_secret", Client: &http.Client{Transport: transport}}
			_, err := client.Execute(context.Background(), &contract.PSRequest{})
			if sawToken != testCase.sent {
				t.Fatalf("token reached the wire = %v, want %v (%s); err=%v", sawToken, testCase.sent, testCase.why, err)
			}
			if testCase.sent {
				return
			}
			apiErr, ok := err.(*contract.APIError)
			if !ok || apiErr.Code != contract.CodeUsage {
				t.Fatalf("refusal must be a typed usage error an agent can branch on, got %#v", err)
			}
			if apiErr.Hint == "" {
				t.Errorf("a refusal with no hint tells the operator nothing about what to do instead")
			}
		})
	}
}

// busyResponse is the service's typed "I am busy, come back" — the only thing the client retries.
func busyResponse(retryAfter string) *http.Response {
	body, _ := json.Marshal(contract.APIError{
		Code:    contract.CodeService,
		Message: "the control plane is busy authenticating other requests",
		Hint:    "Retry in a few seconds. Nothing about your credential is implied by this answer.",
	})
	header := http.Header{}
	header.Set(contract.APIVersionHeader, contract.APIVersion)
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     header,
	}
}

// TestBusyRefusalsAreRetried covers the case that forced this: an agent firing several tool calls
// at once on a brand-new token, where the first burst queues at a narrow door and some of it is
// refused. The assertions are on ATTEMPTS — how many times the request actually reached the
// transport — because that is the behaviour, and on the error the caller finally sees, because a
// retry that swallowed the hint would be worse than no retry at all.
func TestBusyRefusalsAreRetried(t *testing.T) {
	t.Run("503 with Retry-After then 200: one retry, the answer is returned", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return busyResponse("1"), nil
			}
			return jsonResponse(http.StatusOK, contract.APIVersion, contract.PSResponse{}), nil
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		if _, err := client.Execute(context.Background(), &contract.PSRequest{}); err != nil {
			t.Fatalf("the retried request should have succeeded, got %v", err)
		}
		if attempts != 2 {
			t.Errorf("attempts = %d, want 2 (the original and one retry)", attempts)
		}
	})

	t.Run("always 503: exactly two retries, then the typed error with its hint", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return busyResponse("1"), nil
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		_, err := client.Execute(context.Background(), &contract.PSRequest{})
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3 (the original and two retries)", attempts)
		}
		apiErr, ok := err.(*contract.APIError)
		if !ok {
			t.Fatalf("the caller must still receive the typed error, got %#v", err)
		}
		if apiErr.Code != contract.CodeService {
			t.Errorf("code = %q, want %q", apiErr.Code, contract.CodeService)
		}
		// THE HINT IS THE POINT. Retrying and then surfacing a bare status would leave the operator
		// with less than they had before the retry existed.
		if !strings.Contains(apiErr.Hint, "Retry") {
			t.Errorf("the service's hint did not survive the retries: %q", apiErr.Hint)
		}
	})

	t.Run("a 503 with no Retry-After is not retried", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return busyResponse(""), nil
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		_, err := client.Execute(context.Background(), &contract.PSRequest{})
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1: without Retry-After this is not the service asking to be retried", attempts)
		}
		if apiErr, ok := err.(*contract.APIError); !ok || apiErr.Code != contract.CodeService {
			t.Errorf("the refusal must still surface typed, got %#v", err)
		}
	})

	t.Run("a refusal that is not service_unavailable is not retried", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			body, _ := json.Marshal(contract.APIError{Code: contract.CodeUnauthenticated, Message: "this token was not accepted", Hint: "x"})
			header := http.Header{}
			header.Set(contract.APIVersionHeader, contract.APIVersion)
			header.Set("Retry-After", "1")
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(bytes.NewReader(body)), Header: header}, nil
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		_, err := client.Execute(context.Background(), &contract.PSRequest{})
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1: a revoked token does not become valid by asking again", attempts)
		}
		if apiErr, ok := err.(*contract.APIError); !ok || apiErr.Code != contract.CodeUnauthenticated {
			t.Errorf("got %#v, want the unauthenticated refusal unchanged", err)
		}
	})

	t.Run("a stream is never retried", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return busyResponse("1"), nil
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		err := client.Stream(context.Background(), &contract.LogsRequest{CellID: "c"}, func(contract.Response) error { return nil })
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1: resuming a stream is a question about timestamps, not a re-send", attempts)
		}
		if apiErr, ok := err.(*contract.APIError); !ok || apiErr.Code != contract.CodeService {
			t.Errorf("got %#v, want the typed refusal", err)
		}
	})

	t.Run("the total wait is capped", func(t *testing.T) {
		attempts := 0
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return busyResponse("60"), nil // beyond any single sleep this client will take
		})
		client := &HTTPClient{BaseURL: "https://api.example", Token: "act_test_secret", Client: &http.Client{Transport: transport}}
		began := time.Now()
		if _, err := client.Execute(context.Background(), &contract.PSRequest{}); err == nil {
			t.Fatal("want the typed refusal")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1: a delay this long is surfaced rather than waited out", attempts)
		}
		// A LITERAL, not busyMaxWait: a test that reads the implementation's own constant moves
		// whenever the implementation moves, and would pass against a cap raised to an hour. It
		// also lets this file compile against a build WITHOUT the retry, which is what makes it
		// usable as a control.
		if elapsed := time.Since(began); elapsed > 15*time.Second {
			t.Errorf("waited %s, which is past the cap this client promises", elapsed)
		}
	})
}
