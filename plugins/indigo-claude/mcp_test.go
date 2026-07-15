package main

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
