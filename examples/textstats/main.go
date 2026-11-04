// textstats is a deliberately small, local-only MCP stdio server.
// It imports only the Go standard library; copy this directory to start a plugin.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const maxFrame = 64 * 1024
const maxText = 4096
const schema = `{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
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
type server struct{ initialized, ready bool }

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		// stdout is exclusively the protocol, never logs or banners.
		fmt.Fprintln(os.Stderr, "textstats:", err)
		os.Exit(1)
	}
}
func serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	enc := json.NewEncoder(out)
	s := &server{}
	for scanner.Scan() {
		if r := s.handle(scanner.Bytes()); r != nil {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
func failure(id json.RawMessage, code int, message string) *response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}}
}
func strict(raw json.RawMessage, value any) error {
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("expected object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("expected one object")
	}
	return nil
}
func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var value any
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	}
	return false
}
func (s *server) handle(line []byte) *response {
	if !json.Valid(line) {
		return failure(nil, -32700, "Parse error")
	}
	var q request
	if json.Unmarshal(line, &q) != nil || q.JSONRPC != "2.0" || q.Method == "" || !validID(q.ID) {
		return failure(nil, -32600, "Invalid Request")
	}
	// Notifications never receive replies. qi sends this after initialize.
	if len(q.ID) == 0 {
		if q.Method == "notifications/initialized" && s.initialized {
			s.ready = true
		}
		return nil
	}
	ok := func(v any) *response { return &response{JSONRPC: "2.0", ID: q.ID, Result: v} }
	bad := func(message string) *response { return failure(q.ID, -32602, message) }
	switch q.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
		}
		if json.Unmarshal(q.Params, &p) != nil || p.ProtocolVersion == "" || p.ClientInfo == nil || p.Capabilities == nil {
			return bad("Invalid initialize params")
		}
		if s.initialized {
			return failure(q.ID, -32600, "Already initialized")
		}
		s.initialized = true
		return ok(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "textstats", "version": "1.0.0"}})
	case "ping":
		return ok(map[string]any{})
	}
	if !s.ready {
		return failure(q.ID, -32600, "Initialize first")
	}
	switch q.Method {
	case "tools/list":
		if len(q.Params) > 0 {
			var p struct{}
			if strict(q.Params, &p) != nil {
				return bad("Pagination is not supported")
			}
		}
		return ok(map[string]any{"tools": []any{map[string]any{"name": "text_stats", "description": "Count Unicode characters and whitespace-separated words in supplied text, without reading files or using the network.", "inputSchema": json.RawMessage(schema)}}})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if strict(q.Params, &p) != nil {
			return bad("Invalid tool call")
		}
		if p.Name != "text_stats" {
			return bad("Unknown tool")
		}
		var a struct {
			Text *string `json:"text"`
		}
		if strict(p.Arguments, &a) != nil || a.Text == nil || utf8.RuneCountInString(*a.Text) > maxText {
			return bad("text must be a string of at most 4096 Unicode characters; no extra fields")
		}
		counts := fmt.Sprintf("characters=%d words=%d", utf8.RuneCountInString(*a.Text), len(strings.Fields(*a.Text)))
		return ok(map[string]any{"content": []any{map[string]string{"type": "text", "text": counts}}, "isError": false})
	default:
		return failure(q.ID, -32601, "Method not found")
	}
}
