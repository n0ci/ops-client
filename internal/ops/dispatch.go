// Package ops implements the ops commands on top of the GitHub client.
package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/n0ci/ops-client/internal/bundle"
	"github.com/n0ci/ops-client/internal/github"
)

const (
	controlRepo  = "platform-control"
	platformRepo = "platform"
	// DefaultTimeout bounds waiting for a dispatched run to complete.
	DefaultTimeout = 45 * time.Minute
)

// Ops carries the loaded bundle, the GitHub client and polling settings.
type Ops struct {
	B  *bundle.Bundle
	GH *github.Client

	FindInterval time.Duration // poll for the dispatched run (3s)
	FindTimeout  time.Duration // give up finding the run (90s)
	WaitInterval time.Duration // poll a run until completed (10s)
}

// New returns Ops with the contract's default polling intervals.
func New(b *bundle.Bundle) *Ops {
	return &Ops{B: b, GH: github.New(b), FindInterval: 3 * time.Second, FindTimeout: 90 * time.Second, WaitInterval: 10 * time.Second}
}

// WaitOpts controls what happens after dispatch.
type WaitOpts struct {
	NoWait  bool
	Timeout time.Duration
}

// RunFailedError reports a run that completed without success.
type RunFailedError struct {
	URL, Conclusion string
}

func (e *RunFailedError) Error() string {
	return fmt.Sprintf("run concluded %q: %s", e.Conclusion, e.URL)
}

func (o *Ops) runsPath(suffix string) string {
	return fmt.Sprintf("/repos/%s/%s/actions/%s", o.B.Owner, controlRepo, suffix)
}

// Dispatch triggers workflow on platform-control main with inputs plus a
// fresh request_id, finds the resulting run, prints its URL and (unless
// opt.NoWait) waits for it and prints its results.
func (o *Ops) Dispatch(ctx context.Context, w io.Writer, workflow string, inputs map[string]string, opt WaitOpts) error {
	id := NewRequestID()
	in := map[string]string{"request_id": id}
	for k, v := range inputs {
		in[k] = v
	}
	body := map[string]any{"ref": "main", "inputs": in}
	if err := o.GH.Do(ctx, "dispatch", "POST", o.runsPath("workflows/"+workflow+"/dispatches"), body, nil); err != nil {
		return err
	}
	run, err := o.findRun(ctx, workflow, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Run: %s\n", run.HTMLURL)
	if opt.NoWait {
		return nil
	}
	return o.Wait(ctx, w, run.ID, opt.Timeout)
}

func (o *Ops) findRun(ctx context.Context, workflow, id string) (github.Run, error) {
	deadline := time.Now().Add(o.FindTimeout)
	path := o.runsPath("workflows/" + workflow + "/runs?event=workflow_dispatch&per_page=20")
	for {
		var rl github.RunList
		err := o.GH.Do(ctx, "dispatch", "GET", path, nil, &rl)
		if err != nil {
			return github.Run{}, err
		}
		if r, ok := MatchRun(rl.Runs, id); ok {
			return r, nil
		}
		if time.Now().After(deadline) {
			return github.Run{}, fmt.Errorf("dispatched %s (request_id %s) but no run appeared within %s; see https://github.com/%s/%s/actions",
				workflow, id, o.FindTimeout, o.B.Owner, controlRepo)
		}
		if err := sleep(ctx, o.FindInterval); err != nil {
			return github.Run{}, err
		}
	}
}

// Wait polls run until it completes, then prints its OPS: lines, any
// OPS-BEGIN/OPS-END block and the conclusion. It returns nil only when the
// run concluded "success".
func (o *Ops) Wait(ctx context.Context, w io.Writer, runID int64, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var run github.Run
	last := ""
	for {
		if err := o.GH.Do(wctx, "dispatch", "GET", o.runsPath(fmt.Sprintf("runs/%d", runID)), nil, &run); err != nil {
			if errors.Is(wctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timed out after %s; reattach with: ops run %d", timeout, runID)
			}
			return err
		}
		if run.Status == "completed" {
			break
		}
		if run.Status != last {
			fmt.Fprintf(w, "Status: %s\n", run.Status)
			last = run.Status
		}
		if err := sleep(wctx, o.WaitInterval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("timed out after %s; reattach with: ops run %d", timeout, runID)
			}
			return err
		}
	}
	if err := o.printResults(ctx, w, runID); err != nil {
		fmt.Fprintf(w, "warning: could not read job logs: %v\n", err)
	}
	fmt.Fprintf(w, "Conclusion: %s\n", run.Conclusion)
	if run.Conclusion != "success" {
		return &RunFailedError{URL: run.HTMLURL, Conclusion: run.Conclusion}
	}
	return nil
}

func (o *Ops) printResults(ctx context.Context, w io.Writer, runID int64) error {
	var jl github.JobList
	if err := o.GH.Do(ctx, "dispatch", "GET", o.runsPath(fmt.Sprintf("runs/%d/jobs?per_page=100", runID)), nil, &jl); err != nil {
		return err
	}
	var lines, block []string
	for _, j := range jl.Jobs {
		var data []byte
		var err error
		for attempt := 0; attempt < 3; attempt++ { // logs can lag completion briefly
			if data, err = o.GH.Download(ctx, "dispatch", o.runsPath(fmt.Sprintf("jobs/%d/logs", j.ID))); err == nil {
				break
			}
			if serr := sleep(ctx, o.FindInterval); serr != nil {
				return serr
			}
		}
		if err != nil {
			return fmt.Errorf("job %q: %w", j.Name, err)
		}
		l, b := ExtractOPS(logText(data))
		lines, block = append(lines, l...), append(block, b...)
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if len(block) > 0 {
		fmt.Fprintln(w, "--- output ---")
		fmt.Fprintln(w, strings.Join(block, "\n"))
		fmt.Fprintln(w, "--- end ---")
	}
	return nil
}

// Reattach waits for an existing platform-control run (id or run URL).
func (o *Ops) Reattach(ctx context.Context, w io.Writer, ref string, timeout time.Duration) error {
	id, err := ParseRunID(ref)
	if err != nil {
		return err
	}
	var run github.Run
	if err := o.GH.Do(ctx, "dispatch", "GET", o.runsPath(fmt.Sprintf("runs/%d", id)), nil, &run); err != nil {
		return err
	}
	fmt.Fprintf(w, "Run: %s\n", run.HTMLURL)
	return o.Wait(ctx, w, id, timeout)
}

// ParseRunID accepts "123" or ".../actions/runs/123[/...]".
func ParseRunID(ref string) (int64, error) {
	s := ref
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		s = ""
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "runs" {
				s = parts[i+1]
			}
		}
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid run id %q", ref)
	}
	return id, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// PlatformHead returns the HEAD commit SHA of platform main.
func (o *Ops) PlatformHead(ctx context.Context) (string, error) {
	var c struct {
		SHA string `json:"sha"`
	}
	if err := o.GH.Do(ctx, "source", "GET", fmt.Sprintf("/repos/%s/%s/commits/main", o.B.Owner, platformRepo), nil, &c); err != nil {
		return "", err
	}
	return c.SHA, ValidSHA(c.SHA)
}

// Infra dispatches infra.yml (action "plan" or "apply") at platform main HEAD.
func (o *Ops) Infra(ctx context.Context, w io.Writer, action, stack string, opt WaitOpts) error {
	if err := ValidStack(stack); err != nil {
		return err
	}
	if action != "plan" && action != "apply" {
		return fmt.Errorf("invalid infra action %q", action)
	}
	ref, err := o.PlatformHead(ctx)
	if err != nil {
		return fmt.Errorf("resolve platform main: %w", err)
	}
	fmt.Fprintf(w, "%s %s at %s/%s@%s\n", action, stack, o.B.Owner, platformRepo, ref[:12])
	return o.Dispatch(ctx, w, "infra.yml", map[string]string{"stack": stack, "platform_ref": ref, "action": action}, opt)
}

// Deploy dispatches deploy.yml. action is "deploy" or "rollback"; an empty
// revision means the latest successful build on main.
func (o *Ops) Deploy(ctx context.Context, w io.Writer, action, app, revision string, opt WaitOpts) error {
	if err := ValidApp(app); err != nil {
		return err
	}
	if err := ValidRevision(revision); err != nil {
		return err
	}
	if action != "deploy" && action != "rollback" {
		return fmt.Errorf("invalid deploy action %q", action)
	}
	return o.Dispatch(ctx, w, "deploy.yml", map[string]string{"app": app, "action": action, "revision": revision}, opt)
}

// Host dispatches host.yml for read-only diagnostics.
func (o *Ops) Host(ctx context.Context, w io.Writer, app, command, target, since string, opt WaitOpts) error {
	if since == "" {
		since = "30m"
	}
	for _, err := range []error{ValidApp(app), ValidHostCommand(command), ValidTarget(target), ValidSince(since)} {
		if err != nil {
			return err
		}
	}
	return o.Dispatch(ctx, w, "host.yml", map[string]string{"app": app, "command": command, "target": target, "since": since}, opt)
}
