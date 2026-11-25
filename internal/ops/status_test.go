package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStuckQueues(t *testing.T) {
	old := time.Now().Add(-25 * time.Minute).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	queued := map[string][]string{
		"down":    {old, fresh, old}, // runs queued 25m, no runner online: warn
		"online":  {old},             // a runner is online (busy): fine
		"recent":  {fresh},           // queued only 2m: fine
		"idle":    nil,
		"noperms": {old},
	}
	runners := map[string]string{
		"down":   `{"runners":[{"status":"offline"},{"status":"offline"}]}`,
		"online": `{"runners":[{"status":"offline"},{"status":"online"}]}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":"tok-%s","expires_at":%q}`, r.PathValue("id"), time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("GET /repos/n0ci/{repo}/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") != "queued" || r.Header.Get("Authorization") != "token tok-456" {
			t.Errorf("runs request %s auth %q", r.URL, r.Header.Get("Authorization"))
		}
		var runs []map[string]any
		for _, c := range queued[r.PathValue("repo")] {
			runs = append(runs, map[string]any{"status": "queued", "created_at": c})
		}
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
	})
	mux.HandleFunc("GET /repos/n0ci/{repo}/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("repo")
		if repo == "recent" || repo == "idle" {
			t.Errorf("runners listed for %s without a stuck run", repo)
		}
		if b, ok := runners[repo]; ok {
			fmt.Fprint(w, b)
			return
		}
		http.Error(w, `{"message":"Resource not accessible by integration"}`, 403)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("OPS_GITHUB_API", srv.URL)

	got := testOps(t).stuckQueues(context.Background(), []string{"down", "online", "recent", "idle", "noperms"})
	if len(got) != 2 ||
		!strings.HasPrefix(got[0], "down: 2 run(s) queued > 10m0s (oldest 25m), no runner online") ||
		!strings.HasPrefix(got[1], "noperms: 1 run(s) queued > 10m0s; cannot list runners:") {
		t.Fatalf("got %q", got)
	}
}
