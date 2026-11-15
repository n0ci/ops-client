package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n0ci/ops-client/internal/github"
	"github.com/n0ci/ops-client/internal/testutil"
)

func TestValidation(t *testing.T) {
	sha := strings.Repeat("a1", 20)
	ok := []error{
		ValidStack("abfahrt"), ValidStack("a-b-9"),
		ValidApp("abfahrt"), ValidApp("imoutfyi"),
		ValidRevision(""), ValidRevision(sha), ValidSHA(sha),
		ValidTarget(""), ValidTarget("caddy.service"), ValidTarget("app@1_x-y"),
		ValidSince("30m"), ValidSince("999d"), ValidSince("5s"),
		ValidHostCommand("journal"), ValidRequestID(NewRequestID()),
	}
	for i, err := range ok {
		if err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := []error{
		ValidStack(""), ValidStack("Abfahrt"), ValidStack("a;rm"), ValidStack("../x"),
		ValidApp("other"), ValidApp("abfahrt "),
		ValidRevision("main"), ValidRevision(strings.ToUpper(sha)), ValidSHA(""),
		ValidTarget("Caddy"), ValidTarget("a b"), ValidTarget(strings.Repeat("a", 65)), ValidTarget("$(id)"),
		ValidSince("30"), ValidSince("1000m"), ValidSince("1w"), ValidSince(""),
		ValidHostCommand("rm"), ValidHostCommand("status;id"),
		ValidRequestID("not-a-uuid"), ValidRequestID("00000000-0000-1000-8000-000000000000"),
	}
	for i, err := range bad {
		if err == nil {
			t.Errorf("bad[%d]: expected error", i)
		}
	}
}

func TestNewRequestIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewRequestID()
		if seen[id] || ValidRequestID(id) != nil {
			t.Fatalf("bad id %q", id)
		}
		seen[id] = true
	}
}

func TestMatchRun(t *testing.T) {
	id := "6f1c1f0e-1d2b-4c3a-9e8f-0a1b2c3d4e5f"
	runs := []github.Run{
		{ID: 1, DisplayTitle: "plan abfahrt [6f1c1f0e-1d2b-4c3a-9e8f-0a1b2c3d4e50]"},
		{ID: 2, DisplayTitle: "plan abfahrt 6f1c1f0e-1d2b-4c3a-9e8f-0a1b2c3d4e5f"},
		{ID: 3, DisplayTitle: "infra apply abfahrt [" + id + "]"},
	}
	if r, ok := MatchRun(runs, id); !ok || r.ID != 3 {
		t.Fatalf("got %v %v", r, ok)
	}
	if _, ok := MatchRun(runs[:2], id); ok {
		t.Fatal("matched without brackets / with different id")
	}
}

func TestExtractOPS(t *testing.T) {
	log := "\uFEFF2026-11-15T10:00:00.0000000Z ##[group]Run echo \"OPS: result=success\"\r\n" +
		"2026-11-15T10:00:00.1000000Z echo \"OPS: result=success\"\n" +
		"2026-11-15T10:00:01.0000000Z OPS: changes=1,0,2\n" +
		"2026-11-15T10:00:02.0000000Z some noise OPS: not=this\n" +
		"2026-11-15T10:00:03.0000000Z OPS-BEGIN\n" +
		"2026-11-15T10:00:03.1000000Z   PID TTY CMD\n" +
		"2026-11-15T10:00:03.2000000Z OPS: inside=block\n" +
		"2026-11-15T10:00:03.3000000Z OPS-END\n" +
		"OPS: result=success\n"
	ops, block := ExtractOPS(log)
	if want := []string{"OPS: changes=1,0,2", "OPS: result=success"}; !equal(ops, want) {
		t.Fatalf("ops = %q", ops)
	}
	if want := []string{"  PID TTY CMD", "OPS: inside=block"}; !equal(block, want) {
		t.Fatalf("block = %q", block)
	}
}

func TestLogTextZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{"2_b.txt", "OPS: b=2\n"}, {"1_a.txt", "OPS: a=1\n"}} {
		w, _ := zw.Create(f[0])
		w.Write([]byte(f[1]))
	}
	zw.Close()
	ops, _ := ExtractOPS(logText(buf.Bytes()))
	if !equal(ops, []string{"OPS: a=1", "OPS: b=2"}) {
		t.Fatalf("ops = %q", ops)
	}
}

func equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func TestGitCredential(t *testing.T) {
	exp := time.Unix(1_900_000_000, 0)
	calls := 0
	tok := func(context.Context) (github.Token, error) {
		calls++
		return github.Token{Value: "ghs_abc", Expires: exp}, nil
	}
	run := func(op, in string) string {
		var out bytes.Buffer
		if err := GitCredential(context.Background(), op, strings.NewReader(in), &out, tok); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	got := run("get", "protocol=https\nhost=github.com\npath=n0ci/platform.git\n\n")
	if want := "username=x-access-token\npassword=ghs_abc\npassword_expiry_utc=1900000000\n"; got != want {
		t.Fatalf("get = %q", got)
	}
	for _, c := range []struct{ op, in string }{
		{"get", "protocol=https\nhost=gitlab.com\n\n"},
		{"get", "protocol=http\nhost=github.com\n\n"},
		{"get", "protocol=https\nhost=gist.github.com\n"},
		{"store", "protocol=https\nhost=github.com\nusername=x\npassword=y\n\n"},
		{"erase", "protocol=https\nhost=github.com\n\n"},
	} {
		if out := run(c.op, c.in); out != "" {
			t.Errorf("%s %q answered %q", c.op, c.in, out)
		}
	}
	if calls != 1 {
		t.Fatalf("token minted %d times, want 1", calls)
	}
}

func TestParseRunID(t *testing.T) {
	for in, want := range map[string]int64{
		"123": 123,
		"https://github.com/n0ci/platform-control/actions/runs/987654321":       987654321,
		"https://github.com/n0ci/platform-control/actions/runs/42/job/7?pr=1#x": 42,
	} {
		if got, err := ParseRunID(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "-1", "https://github.com/n0ci/x"} {
		if _, err := ParseRunID(in); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestParseInventory(t *testing.T) {
	src, err := os.ReadFile("testdata/inventory.yaml")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := ParseInventory(src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"platform", "platform-control", "ops-client", "abfahrt", "imoutfyi"}; !equal(inv.Repos, want) {
		t.Fatalf("repos = %q", inv.Repos)
	}
	if len(inv.Apps) != 2 || inv.Apps[0] != (AppInfo{"abfahrt", "https://abfahrt.app", "https://api.abfahrt.app/v1/health/ready"}) ||
		inv.Apps[1] != (AppInfo{"imoutfyi", "https://imout.fyi", ""}) {
		t.Fatalf("apps = %+v", inv.Apps)
	}
	if _, err := ParseInventory([]byte("repositories:\n  - name: ../evil\n")); err == nil {
		t.Fatal("path-like repo name accepted")
	}
}

// fakeGitHub mimics the endpoints ops uses for one dispatched run.
type fakeGitHub struct {
	t          *testing.T
	srv        *httptest.Server
	conclusion string
	logs       map[int64]string

	mu         sync.Mutex
	inputs     map[string]any
	listCalls  int
	runCalls   int
	blobAuth   []string
	dispatchOK bool
}

const platformSHA = "0123456789abcdef0123456789abcdef01234567"

func newFake(t *testing.T, conclusion string) *fakeGitHub {
	f := &fakeGitHub{t: t, conclusion: conclusion, logs: map[int64]string{
		11: "2026-11-15T10:00:00.0000000Z Terraform will perform...\n2026-11-15T10:00:01.0000000Z OPS: changes=1,2,0\n2026-11-15T10:00:01.0000000Z OPS: plan_sha256=deadbeef\n",
		12: "2026-11-15T10:00:02.0000000Z OPS-BEGIN\n2026-11-15T10:00:02.1000000Z hello from host\n2026-11-15T10:00:02.2000000Z OPS-END\n2026-11-15T10:00:03.0000000Z OPS: result=" + conclusion + "\n",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"message":"jwt"}`, 401)
			return
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":"tok-%s","expires_at":%q}`, r.PathValue("id"), time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	auth := func(r *http.Request, want string) bool {
		if got := r.Header.Get("Authorization"); got != "token "+want {
			t.Errorf("%s %s: auth %q, want token %s", r.Method, r.URL.Path, got, want)
			return false
		}
		return true
	}
	const ctl = "/repos/n0ci/platform-control/actions"
	mux.HandleFunc("GET /repos/n0ci/platform/commits/main", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-456")
		fmt.Fprintf(w, `{"sha":%q}`, platformSHA)
	})
	mux.HandleFunc("POST "+ctl+"/workflows/{file}/dispatches", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-457")
		var body struct {
			Ref    string         `json:"ref"`
			Inputs map[string]any `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Ref != "main" {
			t.Errorf("ref = %q", body.Ref)
		}
		f.mu.Lock()
		f.inputs, f.dispatchOK = body.Inputs, true
		f.inputs["_workflow"] = r.PathValue("file")
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET "+ctl+"/workflows/{file}/runs", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-457")
		if q := r.URL.Query(); q.Get("event") != "workflow_dispatch" || q.Get("per_page") != "20" {
			t.Errorf("runs query = %v", q)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listCalls++
		runs := []map[string]any{{"id": 7, "display_title": "older [00000000-0000-4000-8000-000000000000]"}}
		if f.listCalls >= 3 && f.dispatchOK { // run shows up after a couple of polls
			runs = append([]map[string]any{{"id": 42, "display_title": fmt.Sprintf("infra [%s]", f.inputs["request_id"]),
				"html_url": "https://github.com/n0ci/platform-control/actions/runs/42", "status": "queued"}}, runs...)
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})
	mux.HandleFunc("GET "+ctl+"/runs/42", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-457")
		f.mu.Lock()
		f.runCalls++
		n := f.runCalls
		f.mu.Unlock()
		run := map[string]any{"id": 42, "html_url": "https://github.com/n0ci/platform-control/actions/runs/42", "status": "in_progress"}
		if n >= 3 {
			run["status"], run["conclusion"] = "completed", f.conclusion
		}
		json.NewEncoder(w).Encode(run)
	})
	mux.HandleFunc("GET "+ctl+"/runs/42/jobs", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-457")
		fmt.Fprint(w, `{"total_count":2,"jobs":[{"id":11,"name":"plan"},{"id":12,"name":"report"}]}`)
	})
	mux.HandleFunc("GET "+ctl+"/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		auth(r, "tok-457")
		http.Redirect(w, r, f.srv.URL+"/blob/"+r.PathValue("id")+"?sv=sig", http.StatusFound)
	})
	mux.HandleFunc("GET /blob/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.blobAuth = append(f.blobAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		var id int64
		fmt.Sscan(r.PathValue("id"), &id)
		fmt.Fprint(w, f.logs[id])
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	t.Setenv("OPS_GITHUB_API", f.srv.URL)
	return f
}

func testOps(t *testing.T) *Ops {
	o := New(testutil.Decoded(t))
	o.FindInterval, o.FindTimeout, o.WaitInterval = 5*time.Millisecond, 2*time.Second, 5*time.Millisecond
	return o
}

func TestPlanFlow(t *testing.T) {
	f := newFake(t, "success")
	o := testOps(t)
	var out bytes.Buffer
	if err := o.Infra(context.Background(), &out, "plan", "abfahrt", InfraMode{}, WaitOpts{}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"Run: https://github.com/n0ci/platform-control/actions/runs/42\n",
		"OPS: changes=1,2,0\nOPS: plan_sha256=deadbeef\nOPS: result=success\n",
		"--- output ---\nhello from host\n--- end ---\n",
		"Conclusion: success\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Run: ") > strings.Index(got, "Status: ") {
		t.Errorf("Run URL not printed before waiting:\n%s", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputs["_workflow"] != "infra.yml" || f.inputs["stack"] != "abfahrt" || f.inputs["action"] != "plan" || f.inputs["platform_ref"] != platformSHA {
		t.Errorf("inputs = %v", f.inputs)
	}
	if ValidRequestID(fmt.Sprint(f.inputs["request_id"])) != nil {
		t.Errorf("request_id = %v", f.inputs["request_id"])
	}
	if _, ok := f.inputs["mode"]; ok {
		t.Errorf("standard plan sent mode: %v", f.inputs)
	}
	if len(f.blobAuth) != 2 || f.blobAuth[0] != "" || f.blobAuth[1] != "" {
		t.Errorf("log blob requests carried auth: %q", f.blobAuth)
	}
}

func TestDeployFailureAndNoWait(t *testing.T) {
	newFake(t, "failure")
	o := testOps(t)
	var out bytes.Buffer
	err := o.Deploy(context.Background(), &out, "deploy", "abfahrt", "", WaitOpts{})
	var rf *RunFailedError
	if !errors.As(err, &rf) || rf.Conclusion != "failure" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "Conclusion: failure") || !strings.Contains(out.String(), "OPS: result=failure") {
		t.Fatalf("output:\n%s", out.String())
	}

	f := newFake(t, "success")
	o = testOps(t)
	out.Reset()
	if err := o.Host(context.Background(), &out, "imoutfyi", "ps", "", "", WaitOpts{NoWait: true}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Run: https://github.com/n0ci/platform-control/actions/runs/42\n" {
		t.Fatalf("no-wait output = %q", out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runCalls != 0 || f.inputs["_workflow"] != "host.yml" || f.inputs["since"] != "30m" || f.inputs["command"] != "ps" {
		t.Fatalf("runCalls=%d inputs=%v", f.runCalls, f.inputs)
	}
}

func TestInvalidInputsNeverDispatch(t *testing.T) {
	f := newFake(t, "success")
	o := testOps(t)
	ctx, w := context.Background(), &bytes.Buffer{}
	for i, err := range []error{
		o.Infra(ctx, w, "plan", "Bad Stack", InfraMode{}, WaitOpts{}),
		o.Infra(ctx, w, "destroy", "abfahrt", InfraMode{}, WaitOpts{}),
		o.Infra(ctx, w, "apply", "abfahrt", InfraMode{ConfirmDestroy: 1}, WaitOpts{}),
		o.Infra(ctx, w, "apply", "abfahrt", InfraMode{Admin: true, ConfirmDestroy: -1}, WaitOpts{}),
		o.Infra(ctx, w, "apply", "abfahrt", InfraMode{PlanRun: "12a"}, WaitOpts{}),
		o.Infra(ctx, w, "plan", "abfahrt", InfraMode{PlanRun: "123"}, WaitOpts{}),
		o.Host(ctx, w, "abfahrt", "exec", "", "", WaitOpts{}),
		o.HostExec(ctx, w, "abfahrt", []byte(" \n"), WaitOpts{}),
		o.HostExec(ctx, w, "nope", []byte("id"), WaitOpts{}),
		o.HostExec(ctx, w, "abfahrt", bytes.Repeat([]byte("x"), MaxScriptB64), WaitOpts{}),
		o.Deploy(ctx, w, "deploy", "nope", "", WaitOpts{}),
		o.Deploy(ctx, w, "deploy", "abfahrt", "HEAD", WaitOpts{}),
		o.Host(ctx, w, "abfahrt", "shell", "", "", WaitOpts{}),
		o.Host(ctx, w, "abfahrt", "logs", "x;id", "", WaitOpts{}),
		o.Host(ctx, w, "abfahrt", "logs", "", "1y", WaitOpts{}),
	} {
		if err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	if f.dispatchOK {
		t.Fatal("invalid input reached dispatch")
	}
}

func TestFindRunTimeout(t *testing.T) {
	newFake(t, "success")
	o := testOps(t)
	o.FindTimeout = 0 // the run "appears" only on the 3rd poll
	err := o.Deploy(context.Background(), &bytes.Buffer{}, "rollback", "abfahrt", "", WaitOpts{})
	if err == nil || !strings.Contains(err.Error(), "no run appeared") {
		t.Fatalf("err = %v", err)
	}
}

func TestReattachTimeout(t *testing.T) {
	newFake(t, "success")
	o := testOps(t)
	o.WaitInterval = time.Second
	err := o.Reattach(context.Background(), &bytes.Buffer{}, "42", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ops run 42") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigureGit(t *testing.T) {
	cfg := t.TempDir() + "/gitconfig"
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.WriteFile(cfg, []byte("[credential \"https://github.com\"]\n\thelper = !gh auth git-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for range 2 { // idempotent
		out.Reset()
		if err := ConfigureGit(&out); err != nil {
			t.Fatal(err)
		}
	}
	vals, err := gitConfigAll("--global", helperKey)
	if err != nil || !equal(vals, []string{"", gitHelperValue()}) {
		t.Fatalf("helpers = %q, %v", vals, err)
	}
	if v, _ := gitConfigAll("--global", httpPathKey); !equal(v, []string{"true"}) {
		t.Fatalf("useHttpPath = %q", v)
	}
	if !strings.Contains(out.String(), " git-credential\"") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAdminApplyInputs(t *testing.T) {
	f := newFake(t, "success")
	o := testOps(t)
	var out bytes.Buffer
	if err := o.Infra(context.Background(), &out, "apply", "cloudflare", InfraMode{Admin: true, ConfirmDestroy: 2, PlanRun: "43706653835"}, WaitOpts{NoWait: true}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.HasPrefix(out.String(), "apply (admin) of plan 43706653835 cloudflare at n0ci/platform@") {
		t.Errorf("output = %q", out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputs["mode"] != "admin" || f.inputs["confirm_destroy"] != "2" || f.inputs["action"] != "apply" || f.inputs["plan_run"] != "43706653835" {
		t.Errorf("inputs = %v", f.inputs)
	}
}

func TestApplyWithoutPlanOnlyPlans(t *testing.T) {
	f := newFake(t, "success")
	f.logs[11] += "2026-11-15T10:00:01.0000000Z OPS: destroys=1\n2026-11-15T10:00:01.0000000Z OPS: plan_run=4242\n"
	o := testOps(t)
	var out bytes.Buffer
	err := o.Infra(context.Background(), &out, "apply", "imout", InfraMode{Admin: true}, WaitOpts{})
	if !errors.Is(err, ErrNotApplied) {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "  ops apply imout --plan 4242 --admin --confirm-destroy 1\n") {
		t.Errorf("output = %s", out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputs["action"] != "plan" || f.inputs["plan_run"] != nil {
		t.Errorf("inputs = %v", f.inputs)
	}
}

func TestParseOPS(t *testing.T) {
	got := ParseOPS("Run: x\nOPS: plan_run=1\n  OPS: next=ops apply a --plan 1 (within 1h)\nOPS: result=noop\nOPS: result=success\n")
	if got["plan_run"] != "1" || got["next"] != "ops apply a --plan 1 (within 1h)" || got["result"] != "success" {
		t.Errorf("ParseOPS = %v", got)
	}
}

func TestHostExecInputs(t *testing.T) {
	f := newFake(t, "success")
	o := testOps(t)
	script := []byte("id\nwhoami\n")
	if err := o.HostExec(context.Background(), &bytes.Buffer{}, "imoutfyi", script, WaitOpts{NoWait: true}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	got, _ := base64.StdEncoding.DecodeString(fmt.Sprint(f.inputs["script_b64"]))
	if f.inputs["_workflow"] != "host.yml" || f.inputs["command"] != "exec" || f.inputs["app"] != "imoutfyi" || string(got) != string(script) {
		t.Errorf("inputs = %v", f.inputs)
	}
}

func TestGHArgsAndEnv(t *testing.T) {
	if got := GHArgs([]string{"--", "pr", "list"}); strings.Join(got, " ") != "pr list" {
		t.Errorf("GHArgs = %q", got)
	}
	if got := GHArgs([]string{"api", "--", "x"}); len(got) != 3 {
		t.Errorf("GHArgs kept only leading -- strip: %q", got)
	}
	env := ghEnv([]string{"PATH=/bin", "GH_TOKEN=personal", "GITHUB_TOKEN=x", "GH_HOST=ghe.example"}, "tok")
	if strings.Join(env, " ") != "PATH=/bin GH_TOKEN=tok GH_HOST=github.com" {
		t.Errorf("env = %q", env)
	}
}

func TestMCPInfraMode(t *testing.T) {
	m := infraMode(map[string]any{"admin": true, "confirm_destroy": float64(3), "plan": "77"})
	if !m.Admin || m.ConfirmDestroy != 3 || m.PlanRun != "77" {
		t.Errorf("infraMode = %+v", m)
	}
	if m := infraMode(map[string]any{}); m.Admin || m.ConfirmDestroy != 0 {
		t.Errorf("default infraMode = %+v", m)
	}
}
