package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/n0ci/ops-client/internal/github"
)

// queuedAlert is how long a workflow run may sit queued, with no runner online
// for its repository, before ops status warns about it.
const queuedAlert = 10 * time.Minute

// Status prints the last 10 platform-control runs, app health checks and any
// repository whose CI is stuck without a runner.
func (o *Ops) Status(ctx context.Context, w io.Writer) error {
	var errs []error
	var rl github.RunList
	if err := o.GH.Do(ctx, "dispatch", "GET", o.runsPath("runs?per_page=10"), nil, &rl); err != nil {
		errs = append(errs, err)
		fmt.Fprintf(w, "runs: %v\n", err)
	} else {
		fmt.Fprintln(w, "Recent platform-control runs:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, r := range rl.Runs {
			st := r.Status
			if r.Status == "completed" {
				st = r.Conclusion
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.Name, st, age(time.Since(r.CreatedAt)), r.HTMLURL)
		}
		tw.Flush()
		if len(rl.Runs) == 0 {
			fmt.Fprintln(w, "  (none)")
		}
	}

	inv, err := o.Inventory(ctx)
	if err != nil {
		errs = append(errs, err)
		fmt.Fprintf(w, "health: %v\n", err)
		return errors.Join(errs...)
	}
	type target struct{ label, url string }
	var targets []target
	for _, a := range inv.Apps {
		if a.URL != "" {
			targets = append(targets, target{a.Name + " url", a.URL})
		}
		if a.API != "" {
			targets = append(targets, target{a.Name + " api", a.API})
		}
	}
	results := make([]string, len(targets))
	client := &http.Client{Timeout: 10 * time.Second}
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = probe(ctx, client, t.url)
		}()
	}
	wg.Wait()
	fmt.Fprintln(w, "App health:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for i, t := range targets {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", t.label, results[i], t.url)
	}
	tw.Flush()

	if stuck := o.stuckQueues(ctx, inv.Repos); len(stuck) > 0 {
		fmt.Fprintln(w, "CI runners:")
		for _, line := range stuck {
			fmt.Fprintln(w, "  "+line)
		}
	}
	return errors.Join(errs...)
}

// stuckQueues returns one line per repository that has a workflow run queued
// for longer than queuedAlert while none of its self-hosted runners is online
// (for example the Docker runners on the owner's Mac are down).
func (o *Ops) stuckQueues(ctx context.Context, repos []string) []string {
	lines := make([]string, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := fmt.Sprintf("/repos/%s/%s/actions/", o.B.Owner, repo)
			var rl github.RunList
			if err := o.GH.Do(ctx, "source", "GET", base+"runs?status=queued&per_page=100", nil, &rl); err != nil {
				lines[i] = fmt.Sprintf("%s: cannot list queued runs: %v", repo, err)
				return
			}
			n, oldest := 0, time.Duration(0)
			for _, r := range rl.Runs {
				if d := time.Since(r.CreatedAt); d > queuedAlert {
					n, oldest = n+1, max(oldest, d)
				}
			}
			if n == 0 {
				return
			}
			var runners struct {
				Runners []struct {
					Status string `json:"status"`
				} `json:"runners"`
			}
			if err := o.GH.Do(ctx, "source", "GET", base+"runners?per_page=100", nil, &runners); err != nil {
				lines[i] = fmt.Sprintf("%s: %d run(s) queued > %s; cannot list runners: %v", repo, n, queuedAlert, err)
				return
			}
			for _, r := range runners.Runners {
				if r.Status == "online" {
					return
				}
			}
			lines[i] = fmt.Sprintf("%s: %d run(s) queued > %s (oldest %s), no runner online: start Docker Desktop, then ~/.gh-runner/start.sh",
				repo, n, queuedAlert, strings.TrimSuffix(age(oldest), " ago"))
		}()
	}
	wg.Wait()
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func probe(ctx context.Context, c *http.Client, url string) string {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return "skipped (not http)"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "error: " + err.Error()
	}
	req.Header.Set("User-Agent", github.UserAgent)
	start := time.Now()
	resp, err := c.Do(req)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Sprintf("error after %dms: %v", ms, err)
	}
	resp.Body.Close()
	return fmt.Sprintf("%d in %dms", resp.StatusCode, ms)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
