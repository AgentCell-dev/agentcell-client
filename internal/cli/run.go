package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

type Runner struct {
	Operations contract.Operations
	Out        io.Writer
	Err        io.Writer
	StdoutTTY  bool
}

func (r Runner) Run(ctx context.Context, args []string) int {
	output, remaining, apiErr := extractOutput(args)
	if apiErr != nil {
		return r.renderError(apiErr, output)
	}
	if len(remaining) == 0 || remaining[0] == "help" {
		if len(remaining) > 1 {
			if definition, ok := contract.Lookup(remaining[1]); ok {
				fmt.Fprint(r.out(), OperationHelp(definition))
				return 0
			}
			if usage, ok := SessionCommandHelp(remaining[1]); ok {
				fmt.Fprint(r.out(), usage)
				return 0
			}
		}
		fmt.Fprint(r.out(), Help(contract.Definitions))
		return 0
	}
	definition, ok := contract.Lookup(remaining[0])
	if !ok {
		return r.renderError(usage("unknown operation "+remaining[0], "run agentcell help"), output)
	}
	request, apiErr := Parse(definition, remaining[1:])
	if apiErr != nil {
		return r.renderError(apiErr, output)
	}
	mode := output
	if mode == "auto" {
		if r.StdoutTTY {
			mode = "human"
		} else {
			mode = "json"
		}
	}
	if definition.Streaming {
		err := r.Operations.Stream(ctx, request, func(response contract.Response) error { return render(r.out(), mode, response) })
		if err != nil {
			return r.renderError(contract.AsAPIError(err), mode)
		}
		return 0
	}
	response, err := r.Operations.Execute(ctx, request)
	if err != nil {
		return r.renderError(contract.AsAPIError(err), mode)
	}
	if err := render(r.out(), mode, response); err != nil {
		return r.renderError(&contract.APIError{Code: contract.CodeService, Message: "could not render output", Hint: "retry with --output=json"}, mode)
	}
	return 0
}

// ExtractOutput is extractOutput, exported so cmd/agentcell can honour --output=auto|human|json
// for the session commands (whoami, ...) it dispatches before ever building a Runner -- the same
// flag, parsed the same way, rather than a second copy that could drift from this one.
func ExtractOutput(args []string) (string, []string, *contract.APIError) { return extractOutput(args) }

func extractOutput(args []string) (string, []string, *contract.APIError) {
	mode := "auto"
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--output=") {
			mode = strings.TrimPrefix(arg, "--output=")
			continue
		}
		if arg == "--output" {
			if i+1 >= len(args) {
				return mode, nil, usage("--output needs a value", "use auto, human or json")
			}
			i++
			mode = args[i]
			continue
		}
		remaining = append(remaining, arg)
	}
	if mode != "auto" && mode != "human" && mode != "json" {
		return mode, nil, usage("invalid output mode", "use auto, human or json")
	}
	return mode, remaining, nil
}

func render(w io.Writer, mode string, value any) error {
	if mode == "json" {
		return json.NewEncoder(w).Encode(value)
	}
	v := reflect.Indirect(reflect.ValueOf(value))
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		// A field the service may omit is not printed as its zero value: a replay or a scheduled
		// cell has no port, and "port: 0" would be a number nothing listens on.
		if strings.Contains(tag, ",omitempty") && v.Field(i).IsZero() {
			continue
		}
		field := v.Field(i).Interface()
		switch typed := field.(type) {
		case string:
			if typed != "" {
				fmt.Fprintf(w, "%s: %s\n", name, typed)
			}
		case map[string]string:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(w, "%s=%s\n", key, typed[key])
			}
		default:
			b, _ := json.Marshal(field)
			fmt.Fprintf(w, "%s: %s\n", name, b)
		}
	}
	return nil
}

func (r Runner) renderError(err *contract.APIError, mode string) int {
	if mode == "auto" {
		if r.StdoutTTY {
			mode = "human"
		} else {
			mode = "json"
		}
	}
	if mode == "json" {
		fmt.Fprintln(r.err(), string(err.JSON()))
	} else {
		fmt.Fprintln(r.err(), err.Human())
	}
	return contract.ExitCode(err.Code)
}
func (r Runner) out() io.Writer {
	if r.Out != nil {
		return r.Out
	}
	return os.Stdout
}
func (r Runner) err() io.Writer {
	if r.Err != nil {
		return r.Err
	}
	return os.Stderr
}
