package yamlite

import (
	"reflect"
	"testing"
)

const inventory = `# Everything an agent needs. No secrets.
owner: n0ci
repositories:
  - name: platform          # this repo
  - name: "platform-control"
apps:
  abfahrt:
    repos: [abfahrt-backend, abfahrt-app]
    url: https://abfahrt.app
    api: https://api.abfahrt.app/v1/health/ready
  imoutfyi:
    repos: [imoutfyi]
    url: 'https://imout.fyi' # quoted
providers:
  oci: {tenancy: fabionocera97, region: eu-frankfurt-1}
  cloudflare: {zones: [abfahrt.app, imout.fyi]}
list:
- a
- b: 1
  c: 2
empty:
`

func TestParse(t *testing.T) {
	v, err := Parse([]byte(inventory))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"owner":        "n0ci",
		"repositories": []any{map[string]any{"name": "platform"}, map[string]any{"name": "platform-control"}},
		"apps": map[string]any{
			"abfahrt": map[string]any{
				"repos": []any{"abfahrt-backend", "abfahrt-app"},
				"url":   "https://abfahrt.app",
				"api":   "https://api.abfahrt.app/v1/health/ready",
			},
			"imoutfyi": map[string]any{"repos": []any{"imoutfyi"}, "url": "https://imout.fyi"},
		},
		"providers": map[string]any{
			"oci":        map[string]any{"tenancy": "fabionocera97", "region": "eu-frankfurt-1"},
			"cloudflare": map[string]any{"zones": []any{"abfahrt.app", "imout.fyi"}},
		},
		"list":  []any{"a", map[string]any{"b": "1", "c": "2"}},
		"empty": nil,
	}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got  %#v\nwant %#v", v, want)
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"a: 1\n   b: 2\n",
		"a: [1, 2\n",
		"just a scalar line\nb: 1\n",
		"a:\n\t- x\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}
