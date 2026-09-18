package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AgentCell-dev/agentcell-client/contract"
	"github.com/AgentCell-dev/agentcell-client/internal/cli"
	"github.com/AgentCell-dev/agentcell-client/internal/mcp"
	"github.com/AgentCell-dev/agentcell-client/operations"
	"github.com/AgentCell-dev/agentcell-client/token"
)

var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "version" {
		fmt.Printf("agentcell %s api=%s\n", version, contract.APIVersion)
		return 0
	}
	baseURL := os.Getenv("AGENTCELL_API_URL")
	if baseURL == "" {
		// From operations, not written here. The literal this replaced was `api.agentcell.dev`,
		// which is a different registrable domain from the one the platform serves and had no
		// route behind it at all — so the shipped default reached nothing, and every working
		// invocation was one that set AGENTCELL_API_URL or --api-url.
		baseURL = operations.DefaultBaseURL
	}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--api-url=") {
			baseURL = strings.TrimPrefix(args[i], "--api-url=")
			args = append(args[:i], args[i+1:]...)
			i--
		}
	}
	store, storeErr := token.DefaultFileStore()
	if storeErr != nil {
		return printStartupError(&contract.APIError{Code: contract.CodeUnauthenticated, Message: "cannot locate token storage", Hint: storeErr.Error()})
	}
	if len(args) == 2 && args[0] == "auth" && args[1] == "token" {
		value, readErr := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
		if readErr != nil {
			return printStartupError(&contract.APIError{Code: contract.CodeInvalid, Message: "could not read token from stdin", Hint: "pipe the token to agentcell auth token"})
		}
		if writeErr := store.Write(string(value)); writeErr != nil {
			return printStartupError(&contract.APIError{Code: contract.CodeUnauthenticated, Message: "could not store AgentCell token", Hint: contract.Redact(writeErr.Error(), string(value))})
		}
		fmt.Fprintln(os.Stderr, "AgentCell token stored securely")
		return 0
	}
	credential, err := (token.Resolver{Store: store}).Load()
	metaCommand := len(args) == 0 || args[0] == "help"
	if err != nil && !metaCommand {
		return printStartupError(&contract.APIError{Code: contract.CodeUnauthenticated, Message: "AgentCell token is unavailable", Hint: "set AGENTCELL_TOKEN or write the token file in the platform config directory"})
	}
	httpOperations := &operations.HTTPClient{BaseURL: baseURL, Token: credential}
	if len(args) > 0 && args[0] == "mcp" {
		if err := (mcp.Server{Operations: httpOperations}).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
			return printStartupError(&contract.APIError{Code: contract.CodeTransport, Message: "MCP server stopped", Hint: err.Error()})
		}
		return 0
	}
	info, _ := os.Stdout.Stat()
	isTTY := info != nil && info.Mode()&os.ModeCharDevice != 0
	return (cli.Runner{Operations: httpOperations, StdoutTTY: isTTY}).Run(context.Background(), args)
}

func printStartupError(err *contract.APIError) int {
	fmt.Fprintln(os.Stderr, string(err.JSON()))
	return contract.ExitCode(err.Code)
}
