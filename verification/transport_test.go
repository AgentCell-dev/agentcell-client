package verification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentcell/agentcell-client/contract"
	"github.com/agentcell/agentcell-client/operations"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string) *http.Response {
	header := make(http.Header)
	header.Set(contract.APIVersionHeader, contract.APIVersion)
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

// Expectations come from README's protocol section and contract/types.go. README does not
// document method, route, JSON field schemas, response schemas, or content types, so this test
// records those as observations rather than deriving expectations from operations/http.go.
func TestEveryNonStreamingRequestCarriesVersionAndTypedBody(t *testing.T) {
	type operationCase struct {
		request                   contract.Request
		requestJSON, responseJSON string
	}
	cases := []operationCase{
		{&contract.DeployRequest{Source: []byte("archive"), CellID: "cell-a", IdempotencyKey: "deploy-v1:abc"}, `{"source_tar_gzip":"YXJjaGl2ZQ==","cell_id":"cell-a","idempotency_key":"deploy-v1:abc"}`, `{"cell_id":"cell-a","deployment_id":"dep-1","url":"https://cell.invalid","status":"ready"}`},
		{&contract.RollbackRequest{CellID: "cell-a", DeploymentID: "dep-1"}, `{"cell_id":"cell-a","deployment_id":"dep-1"}`, `{"cell_id":"cell-a","deployment_id":"dep-1","status":"ready"}`},
		{&contract.EnvRequest{CellID: "cell-a", Action: "set", Values: []string{"A=B"}}, `{"cell_id":"cell-a","action":"set","values":["A=B"]}`, `{"values":{"A":"B"}}`},
		{&contract.SecretsRequest{CellID: "cell-a", Action: "list"}, `{"cell_id":"cell-a","action":"list"}`, `{"names":["TOKEN"]}`},
		{&contract.DomainsRequest{CellID: "cell-a", Action: "add", Domain: "example.test"}, `{"cell_id":"cell-a","action":"add","domain":"example.test"}`, `{"domains":["example.test"]}`},
		{&contract.ShareRequest{CellID: "cell-a", Subject: "user:test", Role: "viewer"}, `{"cell_id":"cell-a","subject":"user:test","role":"viewer"}`, `{"cell_id":"cell-a","subject":"user:test","role":"viewer"}`},
		{&contract.AccessRequest{CellID: "cell-a", Action: "list"}, `{"cell_id":"cell-a","action":"list"}`, `{"grants":[{"subject":"user:test","role":"viewer"}]}`},
		{&contract.PSRequest{All: true}, `{"all":true}`, `{"cells":[{"cell_id":"cell-a","status":"awake","url":"https://cell.invalid"}]}`},
		{&contract.SpendRequest{CellID: "cell-a"}, `{"cell_id":"cell-a"}`, `{"currency":"USD","amount_minor":123}`},
		{&contract.DestroyRequest{CellID: "cell-a", Confirm: "cell-a"}, `{"cell_id":"cell-a","confirm":"cell-a"}`, `{"cell_id":"cell-a","status":"destroyed"}`},
	}
	seen := map[string]bool{}
	next := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		testCase := cases[next]
		operation := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		seen[operation] = true
		if got := request.Header.Get(contract.APIVersionHeader); got != "2026-08-01" {
			t.Errorf("%s version header = %q", operation, got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer unique-wire-token" {
			t.Errorf("%s authorization = %q", operation, got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Errorf("%s body is not JSON: %v", operation, err)
		}
		var expected map[string]any
		if err := json.Unmarshal([]byte(testCase.requestJSON), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(object, expected) {
			t.Errorf("%s body = %#v, want %#v", operation, object, expected)
		}
		if operation == "deploy" {
			if got := request.Header.Get("Idempotency-Key"); got != "deploy-v1:abc" {
				t.Errorf("deploy header key = %q", got)
			}
			if got := object["idempotency_key"]; got != "deploy-v1:abc" {
				t.Errorf("deploy body key = %#v", got)
			}
		}
		next++
		return response(http.StatusOK, testCase.responseJSON), nil
	})
	client := &operations.HTTPClient{BaseURL: "https://documented-contract.invalid", Token: "unique-wire-token", Client: &http.Client{Transport: transport}}
	for _, testCase := range cases {
		result, err := client.Execute(context.Background(), testCase.request)
		if err != nil {
			t.Errorf("%s: %v", testCase.request.Operation(), err)
			continue
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var got, expected map[string]any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(testCase.responseJSON), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s response = %#v, want %#v", testCase.request.Operation(), got, expected)
		}
	}
	if len(seen) != len(cases) {
		t.Fatalf("observed %d operations, want %d", len(seen), len(cases))
	}
	t.Logf("observed %d non-streaming requests; version and bearer headers present on every request", len(seen))
}

func TestLogsEmitsFirstRecordBeforeFinalRecordExists(t *testing.T) {
	reader, writer := io.Pipe()
	requestSeen := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get(contract.APIVersionHeader); got != "2026-08-01" {
			t.Errorf("logs version header = %q", got)
		}
		close(requestSeen)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{contract.APIVersionHeader: []string{contract.APIVersion}}, Body: reader}, nil
	})
	client := &operations.HTTPClient{BaseURL: "https://documented-contract.invalid", Token: "stream-token", Client: &http.Client{Transport: transport}}
	first := make(chan contract.LogEntry, 1)
	done := make(chan error, 1)
	var mu sync.Mutex
	var entries []contract.LogEntry
	go func() {
		done <- client.Stream(context.Background(), &contract.LogsRequest{CellID: "cell-a", Follow: true}, func(value contract.Response) error {
			entry := *value.(*contract.LogEntry)
			mu.Lock()
			entries = append(entries, entry)
			count := len(entries)
			mu.Unlock()
			if count == 1 {
				first <- entry
			}
			return nil
		})
	}()
	<-requestSeen
	if _, err := io.WriteString(writer, "{\"time\":\"t1\",\"stream\":\"stdout\",\"message\":\"first\"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case entry := <-first:
		if entry.Message != "first" {
			t.Fatalf("first message = %q", entry.Message)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("first log was buffered while the final record did not yet exist")
	}
	if _, err := io.WriteString(writer, "{\"time\":\"t2\",\"stream\":\"stderr\",\"message\":\"last\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(entries) != 2 || entries[1].Message != "last" {
		t.Fatalf("entries = %#v", entries)
	}
	t.Log("first record observed before the second record was written")
}
