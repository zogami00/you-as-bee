// Package api implements the yabd management HTTP API on port 3241 and a typed
// standard-library client for it.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/webui"
)

// Backend is the device state the API exposes. It is implemented by the
// reconciler and, in tests, by a fake.
type Backend interface {
	Info(ctx context.Context) proto.Info
	Devices(ctx context.Context) []proto.Device
	Device(ctx context.Context, id string) (proto.Device, bool)
	Export(ctx context.Context, id string, force bool) error
	Unexport(ctx context.Context, id string) error
	Reset(ctx context.Context, id string) error
	Subscribe(ctx context.Context) (<-chan proto.Event, func())
}

// Config configures a Server.
type Config struct {
	// Listen is the TCP address, normally 0.0.0.0:3241.
	Listen string
	// Token is the bearer token required on every route except /healthz.
	Token string
	// AllowedClients is the CIDR allowlist checked against the peer address.
	AllowedClients []string
	// Backend supplies device state.
	Backend Backend
	// UnknownErr is the sentinel a backend uses to signal "unknown device". It
	// is wired at construction so the api package never imports the agent.
	UnknownErr error
	// Log receives request errors. May be nil.
	Log *slog.Logger
	// WebUI enables the embedded browser UI and session-cookie auth on /ui/.
	// It is opt-in (agent config web_ui), so the bearer-token surface is
	// unchanged when it is false.
	WebUI bool
	// Logs, when non-nil, is served by GET /v1/logs. It is the same ring the
	// process logger fans out to.
	Logs *webui.LogRing
}

// Server is the management HTTP server.
type Server struct {
	token      string
	allowed    []*net.IPNet
	backend    Backend
	unknownErr error
	log        *slog.Logger
	webUI      bool
	logs       *webui.LogRing
	sessions   *webui.Sessions
	// codes is retained for the Windows client's login flow in the next
	// milestone. The Pi login flow no longer issues or redeems a one-time code
	// (the token in the form is sufficient and the code was a flood vector), so
	// nothing in this binary calls Issue or Redeem. The table is bounded.
	codes   *webui.OneTimeCodes
	login   *loginLimiter
	mux     *http.ServeMux
	handler http.Handler
	// guardedRoutes records every /v1 route, as "METHOD /pattern". Tests
	// enumerate it so a new guarded route cannot be added without being
	// covered.
	guardedRoutes []string
	// allRoutes records every pattern registered on the mux, including the
	// public and UI routes, as "METHOD /pattern". Tests enumerate it to assert
	// that every registered route is either explicitly public (/healthz) or
	// rejects a non-allowlisted peer.
	allRoutes []string
	http      *http.Server
}

// New builds a Server from cfg.
func New(cfg Config) (*Server, error) {
	s := &Server{
		token:      cfg.Token,
		backend:    cfg.Backend,
		unknownErr: cfg.UnknownErr,
		log:        cfg.Log,
		webUI:      cfg.WebUI,
		logs:       cfg.Logs,
		sessions:   webui.NewSessions(),
		codes:      webui.NewOneTimeCodes(),
		login:      newLoginLimiter(),
		mux:        http.NewServeMux(),
	}
	for _, cidr := range cfg.AllowedClients {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("api: invalid allowed client %q: %w", cidr, err)
		}
		s.allowed = append(s.allowed, network)
	}

	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.allRoutes = append(s.allRoutes, "GET /healthz")
	// Every guarded route is registered from this table, which is also what
	// TestEveryGuardedRouteRequiresToken enumerates. The handlers are not
	// wrapped individually: the front door (see frontDoor) applies guard to
	// every request that is not /healthz or under /ui/, so a route added to
	// the mux is guarded even if it is added here by mistake.
	guarded := []struct {
		method  string
		pattern string
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/v1/info", s.handleInfo},
		{http.MethodGet, "/v1/devices", s.handleDevices},
		{http.MethodGet, "/v1/devices/{id}", s.handleDevice},
		{http.MethodPost, "/v1/devices/{id}/export", s.handleExport},
		{http.MethodPost, "/v1/devices/{id}/unexport", s.handleUnexport},
		{http.MethodPost, "/v1/devices/{id}/reset", s.handleReset},
		{http.MethodGet, "/v1/events", s.handleEvents},
		{http.MethodGet, "/v1/logs", s.handleLogs},
	}
	for _, rt := range guarded {
		pattern := rt.method + " " + rt.pattern
		s.mux.Handle(pattern, rt.handler)
		s.guardedRoutes = append(s.guardedRoutes, pattern)
		s.allRoutes = append(s.allRoutes, pattern)
	}

	if cfg.WebUI {
		// The login page, logout and the assets are pre-authentication: a
		// browser has no token, it logs in with the token to obtain a session
		// cookie. They are still behind the CIDR allowlist (the front door
		// applies it to every /ui/ path), because provision.sh --no-firewall
		// can leave port 3241 open and the allowlist is the application-layer
		// fence. The shell is gated on the session inside the handler so an
		// unauthenticated visitor is redirected to the form.
		ui := []struct {
			pattern string
			handler http.Handler
		}{
			{"GET /ui/login", http.HandlerFunc(s.handleLoginForm)},
			{"POST /ui/login", http.HandlerFunc(s.handleLoginSubmit)},
			{"POST /ui/logout", http.HandlerFunc(s.handleLogout)},
			{"GET /ui/", http.HandlerFunc(s.handleUIIndex)},
			{"GET /ui/assets/", http.StripPrefix("/ui/assets/", webui.StaticHandler())},
		}
		for _, rt := range ui {
			s.mux.Handle(rt.pattern, rt.handler)
			s.allRoutes = append(s.allRoutes, rt.pattern)
		}
	}

	addr := cfg.Listen
	if addr == "" {
		addr = "0.0.0.0:3241"
	}
	s.handler = s.frontDoor(s.mux)
	s.http = &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// SSE overrides this per response; plain requests are bounded.
		WriteTimeout: 30 * time.Second,
	}
	return s, nil
}

// Handler returns the HTTP handler, primarily for tests.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe blocks serving the API.
func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// uiCSP is the Content-Security-Policy applied to every /ui/ response. The
// embedded assets are external files (app.css, app.js) with no inline script or
// style, so no 'unsafe-inline' is needed. connect-src 'self' allows the fetch
// and EventSource calls the shell makes to /v1/. frame-ancestors 'none' plus
// X-Frame-Options stop the UI (which carries the destructive Reset and Force
// controls) being framed or clickjacked.
const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; connect-src 'self'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// frontDoor is the single place every request is classified, so a newly added
// route cannot accidentally escape the allowlist or the credential check:
//
//   - /healthz is the only public route (no allowlist, no credentials) so a
//     client can distinguish "unreachable" from "not allowed".
//   - every /ui/ path is behind the CIDR allowlist (allowOnly) and carries the
//     browser hardening headers; the pre-auth login routes must not skip the
//     allowlist, or provision.sh --no-firewall leaves them exposed.
//   - everything else goes through guard (allowlist + bearer token or session
//     cookie), so a route registered directly on the mux is still guarded.
func (s *Server) frontDoor(next http.Handler) http.Handler {
	ui := s.allowOnly(uiSecurityHeaders(next))
	guarded := s.guard(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			next.ServeHTTP(w, r)
		case r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/"):
			ui.ServeHTTP(w, r)
		default:
			guarded.ServeHTTP(w, r)
		}
	})
}

// allowOnly enforces just the CIDR allowlist, for the pre-authentication /ui/
// routes that authenticate with a session rather than a bearer token.
func (s *Server) allowOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowlisted(r) {
			writeError(w, http.StatusForbidden, "forbidden", "client address is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// uiSecurityHeaders adds the browser hardening headers to a /ui/ response.
func uiSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// guard enforces the source allowlist and authentication. A request passes
// when its peer is allowlisted and it presents either a valid bearer token or a
// valid session cookie (the latter with the CSRF header on writes). /healthz is
// the only route registered without guard.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowlisted(r) {
			writeError(w, http.StatusForbidden, "forbidden", "client address is not allowed")
			return
		}
		if s.tokenOK(r) {
			// A bearer client is unchanged: no CSRF requirement.
			next.ServeHTTP(w, r)
			return
		}
		id, ok := s.sessionID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credentials")
			return
		}
		// Slide the browser cookie's Max-Age with every session-authenticated
		// request so it matches the idle TTL the server actually enforces,
		// instead of expiring 12h after login while the session is still live.
		webui.SetCookie(w, id, webui.SessionTTL)
		if !isReadMethod(r.Method) {
			// A session authenticates a write only with the CSRF header; a
			// missing header is a forbidden request, not an unauthenticated one.
			if r.Header.Get(webui.CSRFHeader) != webui.CSRFHeaderValue {
				writeError(w, http.StatusForbidden, "forbidden", "missing CSRF header")
				return
			}
			if !sameOrigin(r) {
				writeError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Info(r.Context()))
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, proto.ListDevicesResponse{Devices: s.backend.Devices(r.Context())})
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.backend.Device(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown device")
		return
	}
	writeJSON(w, http.StatusOK, dev)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if err := s.backend.Export(r.Context(), r.PathValue("id"), force); err != nil {
		s.backendError(w, err)
		return
	}
	// The bind itself runs on the reconcile goroutine, so this is an
	// acknowledgement, not a completion. Callers confirm the device's state
	// before reporting success.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleUnexport(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Unexport(r.Context(), r.PathValue("id")); err != nil {
		s.backendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Reset(r.Context(), r.PathValue("id")); err != nil {
		s.backendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sessionPollInterval is how often an open /v1/events stream re-checks that its
// authenticating session is still alive. Watch already closes the stream when a
// session is deleted or evicted; this catches idle expiry, which nothing else
// observes while the stream is open.
const sessionPollInterval = time.Minute

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "streaming is not supported")
		return
	}
	// Clear the server write deadline: an event stream is intentionally
	// long-lived and must not be killed by WriteTimeout.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A session-authenticated browser must stop receiving events as soon as its
	// session ends. Watch closes immediately on logout or eviction; the ticker
	// catches idle expiry, which nothing else observes while the stream is open
	// (serving the stream is not session activity).
	var sessionID string
	var sessionDone <-chan struct{}
	if !s.tokenOK(r) {
		if cookie, err := r.Cookie(webui.SessionCookieName); err == nil && cookie.Value != "" {
			if done, ok := s.sessions.Watch(cookie.Value); ok {
				sessionID, sessionDone = cookie.Value, done
			}
		}
	}

	events, cancel := s.backend.Subscribe(r.Context())
	defer cancel()

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	sessionCheck := time.NewTicker(sessionPollInterval)
	defer sessionCheck.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sessionDone:
			return
		case <-sessionCheck.C:
			if sessionID != "" && !s.sessions.Alive(sessionID) {
				return
			}
		case ev, open := <-events:
			if !open {
				return
			}
			writeEvent(w, ev)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleLogs serves GET /v1/logs, the recent structured log records.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit := webui.DefaultLogsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be an integer between 1 and 500")
			return
		}
		if n > webui.MaxLogsLimit {
			n = webui.MaxLogsLimit
		}
		limit = n
	}
	if s.logs == nil {
		writeJSON(w, http.StatusOK, proto.LogsResponse{Entries: []proto.LogEntry{}, Next: after})
		return
	}
	resp := s.logs.Entries(after, limit)
	// The ring mirrors the process log, which can name sysfs paths (for
	// example a failed bind's write target). backendError deliberately redacts
	// exactly that detail, so redact it here too. The console/journald copy is
	// untouched.
	for i := range resp.Entries {
		resp.Entries[i].Msg = redactPaths(resp.Entries[i].Msg)
		for k, v := range resp.Entries[i].Attrs {
			resp.Entries[i].Attrs[k] = redactPaths(v)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// pathTokenRe matches an absolute-looking path token. It is used to redact
// filesystem detail from the /v1/logs mirror.
var pathTokenRe = regexp.MustCompile(`/[^\s,;]+`)

// redactPaths replaces path-like tokens with a fixed marker.
func redactPaths(s string) string {
	if !strings.Contains(s, "/") {
		return s
	}
	return pathTokenRe.ReplaceAllString(s, "[redacted]")
}

func (s *Server) backendError(w http.ResponseWriter, err error) {
	if s.unknownErr != nil && errors.Is(err, s.unknownErr) {
		writeError(w, http.StatusNotFound, "not_found", "unknown device")
		return
	}
	// Log the detail (which can name sysfs paths) but never return it to the
	// caller.
	if s.log != nil {
		s.log.Warn("api: request failed", "err", err)
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func writeEvent(w http.ResponseWriter, ev proto.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, proto.Error{Code: code, Message: message})
}
