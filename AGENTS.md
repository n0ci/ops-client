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
  `ops host` / `ops logs`, and `ops host <app> exec` for root work.
- You have the owner's standing authority to change production (apply, deploy,
  rollback, admin applies, host exec, GitHub settings and secrets). Run `ops plan`
  first, report its `OPS: changes=` and any `OPS: destroy=`/`replace=` lines, then
  apply that exact plan with `ops apply <stack> --plan <id>` (id from `OPS: plan_run=`).
- Before anything destructive or irreversible (deleting resources or data, DB resets,
  replacing a VM): say what will be lost, use `--admin --confirm-destroy N` with the
  exact destroy count from the plan, and afterwards check the run's entry in the
  "Agent activity" issue of platform-control.

## Which command

| Goal | Command |
|---|---|
| Get the repos | `ops clone` (into `~/repo`; skips existing ones) |
| git clone/fetch/push | plain `git` over https; `ops` is the credential helper |
| PRs, issues, workflows, secrets, settings (any repo, platform-control too) | `ops gh -- pr create ...`, `ops gh -- secret set NAME -R n0ci/<repo>` |
| Token for a script | `GH_TOKEN=$(ops token) some-tool` (keep it in the environment, never on disk) |
| Infra change | edit `n0ci/platform` (`stacks/<name>/inputs.tfvars.json`), push to main, then `ops plan <stack>`, then `ops apply <stack> --plan <id>` within 1 h (a newer commit or state change refuses it: re-plan) |
| Deletes, replaces, IAM | `ops plan <stack> --admin`, then `ops apply <stack> --plan <id> --admin --confirm-destroy N` |
| Executor/Terraform code | PR to `n0ci/platform-control`; merge after its CI passes |
| Deploy | `ops deploy <app> [40-hex sha]` (no sha = latest successful build on main) |
| Undo a deploy | `ops rollback <app>` |
| Look at a host | `ops host <app> status\|ps\|df\|journal [unit]\|errors`, `ops logs <app> [service] --since 1h` |
| Root on a host | `ops host <app> exec script.sh` (or `-` for stdin), bash as root |
| What's going on | `ops status` (recent executor runs + app health) |
| A run is taking long | `ops <cmd> --no-wait`, then later `ops run <run-id or URL>` |

Apps: `abfahrt`, `imoutfyi`. Stacks: see `stacks:` in `n0ci/platform/inventory.yaml`.

## Reading results

- `Run: <url>` comes first; include it in your report to the user.
- Result lines look like `OPS: key=value`; the last one is `OPS: result=success|failure|noop`.
- Host output is printed between `--- output ---` and `--- end ---`.
- Exit status 0 means the run concluded `success`. Anything else is a failure: report
  the run URL and the `OPS:` lines. Executor runs can only be read through `ops`
  (`ops run <id>`).

## MCP

`ops mcp` serves the same operations as MCP tools (`ops_status`, `ops_plan`,
`ops_apply`, `ops_deploy`, `ops_rollback`, `ops_logs`, `ops_host`, `ops_gh`, `ops_run`) over
stdio. `ops setup-agents` registers it with Claude Code, Codex and OpenCode (the
bootstrap runs it; re-running is safe, `--dry-run` previews). If the `ops_*` tools
are missing in your session, check the `MCP registered:` line of `ops doctor`, run
`ops setup-agents`, and tell the user to restart the agent; until then use the CLI.
The same rules apply to the tools as to the commands.
