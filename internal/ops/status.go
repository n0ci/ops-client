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

// Status prints the last 10 platform-control runs and app health checks.
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
	return errors.Join(errs...)
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
