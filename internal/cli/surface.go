package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/AgentCell-dev/agentcell-client/contract"
	archivepkg "github.com/AgentCell-dev/agentcell-client/internal/archive"
)

type Command struct {
	Name, Summary          string
	Streaming, Destructive bool
}

func Commands(definitions []contract.Definition) []Command {
	commands := make([]Command, 0, len(definitions))
	for _, definition := range definitions {
		commands = append(commands, Command{definition.Name, definition.Summary, definition.Streaming, definition.Destructive})
	}
	return commands
}

func Parse(definition contract.Definition, args []string) (contract.Request, *contract.APIError) {
	values := map[string][]string{}
	positionals := []string{}
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name := strings.TrimPrefix(arg, "--")
		if before, after, ok := strings.Cut(name, "="); ok {
			values[before] = append(values[before], after)
			continue
		}
		field, boolField := fieldForCLIName(definition.RequestType, name)
		if field == nil {
			return nil, usage("unknown flag --"+name, "run agentcell help "+definition.Name)
		}
		if boolField {
			values[name] = append(values[name], "true")
			continue
		}
		if len(args) == 0 {
			return nil, usage("flag --"+name+" needs a value", "run agentcell help "+definition.Name)
		}
		values[name] = append(values[name], args[0])
		args = args[1:]
	}
	return Bind(definition, values, positionals)
}

// Bind is shared by the CLI and MCP adapter, so their argument semantics cannot drift.
func Bind(definition contract.Definition, values map[string][]string, positionals []string) (contract.Request, *contract.APIError) {
	request := contract.NewRequest(definition)
	v := reflect.Indirect(reflect.ValueOf(request))
	for index := 0; index < definition.RequestType.NumField(); index++ {
		fieldType := definition.RequestType.Field(index)
		cliTag := fieldType.Tag.Get("cli")
		if cliTag == "" || cliTag == "-" {
			continue
		}
		parts := strings.Split(cliTag, ",")
		name := parts[0]
		required, archive, position := has(parts, "required"), has(parts, "archive"), -1
		for _, part := range parts {
			if strings.HasPrefix(part, "position=") {
				position, _ = strconv.Atoi(strings.TrimPrefix(part, "position="))
			}
		}
		var raw []string
		if position > 0 && len(positionals) >= position {
			raw = []string{positionals[position-1]}
		} else {
			raw = values[name]
		}
		if len(raw) == 0 {
			if required {
				return nil, usage("missing --"+name, "run agentcell help "+definition.Name)
			}
			continue
		}
		field := v.Field(index)
		if archive {
			data, key, err := archivepkg.Directory(raw[len(raw)-1])
			if err != nil {
				return nil, &contract.APIError{Code: contract.CodeInvalid, Message: "could not archive source directory", Hint: err.Error()}
			}
			field.SetBytes(data)
			if keyField := v.FieldByName("IdempotencyKey"); keyField.IsValid() {
				keyField.SetString(key)
			}
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString(raw[len(raw)-1])
		case reflect.Bool:
			parsed, err := strconv.ParseBool(raw[len(raw)-1])
			if err != nil {
				return nil, usage("invalid boolean for --"+name, "use true or false")
			}
			field.SetBool(parsed)
		case reflect.Slice:
			field.Set(reflect.ValueOf(raw))
		default:
			return nil, usage("unsupported field --"+name, "report this client bug")
		}
	}
	if len(positionals) > positionalCount(definition.RequestType) {
		return nil, usage("too many positional arguments", "run agentcell help "+definition.Name)
	}
	if err := contract.ValidateDestructive(definition, request); err != nil {
		return nil, err
	}
	return request, nil
}

func JSONValues(arguments map[string]any) (map[string][]string, *contract.APIError) {
	values := make(map[string][]string, len(arguments))
	for key, value := range arguments {
		switch item := value.(type) {
		case string:
			values[key] = []string{item}
		case bool:
			values[key] = []string{strconv.FormatBool(item)}
		case []any:
			for _, element := range item {
				text, ok := element.(string)
				if !ok {
					return nil, usage("array "+key+" must contain strings", "inspect the tool input schema")
				}
				values[key] = append(values[key], text)
			}
		default:
			b, _ := json.Marshal(item)
			return nil, usage("unsupported value for "+key+": "+string(b), "inspect the tool input schema")
		}
	}
	return values, nil
}

// SessionCommand is one of the six commands cmd/agentcell dispatches BEFORE it ever builds a
// contract.Request: login, logout, whoami, auth token, mcp and version. None of them is a
// contract.Definition -- login runs before a bearer token exists to build one with, mcp opens a
// stdio server rather than making a request, and version needs no request or response type at all
// -- so none of them showed up in Help() or had a Run() `help <name>` entry: `agentcell help`
// listed only the eleven operations and `agentcell help login` fell through to that same generic
// listing. This is their visibility, kept beside Commands()/Help()/OperationHelp() rather than
// duplicated in cmd/agentcell, which is the one place that actually dispatches them.
type SessionCommand struct {
	// Name is the lookup key: what a caller types as `agentcell <Name>` and `agentcell help
	// <Name>`. "auth" rather than "auth token", because "agentcell help auth" is what a reader
	// who has seen "auth token" in the summary line actually types.
	Name, Summary, Usage string
}

// SessionCommands is the fixed list, in the get-started order: login before anything that needs a
// stored token, whoami/logout beside it, the machine-token path, then the two standalone commands.
var SessionCommands = []SessionCommand{
	{
		Name: "login", Summary: "Sign in as a person and store an API token (browser, or --no-browser for a device code)",
		Usage: "Usage: agentcell login [--no-browser]\n\n" +
			"Opens a browser to sign in (Google, GitHub, or a one-time email PIN) and stores the API\n" +
			"token this CLI receives; the token itself is never printed. --no-browser prints a URL and\n" +
			"an eight-character code to type on any device with a browser instead. First login creates\n" +
			"a personal organisation. See docs/credentials.md.\n",
	},
	{
		Name: "logout", Summary: "Revoke the stored token and remove it locally",
		Usage: "Usage: agentcell logout\n\n" +
			"Revokes the current token server-side, then removes it from local storage either way -- a\n" +
			"token the server had already invalidated is still removed locally.\n",
	},
	{
		Name: "whoami", Summary: "Show who the stored token is signed in as",
		Usage: "Usage: agentcell whoami [--output=auto|human|json]\n\n" +
			"Prints email, org, plan, scopes, how the token was issued, and the token's prefix -- never\n" +
			"the token itself. Human-readable on a terminal, JSON otherwise, the same rule every other\n" +
			"command follows.\n",
	},
	{
		Name: "auth", Summary: "Store a token piped on stdin (auth token; a machine credential)",
		Usage: "Usage: agentcell auth token\n\n" +
			"Reads a token from stdin and stores it -- for a machine credential an operator minted\n" +
			"(`make cp-token-issue`), not for a person (use `agentcell login` instead). Contacts no\n" +
			"service; storage only.\n",
	},
	{
		Name: "mcp", Summary: "Run the MCP server over stdio for coding agents",
		Usage: "Usage: agentcell mcp\n\n" +
			"Serves the same operations `agentcell` exposes as CLI commands as MCP tools over stdio, for\n" +
			"a coding agent's harness to call. Needs a stored token first (agentcell login or agentcell\n" +
			"auth token). See docs/mcp-harness.md.\n",
	},
	{
		Name: "version", Summary: "Print the client and API version",
		Usage: "Usage: agentcell version\n\n" +
			"Prints the client's build version and the API version this build speaks (the\n" +
			"AgentCell-Version header every request carries). Contacts no service and needs no token.\n",
	},
}

// SessionCommandHelp looks up name's per-command usage, as SessionCommands' Name field (so "auth",
// not "auth token"). The second result is false for anything not in that fixed list.
func SessionCommandHelp(name string) (string, bool) {
	for _, command := range SessionCommands {
		if command.Name == name {
			return command.Usage, true
		}
	}
	return "", false
}

// sessionCommandLabel is what Help() prints in its listing column: "auth token" rather than just
// "auth", so the summary line reads the same as what a caller actually types.
func sessionCommandLabel(command SessionCommand) string {
	if command.Name == "auth" {
		return "auth token"
	}
	return command.Name
}

func Help(definitions []contract.Definition) string {
	var b strings.Builder
	b.WriteString("Usage: agentcell [--output=auto|human|json] <operation> [arguments]\n\nOperations:\n")
	for _, command := range Commands(definitions) {
		fmt.Fprintf(&b, "  %-10s %s\n", command.Name, command.Summary)
	}
	b.WriteString("\nGetting started / session:\n")
	for _, command := range SessionCommands {
		fmt.Fprintf(&b, "  %-10s %s\n", sessionCommandLabel(command), command.Summary)
	}
	b.WriteString("\nRun 'agentcell help <operation>' for arguments; 'agentcell help <command>' for the session commands above.\n")
	return b.String()
}

func OperationHelp(definition contract.Definition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: agentcell %s", definition.Name)
	for i := 0; i < definition.RequestType.NumField(); i++ {
		field := definition.RequestType.Field(i)
		tag := field.Tag.Get("cli")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		if has(parts, "position=1") {
			fmt.Fprintf(&b, " <%s>", parts[0])
		} else {
			fmt.Fprintf(&b, " [--%s]", parts[0])
		}
	}
	fmt.Fprintf(&b, "\n\n%s\n", contract.ToolDescription(definition))
	for i := 0; i < definition.RequestType.NumField(); i++ {
		field := definition.RequestType.Field(i)
		tag := field.Tag.Get("cli")
		if tag == "" || tag == "-" {
			continue
		}
		fmt.Fprintf(&b, "  --%-14s %s\n", strings.Split(tag, ",")[0], field.Tag.Get("description"))
	}
	return b.String()
}

func usage(message, hint string) *contract.APIError {
	return &contract.APIError{Code: contract.CodeUsage, Message: message, Hint: hint}
}
func has(parts []string, wanted string) bool {
	for _, part := range parts {
		if part == wanted {
			return true
		}
	}
	return false
}
func positionalCount(t reflect.Type) int {
	count := 0
	for i := 0; i < t.NumField(); i++ {
		if strings.Contains(t.Field(i).Tag.Get("cli"), "position=") {
			count++
		}
	}
	return count
}
func fieldForCLIName(t reflect.Type, name string) (*reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if strings.Split(field.Tag.Get("cli"), ",")[0] == name {
			return &field, field.Type.Kind() == reflect.Bool
		}
	}
	return nil, false
}
