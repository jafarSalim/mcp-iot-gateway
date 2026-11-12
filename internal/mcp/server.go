// Package mcp is a small Model Context Protocol server for the stdio transport:
// newline-delimited JSON-RPC 2.0 with initialize, ping, tools/list and tools/call.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const ProtocolVersion = "2025-06-18"

// Tool is one callable tool. Handler returns text for the model, or an error shown as isError.
type Tool struct {
	Name        string                                     `json:"name"`
	Title       string                                     `json:"title,omitempty"`
	Description string                                     `json:"description"`
	InputSchema json.RawMessage                            `json:"inputSchema"`
	Annotations map[string]any                             `json:"annotations,omitempty"`
	Handler     func(args json.RawMessage) (string, error) `json:"-"`
}

type Server struct {
	Name, Version, Instructions string
	tools                       []Tool
	out                         io.Writer
	wm                          sync.Mutex
}

func NewServer(name, version string) *Server { return &Server{Name: name, Version: version} }

func (s *Server) AddTool(t Tool) { s.tools = append(s.tools, t) }

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

func (s *Server) send(v any) {
	b, _ := json.Marshal(v)
	s.wm.Lock()
	defer s.wm.Unlock()
	s.out.Write(append(b, '\n'))
}

func (s *Server) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) fail(id json.RawMessage, code int, msg string) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{code, msg}})
}

// Serve reads requests from in and writes responses to out until in is closed.
// Tool calls run concurrently, so a slow device does not block other requests.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.fail(json.RawMessage("null"), -32700, "parse error")
			continue
		}
		if len(req.ID) == 0 { // notification: nothing to answer
			continue
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			v := ProtocolVersion
			if p.ProtocolVersion != "" {
				v = p.ProtocolVersion // echo the client's version; this server uses no version-specific features
			}
			s.reply(req.ID, map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
				"instructions":    s.Instructions,
			})
		case "ping":
			s.reply(req.ID, map[string]any{})
		case "tools/list":
			s.reply(req.ID, map[string]any{"tools": s.tools})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				s.fail(req.ID, -32602, "invalid params")
				continue
			}
			var tool *Tool
			for i := range s.tools {
				if s.tools[i].Name == p.Name {
					tool = &s.tools[i]
				}
			}
			if tool == nil {
				s.fail(req.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
				continue
			}
			if len(p.Arguments) == 0 {
				p.Arguments = json.RawMessage("{}")
			}
			wg.Add(1)
			go func(id json.RawMessage, t *Tool, args json.RawMessage) {
				defer wg.Done()
				text, err := t.Handler(args)
				if err != nil {
					s.reply(id, map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true})
					return
				}
				s.reply(id, map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false})
			}(req.ID, tool, p.Arguments)
		default:
			s.fail(req.ID, -32601, "method not found: "+req.Method)
		}
	}
	wg.Wait()
	return sc.Err()
}
