package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/n0ci/ops-client/internal/bundle"
)

// AgentEnv is what setup-agents needs from the machine; tests replace it.
type AgentEnv struct {
	Home     string                                                                      // user home ($HOME)
	Workdir  string                                                                      // ops workdir (expanded); Claude Code's project scope
	Exe      string                                                                      // absolute path of the ops binary to register
	Getenv   func(string) string                                                         // CODEX_HOME, XDG_CONFIG_HOME
	LookPath func(string) (string, error)                                                // finds claude/codex/opencode
	Run      func(ctx context.Context, dir, name string, args ...string) (string, error) // combined output; dir "" = inherit
}

// DefaultAgentEnv describes the real machine. Home honors $HOME; the workdir
// is resolved like `ops clone` does (OPS_WORKDIR, the bundle, ~/repo/ops).
func DefaultAgentEnv() (*AgentEnv, error) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return nil, err
		}
	}
	exe, err := selfPath()
	if err != nil {
		return nil, fmt.Errorf("locating the ops binary: %w", err)
	}
	b, _ := LoadBundle() // optional: only its workdir setting is used
	return &AgentEnv{
		Home: home, Workdir: bundle.ExpandHome(bundle.Workdir(b)), Exe: exe, Getenv: os.Getenv, LookPath: exec.LookPath,
		Run: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			return string(out), err
		},
	}, nil
}

// selfPath is the absolute, symlink-resolved path of the running binary.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

type agentState int

const (
	agentOK           agentState = iota // registered (now, already, or would be in a dry run)
	agentNotInstalled                   // harness not present
	agentFailed                         // could not register
)

type agentResult struct {
	state   agentState
	changed bool   // a change was (or in a dry run would be) made
	detail  string // one line after "<harness>: "
	extra   string // indented block (snippet, would-be content)
}

type harness struct {
	name string
	fn   func(e *AgentEnv, ctx context.Context, dry bool) agentResult
}

var harnesses = []harness{
	{"claude", (*AgentEnv).claude},
	{"codex", (*AgentEnv).codex},
	{"opencode", (*AgentEnv).opencode},
}

// SetupAgents registers `ops mcp` with every coding harness found on the
// machine. It returns an error when any installed harness failed.
func SetupAgents(ctx context.Context, w io.Writer, e *AgentEnv, dry bool) error {
	if dry {
		fmt.Fprintln(w, "dry run: nothing is changed")
	}
	failed := 0
	for _, h := range harnesses {
		r := h.fn(e, ctx, dry)
		mark := "✓"
		switch r.state {
		case agentNotInstalled:
			mark = "–"
		case agentFailed:
			mark = "✗"
			failed++
		}
		fmt.Fprintf(w, "%s %s: %s\n", mark, h.name, r.detail)
		if r.extra != "" {
			for _, l := range strings.Split(strings.TrimRight(r.extra, "\n"), "\n") {
				fmt.Fprintln(w, "    "+l)
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("setup-agents: %d harness(es) failed", failed)
	}
	return nil
}

// agentStatus is the doctor summary, e.g. "claude ✓ codex – opencode ✗":
// ✓ registered with this binary, – not installed, ✗ missing or stale.
func agentStatus(ctx context.Context, e *AgentEnv) (string, bool) {
	var parts []string
	ok := true
	for _, h := range harnesses {
		r := h.fn(e, ctx, true)
		mark := "✓"
		switch {
		case r.state == agentNotInstalled:
			mark = "–"
		case r.state == agentFailed || r.changed:
			mark, ok = "✗", false
		}
		parts = append(parts, h.name+" "+mark)
	}
	return strings.Join(parts, " "), ok
}

// --- Claude Code ---------------------------------------------------------

// claude registers ops in Claude Code's project scope: .mcp.json in the
// workdir, which Claude Code also reads in every repository below it, so
// sessions elsewhere do not get the ops tools. A user-scope `ops` entry (what
// ops before 0.3.2 registered) is removed; other servers are left alone.
func (e *AgentEnv) claude(ctx context.Context, dry bool) agentResult {
	bin, err := e.LookPath("claude")
	if err != nil {
		return agentResult{state: agentNotInstalled, detail: "not installed (no `claude` on PATH)"}
	}
	if e.Workdir == "" {
		return agentResult{state: agentFailed, detail: "no workdir to register the project-scope server in"}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	mcpJSON := filepath.Join(e.Workdir, ".mcp.json")
	type step struct{ dir, what string }
	var steps []step
	var undo []string

	// `claude mcp get` from $HOME sees the user scope (and Home's own scopes,
	// which then shadow it; those are left alone).
	if out, err := e.Run(ctx, e.Home, bin, "mcp", "get", "ops"); err == nil {
		if scope, _, _ := parseClaudeGet(out); strings.HasPrefix(strings.ToLower(scope), "user") {
			steps = append(steps, step{e.Home, "mcp remove --scope user ops"})
			undo = append(undo, "removed the user-scope entry")
		}
	}
	have, err := claudeProjectEntry(mcpJSON)
	if err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	current := have != nil && have.Command == e.Exe && len(have.Args) == 1 && have.Args[0] == "mcp"
	if have != nil && !current {
		steps = append(steps, step{e.Workdir, "mcp remove --scope project ops"})
	}
	if !current {
		steps = append(steps, step{e.Workdir, ""}) // add, built below (Exe may contain spaces)
	}
	where := "project scope, " + mcpJSON
	if len(steps) == 0 {
		return agentResult{detail: "already registered (" + where + "): " + e.Exe + " mcp"}
	}
	argv := func(s step) []string {
		if s.what == "" {
			return []string{"mcp", "add", "--scope", "project", "ops", "--", e.Exe, "mcp"}
		}
		return strings.Fields(s.what)
	}
	if dry {
		var cmds []string
		for _, s := range steps {
			cmds = append(cmds, "(cd "+s.dir+" && claude "+strings.Join(argv(s), " ")+")")
		}
		return agentResult{changed: true, detail: "would run: " + strings.Join(cmds, " && ")}
	}
	if err := os.MkdirAll(e.Workdir, 0o755); err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	for _, s := range steps {
		if out, err := e.Run(ctx, s.dir, bin, argv(s)...); err != nil {
			return agentResult{state: agentFailed, detail: fmt.Sprintf("claude %s (in %s): %v: %s", strings.Join(argv(s), " "), s.dir, err, strings.TrimSpace(out))}
		}
	}
	detail := "registered (" + where + "): " + e.Exe + " mcp"
	if current {
		detail = "already registered (" + where + ")"
	}
	if len(undo) > 0 {
		detail += "; " + strings.Join(undo, ", ")
	}
	if !current {
		detail += "; Claude Code asks once per project to approve it"
	}
	return agentResult{changed: true, detail: detail}
}

type claudeServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// claudeProjectEntry reads mcpServers.ops from a project .mcp.json (nil if
// the file or the entry is missing).
func claudeProjectEntry(path string) (*claudeServer, error) {
	src, err := readOptional(path)
	if err != nil || len(bytes.TrimSpace(src)) == 0 {
		return nil, err
	}
	var f struct {
		MCPServers map[string]*claudeServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(src, &f); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return f.MCPServers["ops"], nil
}

// parseClaudeGet extracts Scope, Command and Args from `claude mcp get` output.
func parseClaudeGet(out string) (scope, command, args string) {
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Scope":
			scope = v
		case "Command":
			command = v
		case "Args":
			args = v
		}
	}
	return
}

// --- Codex -----------------------------------------------------------------

func (e *AgentEnv) codexHome() string {
	if d := e.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(e.Home, ".codex")
}

func (e *AgentEnv) codex(_ context.Context, dry bool) agentResult {
	dir := e.codexHome()
	if _, err := e.LookPath("codex"); err != nil && !isDir(dir) {
		return agentResult{state: agentNotInstalled, detail: "not installed (no `codex` on PATH, no " + dir + ")"}
	}
	path := filepath.Join(dir, "config.toml")
	src, err := readOptional(path)
	if err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	out, changed, err := codexTOML(src, e.Exe)
	if err != nil {
		return agentResult{state: agentFailed, detail: fmt.Sprintf("%s: %v; add this table by hand:", path, err), extra: codexTable(e.Exe, "\n")}
	}
	return applyFile(path, out, changed, dry, 0o700, codexTable(e.Exe, "\n"))
}

var (
	tomlOpsHeader     = regexp.MustCompile(`^\s*\[\s*mcp_servers\s*\.\s*(?:ops|"ops"|'ops')\s*\]\s*(?:#.*)?$`)
	tomlOpsArray      = regexp.MustCompile(`^\s*\[\[\s*mcp_servers\s*\.\s*(?:ops|"ops"|'ops')\s*\]\]`)
	tomlServersHeader = regexp.MustCompile(`^\s*\[\s*(?:mcp_servers|"mcp_servers")\s*\]\s*(?:#.*)?$`)
	tomlRootServers   = regexp.MustCompile(`^\s*(?:mcp_servers|"mcp_servers")\s*[.=]`)
	tomlOpsKey        = regexp.MustCompile(`^\s*(?:ops|"ops"|'ops')\s*[.=]`)
)

// codexTable is the [mcp_servers.ops] table for exe.
func codexTable(exe, nl string) string {
	return "[mcp_servers.ops]" + nl + "command = " + tomlString(exe) + nl + `args = ["mcp"]` + nl
}

// codexTOML adds or replaces the [mcp_servers.ops] table in a Codex
// config.toml, leaving every other byte as it was. Forms it cannot edit
// safely (inline or dotted-key definitions of mcp_servers.ops) are errors.
func codexTOML(src []byte, exe string) ([]byte, bool, error) {
	text := string(src)
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	want := codexTable(exe, nl)
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	top := tomlTopLevel(lines)
	isHeader := func(i int) bool { return top[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") }

	start, table := -1, "" // table: "", "servers", "ops" or "other"
	for i, l := range lines {
		l = strings.TrimRight(l, "\r\n")
		switch {
		case !top[i]:
		case isHeader(i):
			switch {
			case tomlOpsArray.MatchString(l):
				return nil, false, errors.New("mcp_servers.ops is an array of tables")
			case tomlOpsHeader.MatchString(l):
				if start >= 0 {
					return nil, false, errors.New("[mcp_servers.ops] is defined twice")
				}
				start, table = i, "ops"
			case tomlServersHeader.MatchString(l):
				table = "servers"
			default:
				table = "other"
			}
		case table == "" && tomlRootServers.MatchString(l), table == "servers" && tomlOpsKey.MatchString(l):
			return nil, false, errors.New("mcp_servers.ops is defined with dotted keys or an inline table")
		}
	}

	if start < 0 {
		out := text
		if out != "" {
			if !strings.HasSuffix(out, "\n") {
				out += nl
			}
			if strings.TrimSpace(lines[len(lines)-1]) != "" {
				out += nl
			}
		}
		return []byte(out + want), true, nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isHeader(i) {
			end = i
			break
		}
	}
	// Trailing blank lines and comments stay with whatever follows.
	for end > start+1 {
		t := strings.TrimSpace(lines[end-1])
		if !top[end-1] || (t != "" && !strings.HasPrefix(t, "#")) {
			break
		}
		end--
	}
	old := strings.Join(lines[start:end], "")
	if normLines(old) == normLines(want) {
		return src, false, nil
	}
	if !strings.HasSuffix(old, "\n") { // table was the last line without a newline
		want = strings.TrimSuffix(want, nl)
	}
	out := strings.Join(lines[:start], "") + want + strings.Join(lines[end:], "")
	return []byte(out), true, nil
}

func normLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// tomlTopLevel reports for each line whether it starts outside multi-line
// strings and arrays, i.e. whether it can be a key or a table header.
func tomlTopLevel(lines []string) []bool {
	top := make([]bool, len(lines))
	depth, ml := 0, ""
	for i, l := range lines {
		top[i] = depth == 0 && ml == ""
		for j := 0; j < len(l); j++ {
			if ml != "" {
				if ml == `"""` && l[j] == '\\' {
					j++
				} else if strings.HasPrefix(l[j:], ml) {
					j += 2
					ml = ""
				}
				continue
			}
			switch c := l[j]; {
			case c == '#':
				j = len(l)
			case strings.HasPrefix(l[j:], `"""`), strings.HasPrefix(l[j:], `'''`):
				ml = l[j : j+3]
				j += 2
			case c == '"':
				for j++; j < len(l) && l[j] != '"'; j++ {
					if l[j] == '\\' {
						j++
					}
				}
			case c == '\'':
				for j++; j < len(l) && l[j] != '\''; j++ {
				}
			case c == '[' || c == '{':
				depth++
			case (c == ']' || c == '}') && depth > 0:
				depth--
			}
		}
	}
	return top
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// --- OpenCode --------------------------------------------------------------

func (e *AgentEnv) opencodeDir() string {
	if d := e.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	return filepath.Join(e.Home, ".config", "opencode")
}

type opencodeServer struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	Enabled bool     `json:"enabled"`
}

// opencodeSnippet is the entry to paste by hand, and where it goes.
func opencodeSnippet(src []byte, exe string) (where, snippet string) {
	v2 := false
	if top, err := parseObject(stripJSONC(src)); err == nil {
		if mi := find(top, "mcp"); mi >= 0 {
			if mcp, err := parseObject(top[mi].v); err == nil {
				v2 = find(mcp, "servers") >= 0
			}
		}
	}
	where = `"mcp"`
	if v2 {
		where = `"mcp" > "servers"`
	}
	var buf bytes.Buffer
	_ = json.Indent(&buf, opencodeWant(exe, v2), "", "  ")
	return where, `"ops": ` + buf.String() + "\n"
}

func (e *AgentEnv) opencode(_ context.Context, dry bool) agentResult {
	dir := e.opencodeDir()
	if _, err := e.LookPath("opencode"); err != nil && !isDir(dir) {
		return agentResult{state: agentNotInstalled, detail: "not installed (no `opencode` on PATH, no " + dir + ")"}
	}
	path := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(path); err != nil {
		if jc := filepath.Join(dir, "opencode.jsonc"); fileExists(jc) {
			path = jc
		}
	}
	src, err := readOptional(path)
	if err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	out, changed, err := opencodeJSON(src, e.Exe)
	where, snippet := opencodeSnippet(src, e.Exe)
	if err != nil {
		return agentResult{state: agentFailed, detail: fmt.Sprintf("%s: %v; add this under %s by hand:", path, err, where), extra: snippet}
	}
	return applyFile(path, out, changed, dry, 0o755, snippet)
}

// errHasComments marks JSONC that ops will not rewrite.
var errHasComments = errors.New("file has comments or trailing commas, not rewriting it")

// opencodeWant is the ops entry. OpenCode 1 keeps servers directly under
// "mcp" with "enabled"; OpenCode 2 nests them under "mcp.servers" (and still
// reads the OpenCode 1 layout). v2 selects the nested layout.
func opencodeWant(exe string, v2 bool) json.RawMessage {
	var v any = opencodeServer{"local", []string{exe, "mcp"}, true}
	if v2 {
		v = struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
		}{"local", []string{exe, "mcp"}}
	}
	b, _ := marshalNoEscape(v)
	return b
}

// opencodeJSON sets the ops MCP server in an OpenCode config, keeping other
// keys and their order: mcp.servers.ops when the file already uses the
// OpenCode 2 "servers" layout, else mcp.ops. Files with comments are only
// checked, never rewritten.
func opencodeJSON(src []byte, exe string) ([]byte, bool, error) {
	plain := stripJSONC(src)
	commented := !bytes.Equal(plain, src)
	if len(bytes.TrimSpace(plain)) == 0 {
		if commented {
			return nil, false, errHasComments
		}
		plain = []byte(`{"$schema": "https://opencode.ai/config.json"}`)
	}
	top, err := parseObject(plain)
	if err != nil {
		return nil, false, err
	}
	var mcp, servers []kv
	if mi := find(top, "mcp"); mi >= 0 {
		if mcp, err = parseObject(top[mi].v); err != nil {
			return nil, false, fmt.Errorf("\"mcp\": %w", err)
		}
	}
	si := find(mcp, "servers")
	if si >= 0 {
		if servers, err = parseObject(mcp[si].v); err != nil {
			return nil, false, fmt.Errorf("\"mcp.servers\": %w", err)
		}
	}
	v2 := si >= 0
	cur, oi := mcp, find(mcp, "ops")
	if v2 {
		cur, oi = servers, find(servers, "ops")
	}
	if oi >= 0 && opencodeSame(cur[oi].v, opencodeWant(exe, v2)) && (!v2 || find(mcp, "ops") < 0) {
		return src, false, nil
	}
	if commented {
		return nil, false, errHasComments
	}
	if v2 {
		servers = set(servers, "ops", opencodeWant(exe, true))
		mcp = set(mcp, "servers", marshalObject(servers))
		if i := find(mcp, "ops"); i >= 0 { // superseded OpenCode 1 entry
			mcp = append(mcp[:i], mcp[i+1:]...)
		}
	} else {
		mcp = set(mcp, "ops", opencodeWant(exe, false))
	}
	top = set(top, "mcp", marshalObject(mcp))
	var buf bytes.Buffer
	if err := json.Indent(&buf, marshalObject(top), "", "  "); err != nil {
		return nil, false, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), true, nil
}

// opencodeSame compares entries, ignoring the defaults enabled=true and
// disabled=false.
func opencodeSame(have, want json.RawMessage) bool {
	var h, w map[string]any
	if json.Unmarshal(have, &h) != nil || json.Unmarshal(want, &w) != nil {
		return false
	}
	for _, m := range []map[string]any{h, w} {
		if m["enabled"] == true {
			delete(m, "enabled")
		}
		if m["disabled"] == false {
			delete(m, "disabled")
		}
	}
	return reflect.DeepEqual(h, w)
}

type kv struct {
	k string
	v json.RawMessage
}

func parseObject(b []byte) ([]kv, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []kv
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, kv{t.(string), raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON object")
	}
	return out, nil
}

func marshalObject(kvs []kv) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range kvs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := marshalNoEscape(p.k)
		b.Write(k)
		b.WriteByte(':')
		b.Write(p.v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func find(kvs []kv, k string) int {
	for i, p := range kvs {
		if p.k == k {
			return i
		}
	}
	return -1
}

func set(kvs []kv, k string, v json.RawMessage) []kv {
	if i := find(kvs, k); i >= 0 {
		kvs[i].v = v
		return kvs
	}
	return append(kvs, kv{k, v})
}

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n"), err
}

// stripJSONC removes // and /* */ comments and trailing commas outside
// strings. It returns src itself when there is nothing to strip.
func stripJSONC(src []byte) []byte {
	var out []byte
	changed := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"':
			j := i + 1
			for ; j < len(src) && src[j] != '"'; j++ {
				if src[j] == '\\' {
					j++
				}
			}
			out = append(out, src[i:min(j+1, len(src))]...)
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			changed = true
			for i < len(src) && src[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			changed = true
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				i = len(src)
			} else {
				i += end + 3
			}
		case c == ',':
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				changed = true
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	if !changed {
		return src
	}
	return out
}

// --- shared ----------------------------------------------------------------

func applyFile(path string, out []byte, changed, dry bool, dirMode os.FileMode, snippet string) agentResult {
	exists := fileExists(path)
	if !changed {
		return agentResult{detail: "already registered in " + path}
	}
	verb := "updated"
	if !exists {
		verb = "created"
	}
	if dry {
		if !exists {
			return agentResult{changed: true, detail: "would create " + path + " with:", extra: string(out)}
		}
		return agentResult{changed: true, detail: "would set the ops entry in " + path + " to:", extra: snippet}
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	if err := writeAtomic(path, out); err != nil {
		return agentResult{state: agentFailed, detail: err.Error()}
	}
	return agentResult{changed: true, detail: verb + " " + path}
}

func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
