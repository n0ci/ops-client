# ops-client

`ops` is a small CLI that lets a machine (and the coding agents on it) work on the
n0ci systems through GitHub alone: clone and push via a GitHub App, and ask the
private `n0ci/platform-control` workflows to plan/apply Terraform, deploy, and read
host diagnostics. No cloud credentials ever live on the machine. The interface with
the executor is specified in `platform-control/docs/CONTRACT.md`.

## Fresh machine

1. Copy the "ops bootstrap" note from iCloud Passwords to the clipboard.
2. `curl -fsSL https://raw.githubusercontent.com/n0ci/ops-client/main/bootstrap.sh | sh`
3. Add `~/.local/bin` to `PATH` if the script warns, then `ops doctor`.

The script installs a pinned, checksum-verified release, then runs `ops login`,
`ops clone`, `ops doctor` and `ops setup-agents`.

## Commands

```
ops login | doctor | clone [--dir DIR] | status | version
ops setup-agents [--dry-run]
ops token [source|dispatch]          ops gh [--] <args...>
ops git-credential <get|store|erase> (configured by `ops login`)
ops plan <stack> [--admin]           ops apply <stack> --plan ID [--admin --confirm-destroy N]
ops deploy <app> [revision]          ops rollback <app>
ops logs <app> [service] [--since 30m]
ops host <app> <status|ps|df|journal|errors> [target] [--since 30m]
ops host <app> exec <file|->         (bash script as root on the host)
ops run <run-id|url>                 ops mcp
```

`ops plan` stores the plan and prints its id (`OPS: plan_run=<id>`), the change
counts and every address it destroys (`OPS: destroy=`) or replaces (`OPS: replace=`).
`ops apply <stack> --plan <id>` applies exactly that plan; the executor refuses it if
it is older than 1 h, if `platform` or `platform-control` main moved since, or (in
Terraform) if the state changed. `ops apply` without `--plan` only plans, prints the
command to apply that plan and exits non-zero. Standard mode keeps the stack's
guardrails (no deletes, replaces or IAM, sizes within the envelope). `--admin` runs
in platform-control's `ops-admin` environment, which allows all of that; plan and
apply must use the same mode, and `--confirm-destroy` must equal the plan's destroy
count or the apply is refused.
Admin runs and `host exec` post an entry to the "Agent activity" issue in
platform-control, so the owner sees every one. `ops gh` uses the source app, which is
installed on all repositories (platform-control included) with code, workflow,
secret, variable, environment and settings access.

MCP tools: `ops_status`, `ops_plan` (`admin`), `ops_apply` (`plan`, `admin`, `confirm_destroy`),
`ops_deploy`, `ops_rollback`, `ops_logs`, `ops_host` (`command: exec` + `script`),
`ops_gh` (`args`), `ops_run`.

`ops setup-agents` registers `ops mcp` (by the binary's absolute path) with the
coding agents it finds: Claude Code (`claude mcp add --scope user`), Codex
(`[mcp_servers.ops]` in `~/.codex/config.toml`) and OpenCode (`mcp.ops`, or
`mcp.servers.ops` in the OpenCode 2 layout, in `~/.config/opencode/opencode.json`). It only adds or replaces the `ops` entry, is safe
to re-run, never rewrites a config with comments (it prints the snippet instead), and
`--dry-run` shows what it would change. `ops doctor` reports which agents have it.

Executor commands print `Run: <url>` immediately, wait (`--timeout 45m`, or
`--no-wait`), then print the run's `OPS:` result lines. They exit 0 only when the
run concluded `success`.

## Security model

Agents have full standing authority; the design keeps machines disposable and every
privileged action logged, not limited.

- No provider (OCI, Cloudflare, ...) credentials on disk: privileged work runs only in
  `n0ci/platform-control` workflows, which hold the cloud trust via OIDC (OCI) or
  environment secrets (Cloudflare).
- Two GitHub Apps: `source` (all repositories) for git/gh, `dispatch`
  (`platform-control`, actions but no contents) for workflows. Deleting an app key in
  GitHub revokes a machine's access.
- Installation tokens are minted on demand, live about an hour, and are kept in process
  memory only; git gets them through `ops git-credential` and never stores them.
- The bootstrap bundle (app keys) is stored in the macOS login keychain (or a 0600 file
  under `$XDG_CONFIG_HOME/ops` on Linux) and is never printed.
- Every input is validated client-side and again by the workflows; destructive admin
  applies need an exact destroy count, and the workflows refuse changes to the
  executor's own OCI trust and admin policy so a mistake cannot lock agents out.

## Development

```
go vet ./... && go test ./... && gofmt -l .
OPS_GITHUB_API=http://127.0.0.1:8080 OPS_BUNDLE=ops1:... go run ./cmd/ops doctor
```

Releases: push a `vX.Y.Z` tag; `.github/workflows/release.yml` builds, checksums,
attests and publishes the binaries. Bump `OPS_VERSION` in `bootstrap.sh` afterwards.
