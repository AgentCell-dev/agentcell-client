package contract

import (
	"reflect"
	"strings"
)

// Definition is the single declaration from which CLI commands, MCP tools and
// HTTP routes are derived. Request and response types remain concrete Go types.
type Definition struct {
	Name         string
	Summary      string
	RequestType  reflect.Type
	ResponseType reflect.Type
	Streaming    bool
	Destructive  bool
}

func define[Req Request, Resp Response](name, summary string, streaming, destructive bool) Definition {
	return Definition{Name: name, Summary: summary, RequestType: reflect.TypeFor[Req](), ResponseType: reflect.TypeFor[Resp](), Streaming: streaming, Destructive: destructive}
}

// Definitions is deliberately the only verb list in the codebase.
var Definitions = []Definition{
	define[DeployRequest, DeployResponse]("deploy", "Archive a source directory and deploy it as a cell", false, false),
	define[LogsRequest, LogEntry]("logs", "Stream build or runtime logs for a cell", true, false),
	define[RollbackRequest, RollbackResponse]("rollback", "Restore a previous successful deployment", false, false),
	define[EnvRequest, EnvResponse]("env", "List or change non-secret environment variables", false, false),
	define[SecretsRequest, SecretsResponse]("secrets", "List or change secret names without returning secret values", false, false),
	define[DomainsRequest, DomainsResponse]("domains", "List or change custom domains", false, false),
	define[ShareRequest, ShareResponse]("share", "Grant a user or group access to a cell", false, false),
	define[AccessRequest, AccessResponse]("access", "List or revoke access grants", false, false),
	define[PSRequest, PSResponse]("ps", "List running and sleeping cells", false, false),
	define[SpendRequest, SpendResponse]("spend", "Report consumption spend", false, false),
	define[DestroyRequest, DestroyResponse]("destroy", "Destroy a cell while retaining recoverable backups", false, true),
}

func Lookup(name string) (Definition, bool) {
	for _, definition := range Definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}

func NewRequest(definition Definition) Request {
	return reflect.New(definition.RequestType).Interface().(Request)
}

func NewResponse(definition Definition) Response {
	return reflect.New(definition.ResponseType).Interface().(Response)
}

func ValidateDestructive(definition Definition, request Request) *APIError {
	if !definition.Destructive {
		return nil
	}
	v := reflect.Indirect(reflect.ValueOf(request))
	confirm := v.FieldByName("Confirm")
	cell := v.FieldByName("CellID")
	if confirm.IsValid() && cell.IsValid() && confirm.String() != cell.String() {
		return &APIError{Code: CodeInvalid, Message: "destructive operation was not confirmed", Hint: "set confirm to the exact cell_id"}
	}
	return nil
}

func ToolDescription(definition Definition) string {
	description := definition.Summary
	if definition.Streaming {
		description += ". Returns a stream."
	}
	if definition.Destructive {
		description += ". Destructive: requires a separately scoped token; confirmation is enforced where present."
	}
	return strings.TrimSpace(description)
}
