package mcp

import "encoding/json"

// rpcRequest is an incoming JSON-RPC 2.0 message. A request carries an id and
// expects a response; a notification omits the id and gets none.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is an outgoing JSON-RPC 2.0 response: exactly one of Result or
// Error is set, echoing the request id.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ok builds a successful response for the given request id and result.
func ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

// textResult is a successful MCP tool result carrying one text block.
func textResult(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// errorResult is a tool result flagged as an error: the model sees the message
// and can adjust, rather than the call failing at the protocol level.
func errorResult(text string) map[string]any {
	r := textResult(text)
	r["isError"] = true
	return r
}

// args is a decoded tool-call argument object, with typed accessors that tolerate
// missing keys (JSON numbers/arrays arrive as float64/[]any).
type args map[string]any

func (a args) str(key string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return ""
}

func (a args) boolDefault(key string, def bool) bool {
	if v, ok := a[key].(bool); ok {
		return v
	}
	return def
}

func (a args) strSlice(key string) []string {
	raw, ok := a[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
