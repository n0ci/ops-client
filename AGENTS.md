# Using `ops` (for coding agents)

This machine has no cloud credentials and no personal GitHub token. All access goes
through `ops`, which authenticates as GitHub Apps. Run it from the shell.

## Rules

- Never ask the user for tokens, keys, passwords or the bootstrap bundle, and never
  print, log or commit tokens (`ops token` output included). If auth fails, run
  `ops doctor` and follow its `fix:` lines; if a fix needs the user (for example
  `ops login`), tell them exactly which command to run.
- When anything fails (git auth, a 401/403/404 from GitHub, a dispatch that never
  shows a run), run `ops doctor` first.
- Do not try to reach OCI, Cloudflare or hosts directly. There is no SSH access; use
  `ops host` / `ops logs`.
- `ops apply`, `ops deploy` and `ops rollback` change production. Run them only when
  the user asked for that change. Run `ops plan` first and report the
  `OPS: changes=` line before you apply.

## Which command

| Goal | Command |
|---|---|
| Get the repos | `ops clone` (into `~/repo`; skips existing ones) |
| git clone/fetch/push | plain `git` over https; `ops` is the credential helper |
| PRs, issues, app-repo workflows | `ops gh pr create ...`, `ops gh run list ...` |
| Token for a script | `GH_TOKEN=$(ops token) some-tool` (keep it in the environment, never on disk) |
| Infra change | edit `n0ci/platform` (`stacks/<name>/inputs.tfvars.json`), push to main, then `ops plan <stack>`, then `ops apply <stack>` |
| Deploy | `ops deploy <app> [40-hex sha]` (no sha = latest successful build on main) |
| Undo a deploy | `ops rollback <app>` |
| Look at a host | `ops host <app> status\|ps\|df\|journal [unit]`, `ops logs <app> [service] --since 1h` |
| What's going on | `ops status` (recent executor runs + app health) |
| A run is taking long | `ops <cmd> --no-wait`, then later `ops run <run-id or URL>` |

Apps: `abfahrt`, `imoutfyi`. Stacks: see `stacks:` in `n0ci/platform/inventory.yaml`.

## Reading results

- `Run: <url>` comes first; include it in your report to the user.
- Result lines look like `OPS: key=value`; the last one is `OPS: result=success|failure|noop`.
- Host output is printed between `--- output ---` and `--- end ---`.
- Exit status 0 means the run concluded `success`. Anything else is a failure: report
  the run URL and the `OPS:` lines. Executor runs can only be read through `ops`
  (`ops run <id>`). `ops gh` uses the source app, which has no access to platform-control.

## MCP

`ops mcp` serves the same operations as MCP tools (`ops_status`, `ops_plan`,
`ops_apply`, `ops_deploy`, `ops_rollback`, `ops_logs`, `ops_host`, `ops_run`) over
stdio. `ops setup-agents` registers it with Claude Code, Codex and OpenCode (the
bootstrap runs it; re-running is safe, `--dry-run` previews). If the `ops_*` tools
are missing in your session, check the `MCP registered:` line of `ops doctor`, run
`ops setup-agents`, and tell the user to restart the agent; until then use the CLI.
The same rules apply to the tools as to the commands (apply/deploy/rollback only
when asked).
