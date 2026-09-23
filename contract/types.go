package contract

import "context"

const (
	APIVersion       = "2026-08-01"
	APIVersionHeader = "AgentCell-Version"
)

type Request interface{ Operation() string }
type Response interface{ operationResponse() }

// Operations is implemented by the HTTP client and by the control-plane service.
// In-process service tests use the same interface without opening a socket.
type Operations interface {
	Execute(context.Context, Request) (Response, error)
	Stream(context.Context, Request, func(Response) error) error
}

type DeployRequest struct {
	Source         []byte `json:"source_tar_gzip" cli:"source,position=1,archive" description:"Application source directory"`
	CellID         string `json:"cell_id,omitempty" cli:"cell" description:"Stable cell identifier; generated when omitted"`
	Schedule       string `json:"schedule,omitempty" cli:"schedule" description:"Run the container to completion on this cron schedule (5 fields or @hourly/@daily/@weekly/@monthly, UTC, at most every 5 minutes) instead of serving HTTP; fixed for the life of the cell"`
	IdempotencyKey string `json:"idempotency_key" cli:"-" description:"Derived from the source archive and honoured by the service"`
}

func (DeployRequest) Operation() string { return "deploy" }

type DeployResponse struct {
	CellID       string `json:"cell_id"`
	DeploymentID string `json:"deployment_id"`
	URL          string `json:"url"`                   // "" for a scheduled cell: it has no hostname
	Status       string `json:"status"`                // "deployed" | "unchanged" | "scheduled"
	Kind         string `json:"kind,omitempty"`        // "web" | "scheduled"
	Schedule     string `json:"schedule,omitempty"`    // the cron as the service accepted it
	NextRunAt    string `json:"next_run_at,omitempty"` // RFC 3339 UTC, from Nomad's plan
	Port         int    `json:"port,omitempty"`        // the container's port: the Dockerfile's EXPOSE, 8080 when absent
	Hint         string `json:"hint,omitempty"`        // the service's note when a default was used
}

func (DeployResponse) operationResponse() {}

type LogsRequest struct {
	CellID string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Build  bool   `json:"build,omitempty" cli:"build" description:"Read build rather than runtime logs"`
	Follow bool   `json:"follow,omitempty" cli:"follow" description:"Continue streaming new log records"`
}

func (LogsRequest) Operation() string { return "logs" }

type LogEntry struct {
	Time    string `json:"time"`
	Stream  string `json:"stream"`
	Message string `json:"message"`
}

func (LogEntry) operationResponse() {}

type RollbackRequest struct {
	CellID       string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	DeploymentID string `json:"deployment_id,omitempty" cli:"deployment" description:"Deployment to restore; previous when omitted"`
}

func (RollbackRequest) Operation() string { return "rollback" }

type RollbackResponse struct {
	CellID       string `json:"cell_id"`
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
}

func (RollbackResponse) operationResponse() {}

type EnvRequest struct {
	CellID string   `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Action string   `json:"action" cli:"action,required" description:"One of list, set, unset"`
	Values []string `json:"values,omitempty" cli:"value" description:"KEY=VALUE entries"`
}

func (EnvRequest) Operation() string { return "env" }

type EnvResponse struct {
	Values map[string]string `json:"values"`
}

func (EnvResponse) operationResponse() {}

type SecretsRequest struct {
	CellID string   `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Action string   `json:"action" cli:"action,required" description:"One of list, set, unset"`
	Names  []string `json:"names,omitempty" cli:"name" description:"Secret names only; values are read from stdin by future login flow"`
}

func (SecretsRequest) Operation() string { return "secrets" }

type SecretsResponse struct {
	Names []string `json:"names"`
}

func (SecretsResponse) operationResponse() {}

type DomainsRequest struct {
	CellID string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Action string `json:"action" cli:"action,required" description:"One of list, add, remove"`
	Domain string `json:"domain,omitempty" cli:"domain" description:"Custom domain"`
}

func (DomainsRequest) Operation() string { return "domains" }

type DomainsResponse struct {
	Domains []string `json:"domains"`
}

func (DomainsResponse) operationResponse() {}

type ShareRequest struct {
	CellID  string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Subject string `json:"subject" cli:"subject,required" description:"User or group to grant"`
	Role    string `json:"role" cli:"role,required" description:"Granted role"`
}

func (ShareRequest) Operation() string { return "share" }

type ShareResponse struct {
	CellID  string `json:"cell_id"`
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

func (ShareResponse) operationResponse() {}

type AccessRequest struct {
	CellID  string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Action  string `json:"action" cli:"action,required" description:"One of list, revoke"`
	Subject string `json:"subject,omitempty" cli:"subject" description:"User or group to revoke"`
}

func (AccessRequest) Operation() string { return "access" }

type AccessResponse struct {
	Grants []AccessGrant `json:"grants"`
}

func (AccessResponse) operationResponse() {}

type AccessGrant struct {
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

type PSRequest struct {
	All bool `json:"all,omitempty" cli:"all" description:"Include sleeping cells"`
}

func (PSRequest) Operation() string { return "ps" }

type PSResponse struct {
	Cells []CellStatus `json:"cells"`
}

func (PSResponse) operationResponse() {}

type CellStatus struct {
	CellID        string `json:"cell_id"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	Kind          string `json:"kind,omitempty"`
	Schedule      string `json:"schedule,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"` // running | succeeded | failed | timed_out | unplaced | unknown
	LastExitCode  *int   `json:"last_exit_code,omitempty"`  // a pointer, so exit 0 is reportable and absent is absent
	NextRunAt     string `json:"next_run_at,omitempty"`
}

type SpendRequest struct {
	CellID string `json:"cell_id,omitempty" cli:"cell" description:"Cell identifier; all cells when omitted"`
}

func (SpendRequest) Operation() string { return "spend" }

type SpendResponse struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

func (SpendResponse) operationResponse() {}

type DestroyRequest struct {
	CellID  string `json:"cell_id" cli:"cell,required" description:"Cell identifier"`
	Confirm string `json:"confirm" cli:"confirm,required" description:"Must exactly match cell_id"`
}

func (DestroyRequest) Operation() string { return "destroy" }

type DestroyResponse struct {
	CellID string `json:"cell_id"`
	Status string `json:"status"`
}

func (DestroyResponse) operationResponse() {}
