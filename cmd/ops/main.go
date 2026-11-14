// Command ops is the agent-facing ops CLI: GitHub App auth for git/gh and
// dispatch of privileged platform-control workflows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"github.com/n0ci/ops-client/internal/github"
	"github.com/n0ci/ops-client/internal/ops"
)

var version = "dev"

const usage = `ops — GitHub-only ops for this machine (no cloud credentials)

Setup:
  ops login                      store bootstrap bundle (clipboard on macOS, else stdin); configure git
  ops doctor                     check bundle, tokens, access, git helper, tools
  ops clone [--dir DIR]          clone missing inventory repos into the workdir
  ops token [source|dispatch]    print a fresh installation token
  ops gh <args...>               run gh with GH_TOKEN=<source token>
  ops git-credential <get|store|erase>   git credential helper

Executor (platform-control workflows; prints "Run: <url>" first):
  ops plan <stack>               terraform plan at platform main HEAD
  ops apply <stack>              terraform plan + apply at platform main HEAD
  ops deploy <app> [revision]    deploy (empty revision = latest successful build)
  ops rollback <app>
  ops logs <app> [service] [--since 30m]
  ops host <app> <status|ps|df|journal> [target] [--since 30m]
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
	case "gh":
		o, err := load()
		if err != nil {
			return err
		}
		t, err := o.GH.Token(ctx, "source")
		if err != nil {
			return err
		}
		c := exec.CommandContext(ctx, "gh", args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		c.Env = append(os.Environ(), "GH_TOKEN="+t.Value, "GH_HOST=github.com")
		return c.Run()
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
	timeout := fs.Duration("timeout", ops.DefaultTimeout, "overall wait timeout")
	since := fs.String("since", "30m", "log window for logs/host (e.g. 30m, 2h, 1d)")
	dir := fs.String("dir", "", "clone target directory (default: bundle workdir)")
	pos, err := parse(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	opt := ops.WaitOpts{NoWait: *noWait, Timeout: *timeout}

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
		return o.Infra(ctx, out, cmd, at(0), opt)
	case "deploy":
		return o.Deploy(ctx, out, "deploy", at(0), at(1), opt)
	case "rollback":
		return o.Deploy(ctx, out, "rollback", at(0), "", opt)
	case "logs":
		return o.Host(ctx, out, at(0), "logs", at(1), *since, opt)
	case "host":
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
	"plan":     {1, 1, "plan <stack> [--no-wait] [--timeout 45m]"},
	"apply":    {1, 1, "apply <stack> [--no-wait] [--timeout 45m]"},
	"deploy":   {1, 2, "deploy <app> [revision]"},
	"rollback": {1, 1, "rollback <app>"},
	"logs":     {1, 2, "logs <app> [service] [--since 30m]"},
	"host":     {2, 3, "host <app> <status|ps|df|journal> [target]"},
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
