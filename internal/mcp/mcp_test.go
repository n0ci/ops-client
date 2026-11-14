package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	s := &Server{Name: "ops", Version: "test", Tools: []Tool{
		{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"},
			Handler: func(_ context.Context, a map[string]any) (string, error) { return "hi " + a["who"].(string), nil }},
		{Name: "fail", Description: "fail", InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (string, error) { return "Run: x", errors.New("boom") }},
	}}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"who":"agent"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"missing"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), strings.NewReader(in), pw); pw.Close() }()

	got := map[string]map[string]any{}
	sc := bufio.NewScanner(pr)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("missing jsonrpc: %v", m)
		}
		id, _ := json.Marshal(m["id"])
		got[string(id)] = m
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 { // 7 requests + 1 parse error; no reply to the notification
		t.Fatalf("got %d responses: %v", len(got), got)
	}
	res := func(id string) map[string]any { r, _ := got[id]["result"].(map[string]any); return r }
	if r := res("1"); r["protocolVersion"] != ProtocolVersion || r["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize = %v", got["1"])
	}
	if tools := res(`"two"`)["tools"].([]any); len(tools) != 2 || tools[0].(map[string]any)["inputSchema"] == nil {
		t.Fatalf("tools/list = %v", got[`"two"`])
	}
	text := func(id string) (string, bool) {
		r := res(id)
		c := r["content"].([]any)[0].(map[string]any)
		if c["type"] != "text" {
			t.Fatalf("content type %v", c["type"])
		}
		return c["text"].(string), r["isError"].(bool)
	}
	if s, isErr := text("3"); s != "hi agent" || isErr {
		t.Fatalf("echo = %q %v", s, isErr)
	}
	if s, isErr := text("4"); s != "Run: x\nerror: boom" || !isErr {
		t.Fatalf("fail = %q %v", s, isErr)
	}
	for id, code := range map[string]float64{"5": -32602, "6": -32601, "null": -32700} {
		if e, _ := got[id]["error"].(map[string]any); e == nil || e["code"] != code {
			t.Errorf("id %s: %v", id, got[id])
		}
	}
	if res("7") == nil {
		t.Fatal("ping had no result")
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	s := &Server{Tools: []Tool{{Name: "wait", Handler: func(ctx context.Context, _ map[string]any) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { s.Serve(context.Background(), inR, outW); outW.Close() }()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"wait"}}`+"\n")
	<-started
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`+"\n")
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil || !strings.Contains(line, `"isError":true`) || !strings.Contains(line, "canceled") {
		t.Fatalf("line = %q, %v", line, err)
	}
	inW.Close()
}
