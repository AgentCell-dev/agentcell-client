package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrorCode is a stable, machine-readable part of the public API.
type ErrorCode string

const (
	CodeUsage              ErrorCode = "usage"
	CodeUnauthenticated    ErrorCode = "unauthenticated"
	CodeForbidden          ErrorCode = "forbidden"
	CodeNotFound           ErrorCode = "not_found"
	CodeConflict           ErrorCode = "conflict"
	CodeInvalid            ErrorCode = "invalid_request"
	CodeUnsupportedVersion ErrorCode = "unsupported_version"
	CodeTransport          ErrorCode = "transport"
	CodeService            ErrorCode = "service_unavailable"
	CodeBuild              ErrorCode = "build_failed"
	CodeDeploy             ErrorCode = "deploy_failed"
)

// APIError has one representation and three renderers: human, JSON and MCP.
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Hint    string    `json:"hint"`
}

func (e *APIError) Error() string { return string(e.Code) + ": " + e.Message }

func (e *APIError) Human() string {
	if e.Hint == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s\nhint: %s", e.Code, e.Message, e.Hint)
}

func (e *APIError) JSON() []byte {
	b, _ := json.Marshal(e)
	return b
}

func AsAPIError(err error) *APIError {
	var api *APIError
	if errors.As(err, &api) {
		return api
	}
	return &APIError{Code: CodeService, Message: "operation failed", Hint: "retry; if the failure persists, run with --output=json and report the error code"}
}

// ExitCode is stable. Changing a value is a breaking API change for agents.
func ExitCode(code ErrorCode) int {
	switch code {
	case CodeUsage:
		return 2
	case CodeUnauthenticated:
		return 10
	case CodeForbidden:
		return 11
	case CodeNotFound:
		return 12
	case CodeConflict:
		return 13
	case CodeInvalid:
		return 14
	case CodeUnsupportedVersion:
		return 15
	case CodeTransport:
		return 20
	case CodeService:
		return 21
	case CodeBuild:
		return 30
	case CodeDeploy:
		return 31
	default:
		return 1
	}
}

// Redact replaces secrets even when an upstream error accidentally includes one.
func Redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return message
}
