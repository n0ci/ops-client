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
`ops clone` and `ops doctor`.

## Commands

```
ops login | doctor | clone [--dir DIR] | status | version
ops token [source|dispatch]          ops gh <args...>
ops git-credential <get|store|erase> (configured by `ops login`)
ops plan <stack>                     ops apply <stack>
ops deploy <app> [revision]          ops rollback <app>
ops logs <app> [service] [--since 30m]
ops host <app> <status|ps|df|journal> [target] [--since 30m]
ops run <run-id|url>                 ops mcp
```

Executor commands print `Run: <url>` immediately, wait (`--timeout 45m`, or
`--no-wait`), then print the run's `OPS:` result lines. They exit 0 only when the
run concluded `success`.

## Security model

- No provider (OCI, Cloudflare, ...) credentials on disk: privileged work runs only in
  `n0ci/platform-control` workflows, which hold the cloud trust via OIDC.
- Two GitHub Apps with split installations: `source` (app repos + `platform`) for
  git/gh, `dispatch` (only `platform-control`, actions but no contents) for workflows.
- Installation tokens are minted on demand, live about an hour, and are kept in process
  memory only; git gets them through `ops git-credential` and never stores them.
- The bootstrap bundle (app keys) is stored in the macOS login keychain (or a 0600 file
  under `$XDG_CONFIG_HOME/ops` on Linux) and is never printed.
- Every input is validated client-side and again by the workflows; ops can only request
  the fixed actions the executor offers.

## Development

```
go vet ./... && go test ./... && gofmt -l .
OPS_GITHUB_API=http://127.0.0.1:8080 OPS_BUNDLE=ops1:... go run ./cmd/ops doctor
```

Releases: push a `vX.Y.Z` tag; `.github/workflows/release.yml` builds, checksums,
attests and publishes the binaries. Bump `OPS_VERSION` in `bootstrap.sh` afterwards.
