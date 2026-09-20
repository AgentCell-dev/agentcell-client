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
	// SIGNUP.md §1: the login application is one static Access application, at a hostname of
	// its own (`login.agentcell.cloud`), never the API hostname -- so it gets its own override
	// rather than reusing --api-url, the same way the API hostname got its own when it stopped
	// being the tailnet address above.
	loginBaseURL := os.Getenv("AGENTCELL_LOGIN_URL")
	if loginBaseURL == "" {
		loginBaseURL = cli.DefaultLoginBaseURL
	}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--api-url=") {
			baseURL = strings.TrimPrefix(args[i], "--api-url=")
			args = append(args[:i], args[i+1:]...)
			i--
			continue
		}
		if strings.HasPrefix(args[i], "--login-url=") {
			loginBaseURL = strings.TrimPrefix(args[i], "--login-url=")
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
	// LOGIN NEEDS NO EXISTING CREDENTIAL, the same reason `auth token` above is handled before
	// the Load() below: a machine that has never authenticated has no store to load from yet,
	// and Load()'s error would abort the process before this ever ran.
	if len(args) > 0 && args[0] == "login" {
		return runLogin(args[1:], baseURL, loginBaseURL, store)
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
	if len(args) > 0 && args[0] == "logout" {
		return runLogout(baseURL, credential, store)
	}
	if len(args) > 0 && args[0] == "whoami" {
		return runWhoami(baseURL, credential)
	}
	info, _ := os.Stdout.Stat()
	isTTY := info != nil && info.Mode()&os.ModeCharDevice != 0
	return (cli.Runner{Operations: httpOperations, StdoutTTY: isTTY}).Run(context.Background(), args)
}

func printStartupError(err *contract.APIError) int {
	fmt.Fprintln(os.Stderr, string(err.JSON()))
	return contract.ExitCode(err.Code)
}

// runLogin is `agentcell login [--no-browser]` (SIGNUP.md §2, §5). It writes to the token store
// only on success, and the message it prints on success never carries the token -- see
// internal/cli/auth.go's file comment for the property this holds and how it is tested.
func runLogin(args []string, apiBaseURL, loginBaseURL string, store token.Store) int {
	noBrowser := false
	for _, arg := range args {
		if arg != "--no-browser" {
			return printStartupError(&contract.APIError{Code: contract.CodeUsage, Message: "unknown flag " + arg, Hint: "usage: agentcell login [--no-browser]"})
		}
		noBrowser = true
	}
	info, _ := os.Stdout.Stat()
	isTTY := info != nil && info.Mode()&os.ModeCharDevice != 0
	cfg := cli.AuthConfig{
		APIBaseURL: apiBaseURL, LoginBaseURL: loginBaseURL,
		NoBrowser: noBrowser, IsTTY: isTTY,
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	result, apiErr := cli.Login(context.Background(), cfg)
	if apiErr != nil {
		return printStartupError(apiErr.APIError)
	}
	if writeErr := store.Write(result.Token); writeErr != nil {
		return printStartupError(&contract.APIError{Code: contract.CodeUnauthenticated, Message: "could not store AgentCell token", Hint: contract.Redact(writeErr.Error(), result.Token)})
	}
	fmt.Fprintln(os.Stdout, cli.LoginMessage(result))
	return 0
}

// runLogout is `agentcell logout`: revoke server-side, then clear the store either way a dead
// token still means the credential is gone (internal/cli/auth.go's Logout, SIGNUP.md §5).
func runLogout(apiBaseURL, credential string, store token.Store) int {
	cfg := cli.AuthConfig{APIBaseURL: apiBaseURL, Stdout: os.Stdout, Stderr: os.Stderr}
	alreadyDead, apiErr := cli.Logout(context.Background(), cfg, credential)
	if apiErr != nil {
		return printStartupError(apiErr.APIError)
	}
	if deleteErr := store.Delete(); deleteErr != nil {
		return printStartupError(&contract.APIError{Code: contract.CodeUnauthenticated, Message: "could not remove the stored AgentCell token", Hint: deleteErr.Error()})
	}
	if alreadyDead {
		fmt.Fprintln(os.Stdout, "already logged out: the stored token no longer worked; removed it locally")
	} else {
		fmt.Fprintln(os.Stdout, "logged out")
	}
	return 0
}

// runWhoami is `agentcell whoami` (SIGNUP.md §5).
func runWhoami(apiBaseURL, credential string) int {
	cfg := cli.AuthConfig{APIBaseURL: apiBaseURL, Stdout: os.Stdout, Stderr: os.Stderr}
	result, apiErr := cli.Whoami(context.Background(), cfg, credential)
	if apiErr != nil {
		return printStartupError(apiErr.APIError)
	}
	fmt.Fprint(os.Stdout, cli.WhoamiText(result))
	return 0
}
