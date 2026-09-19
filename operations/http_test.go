package operations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

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
