#!/bin/sh
# Fresh-machine setup for ops: install the pinned release, log in, clone, check,
# register the ops MCP server with the coding agents.
#   curl -fsSL https://raw.githubusercontent.com/n0ci/ops-client/main/bootstrap.sh | sh
# Copy the "ops bootstrap" note from iCloud Passwords to the clipboard first (macOS).
set -eu

OPS_VERSION="${OPS_VERSION:-0.3.0}" # pinned release; bump deliberately
BASE="https://github.com/n0ci/ops-client/releases/download/v${OPS_VERSION}"
BIN_DIR="${HOME}/.local/bin"

say() { printf '%s\n' "$*"; }
die() { printf 'bootstrap: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
# A shell running under Rosetta reports x86_64 on Apple silicon.
if [ "$os" = darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi

if ! git --version >/dev/null 2>&1; then
  if [ "$os" = darwin ]; then
    die "git is missing: run 'xcode-select --install', then re-run this script"
  fi
  die "git is missing: install it with your package manager, then re-run this script"
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "need curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
else
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
fi

asset="ops_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading ops v${OPS_VERSION} (${os}/${arch})"
fetch "${BASE}/${asset}" "${tmp}/${asset}"
fetch "${BASE}/checksums.txt" "${tmp}/checksums.txt"
want="$(awk -v f="$asset" '$2 == f || $2 == "*" f {print $1}' "${tmp}/checksums.txt")"
[ -n "$want" ] || die "no checksum for ${asset} in checksums.txt"
got="$(sha256 "${tmp}/${asset}")"
[ "$want" = "$got" ] || die "checksum mismatch for ${asset}: want ${want}, got ${got}"
say "Checksum OK (${got})"

mkdir -p "$BIN_DIR"
cp "${tmp}/${asset}" "${BIN_DIR}/ops.new"
chmod 0755 "${BIN_DIR}/ops.new"
mv -f "${BIN_DIR}/ops.new" "${BIN_DIR}/ops"
say "Installed ${BIN_DIR}/ops ($("${BIN_DIR}/ops" version))"

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    # Agents run commands through login shells; put ops on their PATH too.
    for profile in "$HOME/.zprofile" "$HOME/.bash_profile"; do
      if ! grep -qs 'ops-client: PATH' "$profile"; then
        # shellcheck disable=SC2016 # $HOME must stay literal in the profile
        printf '\n# ops-client: PATH\nexport PATH="$HOME/.local/bin:$PATH"\n' >>"$profile"
        say "Added ${BIN_DIR} to PATH in ${profile} (open a new shell to pick it up)"
      fi
    done
    PATH="${BIN_DIR}:${PATH}"
    export PATH
    ;;
esac

# stdin may be this script (curl | sh): give login the terminal, or an empty
# stdin so it reads the bundle from the clipboard.
if (: </dev/tty) 2>/dev/null; then
  ops login </dev/tty
else
  ops login </dev/null
fi
ops clone
ops doctor
# Optional: a harness that cannot be configured must not fail the bootstrap.
ops setup-agents || say "bootstrap: ops setup-agents failed; fix the ✗ lines above and re-run it"
