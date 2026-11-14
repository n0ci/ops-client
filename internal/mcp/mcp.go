// Package mcp is a minimal Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0) supporting initialize, ping, tools/list
// and tools/call.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2025-06-18"

// Tool is one callable tool. Handler output is returned as text content;
// a handler error is reported as a tool result with isError set.
type Tool struct {
	Name        string                                                         `json:"name"`
	Description string                                                         `json:"description"`
	InputSchema map[string]any                                                 `json:"inputSchema"`
	Handler     func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
}

// Server serves tools.
type Server struct {
	Name, Version string
	Tools         []Tool

	mu      sync.Mutex // guards out and cancels
	out     io.Writer
	cancels map[string]context.CancelFunc
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from in and writes responses to out until EOF.
// tools/call runs concurrently so long waits don't block pings.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out, s.cancels = out, map[string]context.CancelFunc{}
	var wg sync.WaitGroup
	defer wg.Wait()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
			s.send(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" { // notification
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(req.Params, &p)
				s.mu.Lock()
				if c := s.cancels[string(p.RequestID)]; c != nil {
					c()
				}
				s.mu.Unlock()
			}
			continue
		}
		if req.Method == "tools/call" {
			cctx, cancel := context.WithCancel(ctx)
			s.mu.Lock()
			s.cancels[string(req.ID)] = cancel
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, rerr := s.call(cctx, req.Params)
				s.mu.Lock()
				delete(s.cancels, string(req.ID))
				s.mu.Unlock()
				cancel()
				s.send(response{ID: req.ID, Result: res, Error: rerr})
			}()
			continue
		}
		res, rerr := s.handle(req)
		s.send(response{ID: req.ID, Result: res, Error: rerr})
	}
	return sc.Err()
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{-32602, "invalid params"}
	}
	for _, t := range s.Tools {
		if t.Name != p.Name {
			continue
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		text, err := t.Handler(ctx, p.Arguments)
		if err != nil {
			if text != "" {
				text += "\n"
			}
			text += "error: " + err.Error()
		}
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": err != nil,
		}, nil
	}
	return nil, &rpcError{-32602, "unknown tool: " + p.Name}
}

func (s *Server) send(r response) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{-32603, "internal error"}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(b, '\n'))
}
