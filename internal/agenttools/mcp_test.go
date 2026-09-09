package agenttools

import (
	"encoding/json"
	"testing"
)

func newTestServer(callTool func(name string, input json.RawMessage) (string, bool)) *mcpServer {
	if callTool == nil {
		callTool = func(string, json.RawMessage) (string, bool) { return "", false }
	}
	return &mcpServer{callTool: callTool}
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bad response JSON: %v\n%s", err, raw)
	}
	return m
}

func TestMCPInitializeEchoesProtocolVersion(t *testing.T) {
	srv := newTestServer(nil)
	resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	m := decode(t, resp)
	result, _ := m["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result in response: %s", resp)
	}
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want 2025-06-18", got)
	}
	if m["id"] != float64(1) {
		t.Errorf("id = %v, want 1", m["id"])
	}
}

func TestMCPToolsListExposesBufferTools(t *testing.T) {
	srv := newTestServer(nil)
	resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	m := decode(t, resp)
	result, _ := m["result"].(map[string]any)
	tools, _ := result["tools"].([]any)

	names := map[string]bool{}
	for _, tl := range tools {
		tm, _ := tl.(map[string]any)
		name, _ := tm["name"].(string)
		names[name] = true
		if _, ok := tm["inputSchema"]; !ok {
			t.Errorf("tool %s missing inputSchema", name)
		}
	}
	if !names["read_file"] || !names["apply_edits"] {
		t.Errorf("tools = %v, want read_file and apply_edits", names)
	}
	if names["list_files"] || names["search_files"] {
		t.Errorf("list/search tools should not be exposed via MCP, got %v", names)
	}
}

func TestMCPToolsCallForwardsNameAndArguments(t *testing.T) {
	var gotName string
	var gotInput json.RawMessage
	srv := newTestServer(func(name string, input json.RawMessage) (string, bool) {
		gotName, gotInput = name, input
		return "file contents here", false
	})

	resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"main.go"}}}`))
	m := decode(t, resp)

	if gotName != "read_file" {
		t.Errorf("callTool name = %q, want read_file", gotName)
	}
	var in readFileInput
	if err := json.Unmarshal(gotInput, &in); err != nil || in.Path != "main.go" {
		t.Errorf("callTool input = %s, want path main.go", gotInput)
	}

	result, _ := m["result"].(map[string]any)
	if result["isError"] != false {
		t.Errorf("isError = %v, want false", result["isError"])
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "file contents here" {
		t.Errorf("content block = %v", block)
	}
}

func TestMCPToolsCallReportsToolError(t *testing.T) {
	srv := newTestServer(func(string, json.RawMessage) (string, bool) {
		return "old_text not found in main.go", true
	})
	resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"apply_edits","arguments":{}}}`))
	m := decode(t, resp)
	result, _ := m["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("isError = %v, want true", result["isError"])
	}
}

func TestMCPUnknownMethodReturnsError(t *testing.T) {
	srv := newTestServer(nil)
	resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`))
	m := decode(t, resp)
	errObj, _ := m["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected error response, got %s", resp)
	}
	if errObj["code"] != float64(-32601) {
		t.Errorf("error code = %v, want -32601", errObj["code"])
	}
}

func TestMCPNotificationsProduceNoResponse(t *testing.T) {
	srv := newTestServer(nil)
	if resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Errorf("notification got response: %s", resp)
	}
	// Unknown notification is also silently ignored.
	if resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled"}`)); resp != nil {
		t.Errorf("unknown notification got response: %s", resp)
	}
}

func TestMCPPing(t *testing.T) {
	srv := newTestServer(nil)
	m := decode(t, srv.handleMessage([]byte(`{"jsonrpc":"2.0","id":6,"method":"ping"}`)))
	if _, ok := m["result"]; !ok {
		t.Errorf("ping response missing result")
	}
}

// TestNegotiateProtocolVersion covers initialize's version handling. Echoing
// the client's request claims support for any revision it names, including
// future ones whose semantics this server does not implement; the spec's own
// answer for an unsupported revision is to reply with one the server does
// support and let the client decide whether to continue.
func TestNegotiateProtocolVersion(t *testing.T) {
	for _, v := range supportedProtocolVersions {
		if got := negotiateProtocolVersion(v); got != v {
			t.Errorf("negotiateProtocolVersion(%q) = %q, want it honoured", v, got)
		}
	}
	preferred := supportedProtocolVersions[0]
	for _, req := range []string{"", "2099-01-01", "nonsense"} {
		if got := negotiateProtocolVersion(req); got != preferred {
			t.Errorf("negotiateProtocolVersion(%q) = %q, want the preferred supported "+
				"revision %q rather than an echo", req, got, preferred)
		}
	}
}

// The same property through the actual initialize handler, so the wiring is
// covered and not just the helper.
func TestInitializeDoesNotEchoUnsupportedVersion(t *testing.T) {
	srv := newTestServer(func(string, json.RawMessage) (string, bool) { return "", false })
	resp := srv.handleMessage([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`))

	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("initialize response: %v (%s)", err, resp)
	}
	if out.Result.ProtocolVersion == "2099-01-01" {
		t.Error("initialize echoed a revision this server does not implement")
	}
	if out.Result.ProtocolVersion != supportedProtocolVersions[0] {
		t.Errorf("protocolVersion = %q, want %q", out.Result.ProtocolVersion, supportedProtocolVersions[0])
	}
}

// TestMCPMalformedInputIsAProtocolError pins the distinction handleMessage's
// nil return depends on.
//
// nil means "no reply is owed", which is true only of a valid notification.
// Using it for input that never parsed made the two indistinguishable — over
// stdio that was unhelpful silence, but over HTTP serveMCPPost turns nil into
// 202 Accepted, which tells a client its request was fine when it was not.
func TestMCPMalformedInputIsAProtocolError(t *testing.T) {
	srv := newTestServer(nil)

	for _, tc := range []struct {
		name, input string
		wantCode    float64
	}{
		// Not JSON at all.
		{"not json", `{ this is not json`, -32700},
		{"truncated", `{"jsonrpc":"2.0","id":1,`, -32700},
		// Valid JSON of the wrong shape. Reporting these as parse errors would
		// send a client hunting a syntax problem that is not there.
		{"empty array", `[]`, -32600},
		{"batch array", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, -32600},
		{"bare scalar", `42`, -32600},
		{"method not a string", `{"jsonrpc":"2.0","id":1,"method":1}`, -32600},
		{"missing jsonrpc", `{"id":1,"method":"ping"}`, -32600},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, -32600},
		{"missing method with an id", `{"jsonrpc":"2.0","id":1}`, -32600},
		{"id of the wrong type", `{"jsonrpc":"2.0","id":{},"method":"ping"}`, -32600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := srv.handleMessage([]byte(tc.input))
			if resp == nil {
				t.Fatal("malformed input produced no response; over HTTP that becomes a 202 " +
					"and the client believes the request succeeded")
			}
			m := decode(t, resp)
			errObj, _ := m["error"].(map[string]any)
			if errObj == nil {
				t.Fatalf("expected an error response, got %s", resp)
			}
			if errObj["code"] != tc.wantCode {
				t.Errorf("error code = %v, want %v", errObj["code"], tc.wantCode)
			}
			// JSON-RPC only permits echoing an id once the request it came
			// from is known to be well formed, which by definition none of
			// these are — so every reply here carries a null id even when an
			// id was present and readable.
			if id, present := m["id"]; !present || id != nil {
				t.Errorf("id = %v (present=%v), want null", id, present)
			}
		})
	}

	// Valid ids of every permitted type must still be accepted and echoed.
	for _, id := range []string{`1`, `"abc"`, `-7`, `null`} {
		in := `{"jsonrpc":"2.0","id":` + id + `,"method":"ping"}`
		resp := srv.handleMessage([]byte(in))
		if id == "null" {
			// A null id is a notification, so no reply is owed.
			if resp != nil {
				t.Errorf("id null got a response: %s", resp)
			}
			continue
		}
		if resp == nil {
			t.Errorf("valid request with id %s got no response", id)
			continue
		}
		if m := decode(t, resp); m["error"] != nil {
			t.Errorf("valid request with id %s was rejected: %s", id, resp)
		}
	}

	// A genuine notification must still produce nothing — that is the case the
	// nil return exists for.
	if resp := srv.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Errorf("valid notification got a response: %s", resp)
	}
}
