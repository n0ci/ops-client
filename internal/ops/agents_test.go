package ops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testExe = "/opt/ops bin/ops"

func TestCodexTOMLFresh(t *testing.T) {
	out, changed, err := codexTOML(nil, testExe)
	want := "[mcp_servers.ops]\ncommand = \"/opt/ops bin/ops\"\nargs = [\"mcp\"]\n"
	if err != nil || !changed || string(out) != want {
		t.Fatalf("got %q, %v, %v", out, changed, err)
	}
	out2, changed, err := codexTOML(out, testExe)
	if err != nil || changed || !bytes.Equal(out, out2) {
		t.Fatalf("second run: %q, %v, %v", out2, changed, err)
	}
}

func TestCodexTOMLPreservesUnrelated(t *testing.T) {
	src := `# my codex config
model = "o3"   # trailing comment
notes = """
[mcp_servers.ops]
command = "not a table, inside a string"
"""

[mcp_servers.other]
command = "npx"
args = [
  "-y",
  "other",
]

[profiles.fast]
model = "mini"` // no trailing newline
	out, changed, err := codexTOML([]byte(src), testExe)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := src + "\n\n[mcp_servers.ops]\ncommand = \"/opt/ops bin/ops\"\nargs = [\"mcp\"]\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	if _, changed, _ := codexTOML(out, testExe); changed {
		t.Fatal("second run changed the file")
	}
}

func TestCodexTOMLReplacesExisting(t *testing.T) {
	head := "model = \"o3\"\n\n"
	tail := "\n# next: profiles\n[profiles.fast]\nmodel = \"mini\"\n\n[mcp_servers.ops.env]\nFOO = \"bar\"\n"
	src := head + "[mcp_servers.ops] # old\ncommand = \"/usr/local/bin/ops\"\nargs = [\n  \"mcp\",\n]\nstartup_timeout_sec = 5\n" + tail
	out, changed, err := codexTOML([]byte(src), testExe)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := head + "[mcp_servers.ops]\ncommand = \"/opt/ops bin/ops\"\nargs = [\"mcp\"]\n" + tail
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	out2, changed, err := codexTOML(out, testExe)
	if err != nil || changed || !bytes.Equal(out, out2) {
		t.Fatalf("second run: changed=%v err=%v", changed, err)
	}
	// Last table in a file without a trailing newline.
	src = "a = 1\n\n[mcp_servers.ops]\ncommand = \"x\""
	out, _, _ = codexTOML([]byte(src), testExe)
	if want := "a = 1\n\n[mcp_servers.ops]\ncommand = \"/opt/ops bin/ops\"\nargs = [\"mcp\"]"; string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestCodexTOMLCRLF(t *testing.T) {
	src := "model = \"o3\"\r\n"
	out, _, err := codexTOML([]byte(src), "/bin/ops")
	if err != nil || string(out) != src+"\r\n[mcp_servers.ops]\r\ncommand = \"/bin/ops\"\r\nargs = [\"mcp\"]\r\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, changed, _ := codexTOML(out, "/bin/ops"); changed {
		t.Fatal("second run changed the file")
	}
}

func TestCodexTOMLUnsupportedForms(t *testing.T) {
	for _, src := range []string{
		"[mcp_servers]\nops = { command = \"ops\" }\n",
		"[mcp_servers]\nops.command = \"ops\"\n",
		"mcp_servers.ops.command = \"ops\"\n",
		"mcp_servers = { ops = { command = \"ops\" } }\n",
		"[mcp_servers.ops]\ncommand = \"a\"\n[mcp_servers.ops]\ncommand = \"b\"\n",
	} {
		if _, _, err := codexTOML([]byte(src), testExe); err == nil {
			t.Errorf("no error for %q", src)
		}
	}
	// Other servers' keys under [mcp_servers] are fine.
	if _, _, err := codexTOML([]byte("[mcp_servers]\nopsx = { command = \"x\" }\n"), testExe); err != nil {
		t.Fatal(err)
	}
}

func TestTOMLString(t *testing.T) {
	if got := tomlString(`C:\a "b"` + "\n"); got != `"C:\\a \"b\"\u000A"` {
		t.Fatalf("got %s", got)
	}
}

func TestOpencodeJSONFresh(t *testing.T) {
	out, changed, err := opencodeJSON(nil, testExe)
	want := `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "ops": {
      "type": "local",
      "command": [
        "/opt/ops bin/ops",
        "mcp"
      ],
      "enabled": true
    }
  }
}
`
	if err != nil || !changed || string(out) != want {
		t.Fatalf("got %s, %v, %v", out, changed, err)
	}
	out2, changed, err := opencodeJSON(out, testExe)
	if err != nil || changed || !bytes.Equal(out, out2) {
		t.Fatalf("second run: changed=%v err=%v", changed, err)
	}
}

func TestOpencodeJSONPreservesAndReplaces(t *testing.T) {
	src := `{"theme": "tokyonight", "big": 12345678901234567890, "mcp": {"other": {"type": "remote", "url": "https://x/<y>"}, "ops": {"type": "local", "command": ["/old/ops", "mcp"]}}, "zeta": [1, 2]}`
	out, changed, err := opencodeJSON([]byte(src), testExe)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := `{
  "theme": "tokyonight",
  "big": 12345678901234567890,
  "mcp": {
    "other": {
      "type": "remote",
      "url": "https://x/<y>"
    },
    "ops": {
      "type": "local",
      "command": [
        "/opt/ops bin/ops",
        "mcp"
      ],
      "enabled": true
    }
  },
  "zeta": [
    1,
    2
  ]
}
`
	if string(out) != want {
		t.Fatalf("got\n%s", out)
	}
	if _, changed, _ := opencodeJSON(out, testExe); changed {
		t.Fatal("second run changed the file")
	}
	if _, _, err := opencodeJSON([]byte(`{"mcp": []}`), testExe); err == nil {
		t.Fatal("mcp array accepted")
	}
	if _, _, err := opencodeJSON([]byte(`[1]`), testExe); err == nil {
		t.Fatal("top-level array accepted")
	}
}

func TestOpencodeJSONServersLayout(t *testing.T) {
	// OpenCode 2 nests servers under mcp.servers; an old top-level mcp.ops is dropped.
	src := `{"mcp": {"servers": {"gh": {"type": "remote", "url": "https://gh"}}, "ops": {"type": "local", "command": ["/old/ops", "mcp"], "enabled": true}}}`
	out, changed, err := opencodeJSON([]byte(src), testExe)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := `{
  "mcp": {
    "servers": {
      "gh": {
        "type": "remote",
        "url": "https://gh"
      },
      "ops": {
        "type": "local",
        "command": [
          "/opt/ops bin/ops",
          "mcp"
        ]
      }
    }
  }
}
`
	if string(out) != want {
		t.Fatalf("got\n%s", out)
	}
	if _, changed, _ := opencodeJSON(out, testExe); changed {
		t.Fatal("second run changed the file")
	}
	same := `{"mcp": {"servers": {"ops": {"type": "local", "command": ["/opt/ops bin/ops", "mcp"], "disabled": false}}}}`
	if _, changed, err := opencodeJSON([]byte(same), testExe); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	where, snippet := opencodeSnippet([]byte("// c\n"+same), testExe)
	if where != `"mcp" > "servers"` || strings.Contains(snippet, "enabled") || !strings.HasPrefix(snippet, `"ops": {`) {
		t.Fatalf("%s: %s", where, snippet)
	}
}

func TestOpencodeJSONCLeftAlone(t *testing.T) {
	src := "{\n  // my settings\n  \"theme\": \"x\", /* block */\n  \"url\": \"http://a//b\",\n}\n"
	out, changed, err := opencodeJSON([]byte(src), testExe)
	if !errors.Is(err, errHasComments) || changed || out != nil {
		t.Fatalf("got %q, %v, %v", out, changed, err)
	}
	// Already registered: reported as such, still untouched.
	reg := "{\n  // c\n  \"mcp\": {\"ops\": {\"enabled\": true, \"type\": \"local\", \"command\": [\"/opt/ops bin/ops\", \"mcp\"]},},\n}\n"
	out, changed, err = opencodeJSON([]byte(reg), testExe)
	if err != nil || changed || string(out) != reg {
		t.Fatalf("got %q, %v, %v", out, changed, err)
	}
	// JSONC without comments is plain JSON and gets edited.
	if _, changed, err := opencodeJSON([]byte(`{"url": "http://a//b"}`), testExe); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

type fakeAgentMachine struct {
	onPath map[string]bool
	calls  []string
	getOut string // `claude mcp get ops` output; empty = not registered
}

func (f *fakeAgentMachine) env(home string) *AgentEnv {
	return &AgentEnv{
		Home: home, Exe: testExe,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if f.onPath[name] {
				return "/fake/" + name, nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			c := strings.Join(append([]string{filepath.Base(name)}, args...), " ")
			f.calls = append(f.calls, c)
			if c == "claude mcp get ops" && f.getOut == "" {
				return `No MCP server named "ops".`, errors.New("exit status 1")
			}
			if c == "claude mcp get ops" {
				return f.getOut, nil
			}
			return "ok", nil
		},
	}
}

func TestSetupAgents(t *testing.T) {
	home := t.TempDir()
	f := &fakeAgentMachine{onPath: map[string]bool{"claude": true, "codex": true}}
	e := f.env(home)
	ctx := context.Background()
	codexCfg := filepath.Join(home, ".codex", "config.toml")

	var out bytes.Buffer
	if err := SetupAgents(ctx, &out, e, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(codexCfg) || len(f.calls) != 1 {
		t.Fatalf("dry run changed something: calls=%q", f.calls)
	}
	for _, s := range []string{
		"✓ claude: would run: claude mcp add --scope user ops -- /opt/ops bin/ops mcp",
		"✓ codex: would create " + codexCfg,
		"    [mcp_servers.ops]",
		"– opencode: not installed",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("dry run output lacks %q:\n%s", s, out.String())
		}
	}

	f.calls = nil
	out.Reset()
	if err := SetupAgents(ctx, &out, e, false); err != nil {
		t.Fatal(err)
	}
	if !equal(f.calls, []string{"claude mcp get ops", "claude mcp add --scope user ops -- /opt/ops bin/ops mcp"}) {
		t.Fatalf("calls = %q", f.calls)
	}
	if b, _ := os.ReadFile(codexCfg); !strings.Contains(string(b), "[mcp_servers.ops]") {
		t.Fatalf("codex config = %q", b)
	}

	// Second run: everything already registered, nothing runs or changes.
	f.getOut = "ops:\n  Scope: User config (available in all your projects)\n  Type: stdio\n  Command: /opt/ops bin/ops\n  Args: mcp\n"
	f.calls = nil
	out.Reset()
	if err := SetupAgents(ctx, &out, e, false); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.Contains(out.String(), "✓ claude: already registered") || !strings.Contains(out.String(), "✓ codex: already registered") {
		t.Fatalf("calls=%q out=\n%s", f.calls, out.String())
	}
	if s, ok := agentStatus(ctx, e); !ok || s != "claude ✓ codex ✓ opencode –" {
		t.Fatalf("status = %q, %v", s, ok)
	}

	// Stale user entry is removed and re-added.
	f.getOut = strings.Replace(f.getOut, "/opt/ops bin/ops", "/old/ops", 1)
	f.calls = nil
	if s, ok := agentStatus(ctx, e); ok || s != "claude ✗ codex ✓ opencode –" {
		t.Fatalf("status = %q, %v", s, ok)
	}
	f.calls = nil
	if err := SetupAgents(ctx, &bytes.Buffer{}, e, false); err != nil {
		t.Fatal(err)
	}
	if !equal(f.calls, []string{"claude mcp get ops", "claude mcp remove --scope user ops", "claude mcp add --scope user ops -- /opt/ops bin/ops mcp"}) {
		t.Fatalf("calls = %q", f.calls)
	}
}

func TestSetupAgentsOpencodeJSONC(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	jc := filepath.Join(dir, "opencode.jsonc")
	src := "{\n  // keep me\n  \"theme\": \"x\"\n}\n"
	if err := os.WriteFile(jc, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAgentMachine{onPath: map[string]bool{}}
	var out bytes.Buffer
	err := SetupAgents(context.Background(), &out, f.env(home), false)
	if err == nil {
		t.Fatal("expected a failure for the commented jsonc")
	}
	if b, _ := os.ReadFile(jc); string(b) != src {
		t.Fatalf("jsonc rewritten: %q", b)
	}
	for _, s := range []string{"– claude: not installed", "– codex: not installed", "✗ opencode: " + jc, `    "ops": {`} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out.String())
		}
	}
	if fileExists(filepath.Join(dir, "opencode.json")) {
		t.Fatal("created opencode.json next to opencode.jsonc")
	}
}

func TestParseClaudeGet(t *testing.T) {
	out := "Checking MCP server health…\n\nops:\n  Scope: User config (available in all your projects)\n  Status: ✘ Failed to connect\n  Type: stdio\n  Command: /usr/bin/true\n  Args: mcp\n  Environment:\n\nTo remove this server, run: claude mcp remove ops -s user\n"
	s, c, a := parseClaudeGet(out)
	if !strings.HasPrefix(s, "User config") || c != "/usr/bin/true" || a != "mcp" {
		t.Fatalf("%q %q %q", s, c, a)
	}
}
