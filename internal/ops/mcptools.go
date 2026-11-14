package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/n0ci/ops-client/internal/mcp"
)

func schema(required []string, props map[string]map[string]any) map[string]any {
	if props == nil {
		props = map[string]map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func boolean(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }

func arg(a map[string]any, k string) string {
	switch v := a[k].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}

var (
	appProp    = map[string]any{"type": "string", "enum": []string{"abfahrt", "imoutfyi"}}
	noWaitProp = boolean("return right after dispatch (the run URL is still returned); reattach with ops_run")
)

// MCPServer exposes the ops commands as MCP tools. Each tool runs the same
// functions as the CLI and returns their output as text.
func (o *Ops) MCPServer(version string) *mcp.Server {
	wait := func(a map[string]any) WaitOpts { b, _ := a["no_wait"].(bool); return WaitOpts{NoWait: b} }
	run := func(f func(ctx context.Context, sb *strings.Builder, a map[string]any) error) func(context.Context, map[string]any) (string, error) {
		return func(ctx context.Context, a map[string]any) (string, error) {
			var sb strings.Builder
			err := f(ctx, &sb, a)
			return strings.TrimRight(sb.String(), "\n"), err
		}
	}
	return &mcp.Server{Name: "ops", Version: version, Tools: []mcp.Tool{
		{
			Name: "ops_status", Description: "Recent platform-control executor runs and app health checks.",
			InputSchema: schema(nil, nil),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Status(ctx, sb)
			}),
		},
		{
			Name: "ops_plan", Description: "Terraform plan for a stack at platform main HEAD (runs in platform-control). Returns OPS: changes=add,change,destroy.",
			InputSchema: schema([]string{"stack"}, map[string]map[string]any{"stack": str("stack name, e.g. abfahrt"), "no_wait": noWaitProp}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Infra(ctx, sb, "plan", arg(a, "stack"), wait(a))
			}),
		},
		{
			Name: "ops_apply", Description: "Terraform plan+apply for a stack at platform main HEAD. Mutates infrastructure; run ops_plan first.",
			InputSchema: schema([]string{"stack"}, map[string]map[string]any{"stack": str("stack name"), "no_wait": noWaitProp}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Infra(ctx, sb, "apply", arg(a, "stack"), wait(a))
			}),
		},
		{
			Name: "ops_deploy", Description: "Deploy an app revision (40-hex SHA; empty = latest successful build on main).",
			InputSchema: schema([]string{"app"}, map[string]map[string]any{"app": appProp, "revision": str("40-hex commit SHA or empty"), "no_wait": noWaitProp}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Deploy(ctx, sb, "deploy", arg(a, "app"), arg(a, "revision"), wait(a))
			}),
		},
		{
			Name: "ops_rollback", Description: "Roll an app back to its previous deployment.",
			InputSchema: schema([]string{"app"}, map[string]map[string]any{"app": appProp, "no_wait": noWaitProp}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Deploy(ctx, sb, "rollback", arg(a, "app"), "", wait(a))
			}),
		},
		{
			Name: "ops_logs", Description: "Read recent service logs from an app host (read-only).",
			InputSchema: schema([]string{"app"}, map[string]map[string]any{"app": appProp, "service": str("service/unit name (optional)"), "since": str("e.g. 30m, 2h, 1d (default 30m)")}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Host(ctx, sb, arg(a, "app"), "logs", arg(a, "service"), arg(a, "since"), WaitOpts{})
			}),
		},
		{
			Name: "ops_host", Description: "Read-only host diagnostics: status, ps, df, journal (or logs).",
			InputSchema: schema([]string{"app", "command"}, map[string]map[string]any{
				"app": appProp, "command": {"type": "string", "enum": []string{"status", "ps", "df", "journal", "logs"}},
				"target": str("service/unit name (optional)"), "since": str("e.g. 30m (default 30m)"),
			}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Host(ctx, sb, arg(a, "app"), arg(a, "command"), arg(a, "target"), arg(a, "since"), WaitOpts{})
			}),
		},
		{
			Name: "ops_run", Description: "Reattach to a platform-control run by id or URL: wait for completion and return its OPS: lines.",
			InputSchema: schema([]string{"run_id"}, map[string]map[string]any{"run_id": str("run id or run URL")}),
			Handler: run(func(ctx context.Context, sb *strings.Builder, a map[string]any) error {
				return o.Reattach(ctx, sb, arg(a, "run_id"), 0)
			}),
		},
	}}
}
