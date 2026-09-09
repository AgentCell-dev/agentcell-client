package operations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

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
	response, err := c.do(ctx, definition, request)
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

func (c *HTTPClient) do(ctx context.Context, definition contract.Definition, request contract.Request) (*http.Response, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, &contract.APIError{Code: contract.CodeUsage, Message: "invalid API URL", Hint: "set AGENTCELL_API_URL to an https URL"}
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
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
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
