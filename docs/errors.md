# Stable errors and exit codes

Every failure is `{ "code", "message", "hint" }`. Human output uses the same fields, and MCP
returns the same JSON in an `isError` tool result. Codes and process exits are public API:

| code | exit |
|---|---:|
| `usage` | 2 |
| `unauthenticated` | 10 |
| `forbidden` | 11 |
| `not_found` | 12 |
| `conflict` | 13 |
| `invalid_request` | 14 |
| `unsupported_version` | 15 |
| `transport` | 20 |
| `service_unavailable` | 21 |
| `build_failed` | 30 |
| `deploy_failed` | 31 |

Unknown internal errors use exit 1. Agents should branch on `code`, not message text.

