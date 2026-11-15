package ops

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
)

// GHArgs drops a leading "--" so `ops gh -- <args>` and `ops gh <args>` agree.
func GHArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// ghEnv returns environ with any other GitHub credential or host replaced by
// the source-app token, so gh never falls back to a personal login.
func ghEnv(environ []string, token string) []string {
	var env []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GH_TOKEN="+token, "GH_HOST=github.com")
}

// RunGH runs gh with a fresh source-app installation token. The source app
// is installed on every repository, platform-control included.
func (o *Ops) RunGH(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	t, err := o.GH.Token(ctx, "source")
	if err != nil {
		return err
	}
	c := exec.CommandContext(ctx, "gh", GHArgs(args)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	c.Env = ghEnv(os.Environ(), t.Value)
	return c.Run()
}
