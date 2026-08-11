package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/agentcell/agentcell-client/contract"
	"github.com/agentcell/agentcell-client/internal/cli"
)

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema Schema `json:"inputSchema"`
}
type Schema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}
type Property struct {
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Items       *Property `json:"items,omitempty"`
}

func Tools(definitions []contract.Definition) []Tool {
	tools := make([]Tool, 0, len(definitions))
	for _, definition := range definitions {
		schema := Schema{Type: "object", Properties: map[string]Property{}}
		for i := 0; i < definition.RequestType.NumField(); i++ {
			field := definition.RequestType.Field(i)
			tag := field.Tag.Get("cli")
			if tag == "" || tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			name := parts[0]
			property := Property{Type: jsonType(field.Type), Description: field.Tag.Get("description")}
			if property.Type == "array" {
				property.Items = &Property{Type: "string"}
			}
			schema.Properties[name] = property
			if contains(parts, "required") || containsPrefix(parts, "position=") {
				schema.Required = append(schema.Required, name)
			}
		}
		tools = append(tools, Tool{Name: definition.Name, Description: contract.ToolDescription(definition), InputSchema: schema})
	}
	return tools
}

type Server struct {
	Operations  contract.Operations
	Definitions []contract.Definition
}
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	definitions := s.Definitions
	if definitions == nil {
		definitions = contract.Definitions
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var call request
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			if err := encoder.Encode(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		result, rpcErr := s.handle(ctx, definitions, call)
		if call.ID != nil {
			if err := encoder.Encode(response{JSONRPC: "2.0", ID: call.ID, Result: result, Error: rpcErr}); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (s Server) handle(ctx context.Context, definitions []contract.Definition, call request) (any, *rpcError) {
	switch call.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "agentcell", "version": contract.APIVersion}}, nil
	case "notifications/initialized":
		return nil, nil
	case "tools/list":
		return map[string]any{"tools": Tools(definitions)}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(call.Params, &params); err != nil {
			return nil, invalidParams("invalid tool call", "send name and arguments")
		}
		var definition *contract.Definition
		for i := range definitions {
			if definitions[i].Name == params.Name {
				definition = &definitions[i]
				break
			}
		}
		if definition == nil {
			return nil, invalidParams("unknown tool", "call tools/list")
		}
		values, apiErr := cli.JSONValues(params.Arguments)
		if apiErr != nil {
			return toolFailure(apiErr), nil
		}
		positionals := []string{}
		for i := 0; i < definition.RequestType.NumField(); i++ {
			field := definition.RequestType.Field(i)
			parts := strings.Split(field.Tag.Get("cli"), ",")
			if containsPrefix(parts, "position=") {
				name := parts[0]
				if raw := values[name]; len(raw) > 0 {
					positionals = append(positionals, raw[len(raw)-1])
					delete(values, name)
				}
			}
		}
		req, apiErr := cli.Bind(*definition, values, positionals)
		if apiErr != nil {
			return toolFailure(apiErr), nil
		}
		if definition.Streaming {
			items := []contract.Response{}
			if err := s.Operations.Stream(ctx, req, func(item contract.Response) error { items = append(items, item); return nil }); err != nil {
				return toolFailure(contract.AsAPIError(err)), nil
			}
			return toolSuccess(items), nil
		}
		result, err := s.Operations.Execute(ctx, req)
		if err != nil {
			return toolFailure(contract.AsAPIError(err)), nil
		}
		return toolSuccess(result), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func toolSuccess(value any) map[string]any {
	b, _ := json.Marshal(value)
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(b)}}}
}
func toolFailure(err *contract.APIError) map[string]any {
	return map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": string(err.JSON())}}}
}
func invalidParams(message, hint string) *rpcError {
	return &rpcError{Code: -32602, Message: message, Data: &contract.APIError{Code: contract.CodeInvalid, Message: message, Hint: hint}}
}
func jsonType(t reflect.Type) string {
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		return "string"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		return "array"
	case reflect.Int, reflect.Int64:
		return "integer"
	default:
		return "string"
	}
}
func contains(parts []string, wanted string) bool {
	for _, part := range parts {
		if part == wanted {
			return true
		}
	}
	return false
}
func containsPrefix(parts []string, wanted string) bool {
	for _, part := range parts {
		if strings.HasPrefix(part, wanted) {
			return true
		}
	}
	return false
}
