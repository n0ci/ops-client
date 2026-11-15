package ops

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

// Input patterns from platform-control/docs/CONTRACT.md. The executor
// workflows validate again; these exist so mistakes fail fast and locally.
var (
	stackRe     = regexp.MustCompile(`^[a-z0-9-]+$`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	appRe       = regexp.MustCompile(`^(abfahrt|imoutfyi)$`)
	targetRe    = regexp.MustCompile(`^[a-z0-9_.@-]{0,64}$`)
	sinceRe     = regexp.MustCompile(`^[0-9]{1,3}[smhd]$`)
	hostCmdRe   = regexp.MustCompile(`^(status|ps|logs|df|journal|errors)$`)
	requestIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	repoNameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	planRunRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

func check(re *regexp.Regexp, what, v string) error {
	if !re.MatchString(v) {
		return fmt.Errorf("invalid %s %q (must match %s)", what, v, re)
	}
	return nil
}

func ValidStack(s string) error       { return check(stackRe, "stack", s) }
func ValidApp(s string) error         { return check(appRe, "app", s) }
func ValidTarget(s string) error      { return check(targetRe, "target", s) }
func ValidSince(s string) error       { return check(sinceRe, "since", s) }
func ValidHostCommand(s string) error { return check(hostCmdRe, "host command", s) }
func ValidRequestID(s string) error   { return check(requestIDRe, "request_id", s) }
func ValidPlanRun(s string) error     { return check(planRunRe, "plan run id", s) }

// ValidRevision accepts a 40-hex commit SHA or "" (latest successful build).
func ValidRevision(s string) error {
	if s == "" {
		return nil
	}
	return check(shaRe, "revision", s)
}

// ValidSHA requires a 40-hex commit SHA.
func ValidSHA(s string) error { return check(shaRe, "commit SHA", s) }

// NewRequestID returns a random UUIDv4.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
