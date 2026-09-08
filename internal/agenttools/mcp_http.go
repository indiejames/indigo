package agenttools

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ─── MCP over HTTP ───────────────────────────────────────────────────────────
//
// The stdio transport requires the client to *spawn* `indigo --mcp`, which ties
// the agent to the same machine, filesystem and uid as the editor. That breaks
// down as soon as the agent runs somewhere else — most commonly in a container,
// where reaching the editor's Unix socket means aligning the workspace path (it
// is hashed into the socket name), the uid (it is in the directory name), and
// getting a socket across the container boundary at all, which Docker Desktop
// on macOS cannot do for socket files.
//
// Over HTTP none of that applies: the server runs beside the editor, and the
// client needs only a URL.
//
// This implements the "Streamable HTTP" transport: one endpoint, JSON-RPC in a
// POST body, the response as JSON. Server-initiated streaming (the optional
// GET/SSE half) is not implemented because nothing here initiates anything —
// every message is a reply to a call — and the spec's answer for an endpoint
// that offers no stream is to refuse the GET, which is what this does.

// defaultMCPHTTPAddr binds loopback, not all interfaces. An MCP endpoint is
// unauthenticated by default and exposes the editor's buffers and an edit tool,
// so the default has to be the one that is safe when someone does not think
// about it. Reaching it from a container means binding something wider on
// purpose — see docs/agent-integration.md.
const defaultMCPHTTPAddr = "127.0.0.1:7391"

// mcpHTTPTokenEnv names an optional shared secret. When set, every request must
// carry `Authorization: Bearer <token>`.
//
// This exists so binding beyond loopback is a defensible choice rather than an
// unavoidably reckless one. It is not offered as strong authentication — it is
// a bearer token over plain HTTP — but it is the difference between "anything
// that can route to this port can edit your files" and "and knows a secret".
const mcpHTTPTokenEnv = "INDIGO_MCP_TOKEN" //nolint:gosec // name of a variable, not a credential

// maxMCPHTTPBody bounds a request body, mirroring the stdio scanner's line cap.
const maxMCPHTTPBody = mcpMaxLine

// RunHTTP serves MCP over HTTP on addr until the process is killed. An empty
// addr means defaultMCPHTTPAddr.
func RunHTTP(addr string) {
	if addr == "" {
		addr = defaultMCPHTTPAddr
	}
	srv := &mcpServer{callTool: workspaceToolCaller()}
	token := os.Getenv(mcpHTTPTokenEnv)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		mcpFatal("cannot listen on %s: %v", addr, err)
	}
	// stdout is not a transport in this mode, but the startup line goes to
	// stderr anyway: a future reader copying this pattern should not learn
	// that writing to stdout is fine here when it is fatal in the stdio mode.
	fmt.Fprintf(os.Stderr, "indigo: MCP over HTTP on http://%s\n", ln.Addr())
	if token == "" && !isLoopbackAddr(ln.Addr()) {
		fmt.Fprintf(os.Stderr,
			"indigo: warning — listening beyond loopback with no %s set, so anything that can "+
				"reach this port can read and edit your files\n", mcpHTTPTokenEnv)
	}

	httpSrv := &http.Server{
		Handler: mcpHTTPHandler(srv, token),
		// A tool call can legitimately take a while (an edit waits on the
		// server's format-on-save), so the write budget is the tool budget plus
		// room to send the reply. No read timeout beyond headers: bodies here
		// are small and arrive at once.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      mcpToolTimeout + 30*time.Second,
	}
	if err := httpSrv.Serve(ln); err != nil {
		mcpFatal("http server: %v", err)
	}
}

func mcpHTTPHandler(srv *mcpServer, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !checkOrigin(r) {
			// DNS-rebinding defence: a browser on some page can be made to POST
			// to a loopback port, and would attach an Origin naming that page.
			// A real MCP client is not a browser and sends none, so rejecting
			// only cross-origin values costs nothing and closes that path.
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		if token != "" && !hasBearer(r, token) {
			http.Error(w, "missing or incorrect bearer token", http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodPost:
			serveMCPPost(w, r, srv)
		case http.MethodGet:
			// No server-initiated stream is offered; the spec's prescribed
			// answer is 405 rather than an empty stream a client would wait on.
			http.Error(w, "this endpoint offers no event stream", http.StatusMethodNotAllowed)
		case http.MethodDelete:
			// Session teardown. This transport is stateless — there is no
			// session to end — so acknowledge rather than fail a client that
			// tidies up after itself.
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func serveMCPPost(w http.ResponseWriter, r *http.Request, srv *mcpServer) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPHTTPBody))
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}
	resp := srv.handleMessage(body)
	if resp == nil {
		// A notification, or something unparseable. Either way there is no
		// reply to send, which over HTTP is 202 rather than an empty 200 body
		// that a client would try to parse as JSON-RPC.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp) //nolint:errcheck
}

// checkOrigin accepts a request with no Origin (every non-browser client) and
// one naming a loopback host, and rejects anything else.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func hasBearer(r *http.Request, token string) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(got) <= len(prefix) || !strings.EqualFold(got[:len(prefix)], prefix) {
		return false
	}
	// Constant time: the comparison is against a secret, and a byte-at-a-time
	// early exit leaks it to anything that can time requests.
	return subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(token)) == 1
}

func isLoopbackAddr(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
