package ops

import (
	"archive/zip"
	"bytes"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/n0ci/ops-client/internal/github"
)

var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)

// cleanLine strips the BOM, CR and the Actions timestamp prefix.
func cleanLine(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "\uFEFF"), "\r")
	return tsRe.ReplaceAllString(s, "")
}

// ExtractOPS returns the "OPS: key=value" lines and the text between
// "OPS-BEGIN" and "OPS-END" marker lines of an Actions job log.
func ExtractOPS(log string) (ops, block []string) {
	in := false
	for _, l := range strings.Split(log, "\n") {
		l = cleanLine(l)
		switch t := strings.TrimSpace(l); {
		case t == "OPS-BEGIN":
			in = true
		case t == "OPS-END":
			in = false
		case in:
			block = append(block, l)
		case strings.HasPrefix(l, "OPS: "):
			ops = append(ops, l)
		}
	}
	return ops, block
}

// MatchRun finds the run whose display_title carries "[requestID]".
func MatchRun(runs []github.Run, requestID string) (github.Run, bool) {
	tag := "[" + requestID + "]"
	for _, r := range runs {
		if strings.Contains(r.DisplayTitle, tag) {
			return r, true
		}
	}
	return github.Run{}, false
}

// logText returns the log as text, unpacking it if GitHub served a zip.
func logText(b []byte) string {
	if !bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		return string(b)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return ""
	}
	files := append([]*zip.File(nil), zr.File...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var sb strings.Builder
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		_, _ = io.Copy(&sb, io.LimitReader(rc, 64<<20))
		rc.Close()
		sb.WriteString("\n")
	}
	return sb.String()
}
