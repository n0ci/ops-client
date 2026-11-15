// Command ops is the agent-facing ops CLI: GitHub App auth for git/gh and
// dispatch of privileged platform-control workflows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"

	"github.com/n0ci/ops-client/internal/github"
	"github.com/n0ci/ops-client/internal/ops"
)

var version = "dev"

const usage = `ops — GitHub-only ops for this machine (no cloud credentials)

Admin actions (--admin, host exec) are logged to the "Agent activity" issue
in platform-control.

Setup:
  ops login                      store bootstrap bundle (clipboard on macOS, else stdin); configure git
  ops doctor                     check bundle, tokens, access, git helper, tools
  ops clone [--dir DIR]          clone missing inventory repos into the workdir
                                 (~/repo/ops; override with $OPS_WORKDIR)
  ops token [source|dispatch]    print a fresh installation token
  ops gh [--] <args...>          run gh with GH_TOKEN=<source token> (all repos, incl. secrets/settings)
  ops git-credential <get|store|erase>   git credential helper
  ops setup-agents [--dry-run]   register "ops mcp" with Claude Code (project scope:
                                 <workdir>/.mcp.json), Codex, OpenCode (user-wide)

Executor (platform-control workflows; prints "Run: <url>" first):
  ops plan <stack> [--admin [--allow-cost]]
                                 terraform plan at platform main HEAD; prints the plan
                                 id (OPS: plan_run=) and every address it destroys or
                                 replaces
  ops apply <stack> --plan ID [--admin --confirm-destroy N]
                                 apply exactly that plan (refused if older than 1h or
                                 if platform/platform-control main or the state moved);
                                 --admin allows deletes, replaces and IAM; N must equal
                                 the plan's destroy count. Without --plan: only plans.
                                 --allow-cost (admin plans): may exceed the stack's
                                 free storage budget, which costs money
  ops deploy <app> [revision] [--wait]
                                 deploy (empty revision = latest successful build);
                                 --wait (abfahrt): keep waiting up to 5 h while an
                                 import defers the release (without it: state=pending,
                                 non-zero exit)
  ops rollback <app> [--wait]
  ops logs <app> [service] [--since 30m]
  ops host <app> <status|ps|df|journal|errors> [target] [--since 30m]
  ops host <app> exec <file|->   run a bash script as root on the host (admin)
  ops run <run-id|url>           reattach to a run and print its results
  ops status                     recent runs + app health
  ops mcp                        MCP stdio server exposing the commands as tools
  ops version

Flags for executor commands: --no-wait, --timeout 45m
`

func main() {
	github.UserAgent = "ops-client/" + version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "ops:", err)
		os.Exit(1)
	}
}

// parse parses flags that may appear before, between or after positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	out := os.Stdout

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "login":
		return ops.Login(ctx, os.Stdin, out)
	case "doctor":
		if !ops.Doctor(ctx, out) {
			return errors.New("doctor: required checks failed")
		}
		return nil
	case "setup-agents":
		fs := flag.NewFlagSet("ops setup-agents", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "print what would change without changing anything")
		pos, err := parse(fs, args)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return errors.New("usage: ops setup-agents [--dry-run]")
		}
		env, err := ops.DefaultAgentEnv()
		if err != nil {
			return err
		}
		return ops.SetupAgents(ctx, out, env, *dry)
	case "gh":
		o, err := load()
		if err != nil {
			return err
		}
		return o.RunGH(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	case "git-credential":
		if len(args) != 1 {
			return errors.New("usage: ops git-credential <get|store|erase>")
		}
		return ops.GitCredential(ctx, args[0], os.Stdin, out, func(ctx context.Context) (github.Token, error) {
			o, err := load()
			if err != nil {
				return github.Token{}, err
			}
			return o.GH.Token(ctx, "source")
		})
	}

	fs := flag.NewFlagSet("ops "+cmd, flag.ContinueOnError)
	noWait := fs.Bool("no-wait", false, "return right after dispatch")
	waitPending := fs.Bool("wait", false, "deploy/rollback abfahrt: keep waiting (up to 5 h) while an import defers the release")
	timeout := fs.Duration("timeout", ops.DefaultTimeout, "overall wait timeout")
	since := fs.String("since", "30m", "log window for logs/host (e.g. 30m, 2h, 1d)")
	dir := fs.String("dir", "", "clone target directory (default: the workdir, ~/repo/ops or $OPS_WORKDIR)")
	admin := fs.Bool("admin", false, "plan/apply: admin mode (deletes, IAM, any resource type)")
	confirm := fs.Int("confirm-destroy", 0, "plan/apply --admin: number of resources the plan may destroy")
	allowCost := fs.Bool("allow-cost", false, "plan --admin: allow exceeding the stack's free storage budget (costs money)")
	planRun := fs.String("plan", "", "apply: run id of the plan to apply (printed by ops plan)")
	pos, err := parse(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	opt := ops.WaitOpts{NoWait: *noWait, Timeout: *timeout, Pending: *waitPending}
	if *waitPending && cmd != "deploy" && cmd != "rollback" {
		return errors.New("--wait applies only to deploy/rollback")
	}
	if (*admin || *confirm != 0) && cmd != "plan" && cmd != "apply" {
		return errors.New("--admin and --confirm-destroy apply only to plan/apply")
	}
	if *allowCost && cmd != "plan" && cmd != "apply" {
		return errors.New("--allow-cost applies only to plan/apply")
	}
	if *planRun != "" && cmd != "apply" {
		return errors.New("--plan applies only to apply")
	}

	spec, ok := arity[cmd]
	if !ok {
		return fmt.Errorf("unknown command %q (see `ops help`)", cmd)
	}
	if len(pos) < spec.min || len(pos) > spec.max {
		return fmt.Errorf("usage: ops %s", spec.use)
	}
	at := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	o, err := load()
	if err != nil {
		return err
	}

	switch cmd {
	case "clone":
		return o.Clone(ctx, out, *dir)
	case "token":
		app := at(0)
		if app == "" {
			app = "source"
		}
		if app != "source" && app != "dispatch" {
			return fmt.Errorf("unknown app %q (source|dispatch)", app)
		}
		t, err := o.GH.Token(ctx, app)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, t.Value)
		return nil
	case "plan", "apply":
		return o.Infra(ctx, out, cmd, at(0), ops.InfraMode{Admin: *admin, ConfirmDestroy: *confirm, PlanRun: *planRun, AllowCost: *allowCost}, opt)
	case "deploy":
		return o.Deploy(ctx, out, "deploy", at(0), at(1), opt)
	case "rollback":
		return o.Deploy(ctx, out, "rollback", at(0), "", opt)
	case "logs":
		return o.Host(ctx, out, at(0), "logs", at(1), *since, opt)
	case "host":
		if at(1) == "exec" {
			script, err := readScript(at(2))
			if err != nil {
				return err
			}
			return o.HostExec(ctx, out, at(0), script, opt)
		}
		return o.Host(ctx, out, at(0), at(1), at(2), *since, opt)
	case "run":
		return o.Reattach(ctx, out, at(0), *timeout)
	case "status":
		return o.Status(ctx, out)
	case "mcp":
		return o.MCPServer(version).Serve(ctx, os.Stdin, out)
	}
	return nil
}

var arity = map[string]struct {
	min, max int
	use      string
}{
	"clone":    {0, 0, "clone [--dir DIR]"},
	"token":    {0, 1, "token [source|dispatch]"},
	"plan":     {1, 1, "plan <stack> [--admin [--allow-cost] [--confirm-destroy N]] [--no-wait] [--timeout 45m]"},
	"apply":    {1, 1, "apply <stack> --plan ID [--admin --confirm-destroy N] [--no-wait] [--timeout 45m]"},
	"deploy":   {1, 2, "deploy <app> [revision] [--wait]"},
	"rollback": {1, 1, "rollback <app> [--wait]"},
	"logs":     {1, 2, "logs <app> [service] [--since 30m]"},
	"host":     {2, 3, "host <app> <status|ps|df|journal|errors> [target] | host <app> exec <file|->"},
	"run":      {1, 1, "run <run-id|url> [--timeout 45m]"},
	"status":   {0, 0, "status"},
	"mcp":      {0, 0, "mcp"},
}

func load() (*ops.Ops, error) {
	b, err := ops.LoadBundle()
	if err != nil {
		return nil, err
	}
	return ops.New(b), nil
}

// readScript reads an exec script from a file, or from stdin for "-".
func readScript(path string) ([]byte, error) {
	switch path {
	case "":
		return nil, errors.New("usage: ops host <app> exec <file|->")
	case "-":
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
