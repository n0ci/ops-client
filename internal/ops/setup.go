package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/n0ci/ops-client/internal/bundle"
	"github.com/n0ci/ops-client/internal/github"
	"github.com/n0ci/ops-client/internal/keychain"
)

const (
	helperKey   = "credential.https://github.com.helper"
	httpPathKey = "credential.https://github.com.useHttpPath"
	helperValue = "!ops git-credential"
)

// LoadBundle reads the stored bundle (OPS_BUNDLE first) and decodes it.
func LoadBundle() (*bundle.Bundle, error) {
	s, err := keychain.Load()
	if err != nil {
		return nil, err
	}
	return bundle.Decode(s)
}

// Login reads a bundle (clipboard on macOS when stdin is a terminal,
// otherwise stdin), verifies both apps can mint tokens, stores it and
// configures git to use ops as the github.com credential helper.
func Login(ctx context.Context, stdin *os.File, w io.Writer) error {
	text, fromClipboard, err := readBundleInput(stdin)
	if err != nil {
		return err
	}
	b, err := bundle.Decode(text)
	if err != nil {
		return err
	}
	gh := github.New(b)
	for _, app := range []string{"source", "dispatch"} {
		if _, err := gh.Token(ctx, app); err != nil {
			return fmt.Errorf("bundle decoded but %w", err)
		}
		fmt.Fprintf(w, "✓ %s app %s (installation %d) can mint tokens\n", app, b.Apps[app].Slug, b.Apps[app].InstallationID)
	}
	where, err := keychain.Save(strings.Join(strings.Fields(text), ""))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ bundle stored in %s\n", where)
	if err := ConfigureGit(w); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader("")
		if cmd.Run() == nil && fromClipboard {
			fmt.Fprintln(w, "✓ clipboard cleared")
		}
	}
	return nil
}

func readBundleInput(stdin *os.File) (string, bool, error) {
	fi, err := stdin.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	if !tty {
		// Piped input wins when it holds a bundle; otherwise (empty stdin, or a
		// script's remainder under `curl | sh`) fall back to the clipboard.
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return "", false, err
		}
		if strings.HasPrefix(strings.TrimSpace(string(b)), bundle.Prefix) || runtime.GOOS != "darwin" {
			return string(b), false, nil
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("pbpaste").Output()
		if err != nil {
			return "", false, fmt.Errorf("pbpaste: %w", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(out)), bundle.Prefix) {
			return "", false, errors.New("clipboard does not hold an ops bundle: copy \"ops bootstrap bundle\" from Passwords (ops.nocera.dev) and re-run `ops login`")
		}
		return string(out), true, nil
	}
	fmt.Fprintln(os.Stderr, "Paste the ops1: bundle, then press Enter and Ctrl-D:")
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	return string(b), false, err
}

// ConfigureGit makes `ops git-credential` the only credential helper for
// https://github.com (the empty value resets helpers inherited from system
// config such as osxkeychain, so tokens are never persisted).
// gitHelperValue names this binary by absolute path: agents' shells are often
// not login shells, so ~/.local/bin may be missing from their PATH.
func gitHelperValue() string {
	if exe, err := selfPath(); err == nil {
		if !strings.ContainsAny(exe, " '\"") {
			return "!" + exe + " git-credential"
		}
	}
	return helperValue
}

func ConfigureGit(w io.Writer) error {
	helperValue := gitHelperValue()
	prev, _ := gitConfigAll("--global", helperKey)
	_ = exec.Command("git", "config", "--global", "--unset-all", helperKey).Run() // exit 5 if unset
	for _, args := range [][]string{
		{"--global", "--add", helperKey, ""},
		{"--global", "--add", helperKey, helperValue},
		{"--global", httpPathKey, "true"},
	} {
		if out, err := exec.Command("git", append([]string{"config"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Fprintf(w, "✓ git: %s = %q, useHttpPath = true (global config)\n", helperKey, helperValue)
	for _, p := range prev {
		if p != "" && p != helperValue {
			fmt.Fprintf(w, "  replaced previous github.com helper: %s\n", p)
		}
	}
	return nil
}

func gitConfigAll(scope, key string) ([]string, error) {
	out, err := exec.Command("git", "config", scope, "--get-all", key).Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}

// GitCredential implements the git credential-helper protocol. For "get"
// with protocol=https and host=github.com it answers with a source-app
// installation token; everything else is ignored.
func GitCredential(ctx context.Context, op string, in io.Reader, out io.Writer, token func(context.Context) (github.Token, error)) error {
	attrs := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if l == "" {
			break
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			attrs[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if op != "get" || attrs["protocol"] != "https" || attrs["host"] != "github.com" {
		return nil
	}
	t, err := token(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "username=x-access-token\npassword=%s\n", t.Value)
	if !t.Expires.IsZero() {
		fmt.Fprintf(out, "password_expiry_utc=%d\n", t.Expires.Unix())
	}
	return nil
}

// Clone clones every inventory repository missing from dir over https.
func (o *Ops) Clone(ctx context.Context, w io.Writer, dir string) error {
	if dir == "" {
		dir = o.B.WorkdirPath()
	}
	dir = bundle.ExpandHome(dir)
	inv, err := o.Inventory(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Use this binary as helper explicitly so cloning works before ops is on PATH.
	helper := helperValue
	if exe, err := os.Executable(); err == nil {
		helper = "!'" + strings.ReplaceAll(exe, "'", `'\''`) + "' git-credential"
	}
	var failed []string
	for _, name := range inv.Repos {
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(w, "= %s (exists)\n", name)
			continue
		}
		if err := o.GH.Do(ctx, "source", "GET", fmt.Sprintf("/repos/%s/%s", o.B.Owner, name), nil, nil); err != nil {
			if github.IsStatus(err, 404) || github.IsStatus(err, 403) {
				fmt.Fprintf(w, "- %s (skipped: not accessible to the source app)\n", name)
				continue
			}
			return err
		}
		fmt.Fprintf(w, "+ %s\n", name)
		cmd := exec.CommandContext(ctx, "git", "-c", helperKey+"=", "-c", helperKey+"="+helper,
			"clone", "--quiet", fmt.Sprintf("https://github.com/%s/%s.git", o.B.Owner, name), target)
		cmd.Stdout, cmd.Stderr = w, w
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if err := cmd.Run(); err != nil {
			failed = append(failed, name)
		}
	}
	if out, _ := exec.Command("git", "config", "user.email").Output(); strings.TrimSpace(string(out)) == "" {
		fmt.Fprintln(w, "hint: git user.email is not set; commits need it: git config --global user.email you@example.com")
	}
	writeWorkspaceGuide(w, dir)
	if len(failed) > 0 {
		return fmt.Errorf("clone failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

// workspaceGuide is written once to the workdir root so agents started there
// (outside any repository) learn how to operate the systems.
const workspaceGuide = `# Workspace

Repositories of this machine's ops setup live in this directory (see
platform/inventory.yaml). For anything beyond editing code, use the ops CLI:

- Deploy: ops deploy <app> [revision]; roll back: ops rollback <app>
- Infrastructure: edit platform/stacks/<stack>/inputs.tfvars.json, push, then
  ops plan <stack> / ops apply <stack> (add --admin for deletes and IAM)
- Hosts: ops host <app> status|errors, ops logs <app> <service>;
  root shell work: ops host <app> exec <script>
- GitHub (any repo, secrets, settings): ops gh -- <args>
- When something fails: ops doctor

Agents have full standing authority to operate these systems. Before anything
destructive or irreversible: say what will be lost, pass --confirm-destroy N
with the exact count, and check the run's entry in the "Agent activity" issue
of platform-control. Never ask the user for cloud or GitHub tokens.
Details: platform/AGENTS.md.
`

func writeWorkspaceGuide(w io.Writer, dir string) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(workspaceGuide), 0o644); err == nil {
			fmt.Fprintf(w, "+ %s (agent guide)\n", p)
		}
	}
}

type checkResult struct {
	ok       bool
	optional bool
	name     string
	detail   string
	hint     string
}

// Doctor prints a checklist and returns false if a required check failed.
func Doctor(ctx context.Context, w io.Writer) bool {
	var res []checkResult
	add := func(name string, optional bool, hint string, detail string, err error) bool {
		r := checkResult{ok: err == nil, optional: optional, name: name, detail: detail, hint: hint}
		if err != nil {
			r.detail = err.Error()
		}
		res = append(res, r)
		return err == nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	b, err := LoadBundle()
	src := "keychain"
	if os.Getenv(keychain.EnvVar) != "" {
		src = "$" + keychain.EnvVar
	} else if runtime.GOOS != "darwin" {
		src = keychain.FilePath()
	}
	var o *Ops
	if add("bundle present and decodes", false, "run `ops login` with the bundle from iCloud Passwords copied", "from "+src, err) {
		o = New(b)
	}
	skip := errors.New("skipped (no bundle)")
	tokenOK := map[string]bool{}
	for _, app := range []string{"source", "dispatch"} {
		e := skip
		detail := ""
		if o != nil {
			var t github.Token
			if t, e = o.GH.Token(ctx, app); e == nil {
				detail = fmt.Sprintf("%s, expires %s", b.Apps[app].Slug, t.Expires.Local().Format("15:04"))
			}
		}
		tokenOK[app] = add(app+" token mint", false, "app key revoked or app uninstalled? get a fresh bundle and re-run `ops login`", detail, e)
	}
	e := skip
	if tokenOK["source"] {
		e = o.GH.Do(ctx, "source", "GET", fmt.Sprintf("/repos/%s/%s", b.Owner, platformRepo), nil, nil)
	}
	add("source can read "+ownerOr(b)+"/platform", false, "install the source app on the platform repo", "", e)
	e, detail := skip, ""
	if tokenOK["dispatch"] {
		var wl struct {
			Workflows []struct {
				Path string `json:"path"`
			} `json:"workflows"`
		}
		if e = o.GH.Do(ctx, "dispatch", "GET", o.runsPath("workflows?per_page=100"), nil, &wl); e == nil {
			have := map[string]bool{}
			for _, f := range wl.Workflows {
				have[filepath.Base(f.Path)] = true
			}
			var missing []string
			for _, f := range []string{"infra.yml", "deploy.yml", "host.yml"} {
				if !have[f] {
					missing = append(missing, f)
				}
			}
			detail = fmt.Sprintf("%d workflows", len(wl.Workflows))
			if len(missing) > 0 {
				detail += ", not yet present: " + strings.Join(missing, ", ")
			}
		}
	}
	add("dispatch can list platform-control workflows", false, "install the dispatch app on platform-control (actions: write)", detail, e)

	e, detail = nil, ""
	if vals, _ := gitConfigAll("--global", helperKey); len(vals) == 0 || !isOpsHelper(vals[len(vals)-1]) {
		e = errors.New("credential.https://github.com.helper is not `!ops git-credential`")
	} else if _, err := exec.LookPath("ops"); err != nil {
		e = errors.New("helper set, but `ops` is not on PATH for git")
	} else {
		detail = helperValue
	}
	add("git credential helper", false, "run `ops login` (configures git), and put ~/.local/bin on PATH", detail, e)

	for _, t := range []struct {
		bin      string
		optional bool
		hint     string
	}{
		{"git", false, gitHint()},
		{"gh", true, "brew install gh (then use `ops gh ...`)"},
		{"terraform", true, "only needed for local fmt/validate; plans run in platform-control"},
	} {
		p, err := exec.LookPath(t.bin)
		add(t.bin+" on PATH", t.optional, t.hint, p, err)
	}
	wd := "~/repo"
	if b != nil {
		wd = b.Workdir
	}
	_, err = os.Stat(bundle.ExpandHome(wd))
	add("workdir "+wd+" exists", false, "run `ops clone`", "", err)

	// Informational: ✓ registered, – harness not installed, ✗ missing or stale.
	e, detail = nil, ""
	if env, err := DefaultAgentEnv(); err != nil {
		e = err
	} else {
		actx, acancel := context.WithTimeout(ctx, 30*time.Second)
		var all bool
		if detail, all = agentStatus(actx, env); !all {
			e = errors.New(detail)
		}
		acancel()
	}
	add("MCP registered", true, "run `ops setup-agents`", detail, e)

	ok := true
	for _, r := range res {
		mark := "✓"
		if !r.ok {
			mark = "✗"
			if !r.optional {
				ok = false
			}
		}
		line := mark + " " + r.name
		if r.optional && !r.ok {
			line += " (optional)"
		}
		if r.detail != "" {
			line += ": " + r.detail
		}
		fmt.Fprintln(w, line)
		if !r.ok {
			fmt.Fprintln(w, "    fix: "+r.hint)
		}
	}
	return ok
}

func ownerOr(b *bundle.Bundle) string {
	if b == nil {
		return "n0ci"
	}
	return b.Owner
}

func gitHint() string {
	if runtime.GOOS == "darwin" {
		return "xcode-select --install"
	}
	return "install git with your package manager"
}

// isOpsHelper accepts both the bare and the absolute-path helper forms.
func isOpsHelper(v string) bool {
	return v == helperValue || (strings.HasPrefix(v, "!/") && strings.HasSuffix(v, "/ops git-credential"))
}
