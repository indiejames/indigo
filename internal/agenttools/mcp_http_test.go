package agenttools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// echoServer is an mcpServer whose tool calls are stubbed, so these tests
// exercise the HTTP layer and nothing below it.
func echoServer() *mcpServer {
	return &mcpServer{callTool: func(name string, _ json.RawMessage) (string, bool) {
		return "called " + name, false
	}}
}

func post(t *testing.T, h http.Handler, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

// A request/response round trip must produce the same JSON-RPC the stdio
// transport produces — the transport moves bytes and must not alter protocol.
func TestMCPHTTPRoundTrip(t *testing.T) {
	h := mcpHTTPHandler(echoServer(), "")

	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Result.Tools) == 0 {
		t.Error("tools/list over HTTP returned no tools")
	}
}

// A notification has no reply. Answering 200 with an empty body would leave a
// client parsing "" as JSON-RPC.
func TestMCPHTTPNotificationIsAccepted(t *testing.T) {
	h := mcpHTTPHandler(echoServer(), "")
	resp := post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202 for a notification", resp.StatusCode)
	}
}

// GET must be refused rather than left hanging: this endpoint offers no
// server-initiated stream, and a client told otherwise would wait on one.
func TestMCPHTTPGetIsRefused(t *testing.T) {
	h := mcpHTTPHandler(echoServer(), "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", w.Result().StatusCode)
	}
}

// DNS-rebinding defence. A browser coerced into POSTing at a loopback port
// attaches an Origin naming the page; a real MCP client sends none.
func TestMCPHTTPRejectsCrossOrigin(t *testing.T) {
	h := mcpHTTPHandler(echoServer(), "")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	if got := post(t, h, body, map[string]string{"Origin": "https://evil.example"}).StatusCode; got != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403 — a web page could otherwise drive "+
			"the edit tools against the user's files", got)
	}
	for _, ok := range []string{"http://localhost:3000", "http://127.0.0.1:9999"} {
		if got := post(t, h, body, map[string]string{"Origin": ok}).StatusCode; got != http.StatusOK {
			t.Errorf("Origin %q status = %d, want 200", ok, got)
		}
	}
	// No Origin at all is every non-browser client, including Claude Code.
	if got := post(t, h, body, nil).StatusCode; got != http.StatusOK {
		t.Errorf("no-Origin status = %d, want 200", got)
	}
}

// The token gates every request when set, and is absent from the equation when
// not — so the default (loopback, no token) stays usable with no configuration.
func TestMCPHTTPBearerToken(t *testing.T) {
	h := mcpHTTPHandler(echoServer(), "sekrit")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	cases := []struct {
		name, auth string
		want       int
	}{
		{"correct", "Bearer sekrit", http.StatusOK},
		{"case-insensitive scheme", "bearer sekrit", http.StatusOK},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"no scheme", "sekrit", http.StatusUnauthorized},
		{"empty", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.auth != "" {
				hdr["Authorization"] = tc.auth
			}
			if got := post(t, h, body, hdr).StatusCode; got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}
