package operations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agentcell/agentcell-client/contract"
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
