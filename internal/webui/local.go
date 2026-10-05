package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
)

// DefaultLocalListen is the bind address of the Windows local web UI. Port 0
// asks the OS for a free port, which the tray then uses for the Host check, the
// one-time code and the URL it opens.
const DefaultLocalListen = "127.0.0.1:0"

// LoginCookieName is the short-lived cookie that binds a one-time login code to
// the browser that first loaded it. It is distinct from the session cookie: it
// exists only between the issuing GET and the redemption POST.
const LoginCookieName = "yab_login"

// maxLocalLoginBody bounds the POST /ui/login body. The Windows flow submits a
// one-time code and nothing else, so anything larger is junk from an
// unauthenticated caller and must not be buffered.
const maxLocalLoginBody = 4 << 10

// maxLoginPeers caps the per-peer login limiter table. The local server only
// ever sees loopback peers, but the table is still bounded so a burst cannot
// grow it without limit. The algorithm mirrors internal/api/ui.go; webui cannot
// import internal/api (api imports webui) and this must not be a weaker
// limiter, so it is reproduced here rather than replaced with a global one.
const maxLoginPeers = 1024

// serversCacheTTL is how long the server/reachability view is reused before it
// is probed again. Probing a server means opening a TCP connection and an HTTP
// request, so a page load (or a client that reloads in a loop) must not trigger
// repeated probes.
const serversCacheTTL = 30 * time.Second

// Default SSE timing: compare the pin snapshot once a second and send a
// keepalive comment every 20 seconds.
const (
	defaultSSETick      = time.Second
	defaultSSEKeepAlive = 20 * time.Second
)

// localSessionPollInterval is how often an open event stream re-checks that its
// session is still alive, catching idle expiry that nothing else observes while
// the stream is open.
const localSessionPollInterval = time.Minute

// localCSP mirrors the agent's UI policy: external script and style only, no
// inline, and the UI may only talk to its own origin. frame-ancestors 'none'
// plus X-Frame-Options stop the shell (which carries attach/detach controls)
// being framed.
const localCSP = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; connect-src 'self'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// PinStatus is one auto_attach entry as the browser sees it. It carries no
// credential: the browser must never receive a Pi token.
type PinStatus struct {
	// Pin is the qualified "server/device" id.
	Pin    string `json:"pin"`
	Server string `json:"server"`
	State  string `json:"state"`
	BusID  string `json:"busid"`
	Port   int    `json:"port"`
	Paused bool   `json:"paused"`
	// PauseReason explains a user or external-detach pause.
	PauseReason string `json:"pause_reason"`
	// LastError is the most recent attach/confirm failure, if any.
	LastError string `json:"last_error"`
}

// ServerStatus is one configured server's reachability, with no credential.
type ServerStatus struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	APIPort     int    `json:"api_port"`
	Reachable   bool   `json:"reachable"`
	TokenValid  bool   `json:"token_valid"`
	DeviceCount int    `json:"device_count"`
	Err         string `json:"error,omitempty"`
}

// LocalState is the JSON body of GET /ui/api/state and of an SSE state event.
type LocalState struct {
	Pins    []PinStatus    `json:"pins"`
	Servers []ServerStatus `json:"servers"`
}

// Backend is the supervisor surface the local web UI drives. It is implemented
// in cmd/yab by an adapter over the tray controller and the client manager, so
// this package never imports internal/client or internal/agent.
type Backend interface {
	// Status returns the current pin list in config order.
	Status() []PinStatus
	// Servers returns the server/reachability view. It may probe the network,
	// so LocalServer caches the result.
	Servers() []ServerStatus
	// Attach attaches a pin (the tray's Attach).
	Attach(pin string) error
	// Detach detaches a pin and marks it user-paused (the tray's Detach). It
	// maps to Pause and does not unexport the device on the Pi.
	Detach(pin string) error
}

// LocalConfig configures a LocalServer.
type LocalConfig struct {
	// Backend supplies pin and server state. Required.
	Backend Backend
	// Logs, when non-nil, is served by GET /ui/api/logs.
	Logs *LogRing
	// Listen is the loopback bind address, normally 127.0.0.1:0. A non-loopback
	// host is rejected.
	Listen string
	// Log receives request errors. May be nil.
	Log *slog.Logger
}

// LocalServer is the Windows local web UI. It binds loopback only, requires the
// Host header to match the address it is serving on, and authenticates the
// browser with a session cookie obtained by redeeming a tray-issued one-time
// code.
type LocalServer struct {
	backend Backend
	logs    *LogRing
	log     *slog.Logger

	sessions *Sessions
	codes    *OneTimeCodes
	login    *loginLimiter

	listen string
	now    func() time.Time

	sseTick   time.Duration
	keepAlive time.Duration

	mu   sync.Mutex
	addr string

	serversMu sync.Mutex
	servers   []ServerStatus
	serversAt time.Time

	handler http.Handler
	http    *http.Server
}

// NewLocal builds a LocalServer. It does not listen; call Listen, Serve or
// ListenAndServe.
func NewLocal(cfg LocalConfig) (*LocalServer, error) {
	if cfg.Backend == nil {
		return nil, errors.New("webui: Backend is required")
	}
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = DefaultLocalListen
	}
	if err := validateLoopbackListen(listen); err != nil {
		return nil, err
	}

	s := &LocalServer{
		backend:   cfg.Backend,
		logs:      cfg.Logs,
		log:       cfg.Log,
		sessions:  NewSessions(),
		codes:     NewOneTimeCodes(),
		login:     newLoginLimiter(),
		listen:    listen,
		now:       time.Now,
		sseTick:   defaultSSETick,
		keepAlive: defaultSSEKeepAlive,
	}

	mux := http.NewServeMux()
	// Pre-authentication: the login form and the embedded assets.
	mux.HandleFunc("GET /ui/login", s.handleLoginForm)
	mux.HandleFunc("POST /ui/login", s.handleLoginSubmit)
	mux.Handle("GET /ui/assets/", http.StripPrefix("/ui/assets/", StaticHandler()))
	// Session-authenticated.
	mux.HandleFunc("POST /ui/logout", s.requireSession(s.handleLogout))
	mux.HandleFunc("GET /ui/api/state", s.requireSession(s.handleState))
	mux.HandleFunc("POST /ui/api/pins/{server}/{device}/attach", s.requireSession(s.handleAttach))
	mux.HandleFunc("POST /ui/api/pins/{server}/{device}/detach", s.requireSession(s.handleDetach))
	mux.HandleFunc("GET /ui/api/events", s.requireSession(s.handleEvents))
	mux.HandleFunc("GET /ui/api/logs", s.requireSession(s.handleLogs))
	// The shell and its SPA fallthrough. Gated inside the handler so an
	// unauthenticated visitor is redirected rather than served the app.
	mux.HandleFunc("GET /ui/", s.handleIndex)

	s.handler = s.security(mux)
	s.http = &http.Server{
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
func (s *LocalServer) Handler() http.Handler { return s.handler }

// Listen binds the loopback listener and records the address it actually bound
// (so the tray can use the real port when Listen asked for port 0).
func (s *LocalServer) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()
	return ln, nil
}

// Serve serves on ln until the server is shut down.
func (s *LocalServer) Serve(ln net.Listener) error { return s.http.Serve(ln) }

// ListenAndServe binds and serves, blocking until the server stops.
func (s *LocalServer) ListenAndServe() error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Shutdown stops the server gracefully.
func (s *LocalServer) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Addr returns the bound "host:port" address, or the empty string before
// Listen.
func (s *LocalServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// BaseURL returns the loopback base URL, e.g. "http://127.0.0.1:54321".
func (s *LocalServer) BaseURL() string { return "http://" + s.Addr() }

// NewLoginURL issues a fresh one-time code and returns the URL the tray should
// open. The code leaves the URL as soon as the browser redeems it, which
// 303-redirects to the shell.
func (s *LocalServer) NewLoginURL() string {
	return s.BaseURL() + "/ui/login?code=" + url.QueryEscape(s.codes.Issue())
}

// state assembles the payload served by GET /ui/api/state.
func (s *LocalServer) state() LocalState {
	return LocalState{Pins: s.backend.Status(), Servers: s.cachedServers()}
}

// cachedServers returns the server view, refreshing it at most once every
// serversCacheTTL so a page load cannot trigger repeated network probes.
func (s *LocalServer) cachedServers() []ServerStatus {
	s.serversMu.Lock()
	defer s.serversMu.Unlock()
	now := s.now()
	if s.serversAt.IsZero() || now.Sub(s.serversAt) >= serversCacheTTL {
		s.servers = s.backend.Servers()
		s.serversAt = now
	}
	return s.servers
}

// security is the outermost wrapper: it rejects a Host header that does not
// match the address this server is bound to (blocks DNS rebinding), refuses a
// CORS preflight outright, rejects cross-origin requests, and applies the
// browser hardening headers to every response including errors.
func (s *LocalServer) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" || !strings.EqualFold(r.Host, s.Addr()) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodOptions {
			// The local UI is same-origin only; never answer a preflight.
			http.Error(w, "forbidden: cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		if !originMatchesHost(r) {
			writeError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", localCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// requireSession authenticates a request with the session cookie and, for a
// state-changing method, requires the CSRF header and a same-origin request.
func (s *LocalServer) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.sessionID(r)
		if !ok {
			s.unauthorized(w, r)
			return
		}
		if !isReadMethod(r.Method) {
			if r.Header.Get(CSRFHeader) != CSRFHeaderValue {
				writeError(w, http.StatusForbidden, "forbidden", "missing CSRF header")
				return
			}
			if !originMatchesHost(r) {
				writeError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
				return
			}
		}
		// Slide the cookie's Max-Age with each authenticated request so it
		// tracks the idle TTL rather than a fixed window from login.
		SetCookie(w, id, SessionTTL)
		next(w, r)
	}
}

// sessionID returns the live session id the request carries, refreshing its
// idle clock, or false when there is none.
func (s *LocalServer) sessionID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	if !s.sessions.Validate(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

// unauthorized answers an unauthenticated request: JSON for the API, a
// redirect to the login page for a page request.
func (s *LocalServer) unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/ui/api/") {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid session")
		return
	}
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// handleLoginForm issues the browser binding for a one-time code. The tray
// creates the code and opens /ui/login?code=...; this GET sets the binding
// cookie and renders the confirm page. The code is not redeemed here, so a code
// that is copied out of the URL cannot be redeemed from another browser.
func (s *LocalServer) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		_ = RenderLogin(w, LoginPage{Local: true, Error: "Open this page from the tray menu: Open web UI."}, http.StatusOK)
		return
	}

	binding := ""
	if cookie, err := r.Cookie(LoginCookieName); err == nil {
		binding = cookie.Value
	}
	if binding == "" {
		binding = randomToken()
		setLoginCookie(w, binding)
	}
	if !s.codes.Bind(code, binding) {
		_ = RenderLogin(w, LoginPage{Local: true, Error: expiredLoginMessage}, http.StatusUnauthorized)
		return
	}
	_ = RenderLogin(w, LoginPage{Local: true, Code: code}, http.StatusOK)
}

// expiredLoginMessage is shown when the code is unknown, expired or already
// redeemed.
const expiredLoginMessage = "This sign-in link has expired or was already used. Open the web UI again from the tray."

// handleLoginSubmit redeems the code, but only for the browser that carries its
// binding cookie, and exchanges it for a session cookie.
func (s *LocalServer) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	// Cap the body before parsing so an unauthenticated caller cannot make the
	// server buffer a large form.
	r.Body = http.MaxBytesReader(w, r.Body, maxLocalLoginBody)
	if err := r.ParseForm(); err != nil {
		_ = RenderLogin(w, LoginPage{Local: true, Error: "Malformed sign-in submission."}, http.StatusBadRequest)
		return
	}

	peer := s.peerKey(r)
	if !s.login.allow(peer) {
		_ = RenderLogin(w, LoginPage{Local: true, Error: "Too many attempts. Wait a second and try again."}, http.StatusTooManyRequests)
		return
	}

	binding := ""
	if cookie, err := r.Cookie(LoginCookieName); err == nil {
		binding = cookie.Value
	}
	if binding == "" || !s.codes.RedeemBound(r.PostFormValue("code"), binding) {
		// A wrong or expired code is the failure the limiter records; do not
		// clear it.
		_ = RenderLogin(w, LoginPage{Local: true, Error: expiredLoginMessage}, http.StatusUnauthorized)
		return
	}
	s.login.succeed(peer)

	id := s.sessions.Create()
	SetCookie(w, id, SessionTTL)
	clearLoginCookie(w)
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// handleLogout deletes the session and clears the cookie.
func (s *LocalServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		s.sessions.Delete(cookie.Value)
	}
	ClearCookie(w)
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// handleIndex serves the application shell, redirecting a browser without a
// session to the login form.
func (s *LocalServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sessionID(r)
	if !ok {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}
	SetCookie(w, id, SessionTTL)
	_ = RenderIndex(w, Page{Mode: "client", APIBase: "/ui/api"})
}

// handleState serves the pin list and the cached server view.
func (s *LocalServer) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.state())
}

// handleAttach attaches one pin.
func (s *LocalServer) handleAttach(w http.ResponseWriter, r *http.Request) {
	s.pinAction(w, r, s.backend.Attach, "ok")
}

// handleDetach detaches one pin (maps to Pause; it does not unexport on the Pi).
func (s *LocalServer) handleDetach(w http.ResponseWriter, r *http.Request) {
	s.pinAction(w, r, s.backend.Detach, "paused")
}

func (s *LocalServer) pinAction(w http.ResponseWriter, r *http.Request, action func(string) error, status string) {
	pin := r.PathValue("server") + "/" + r.PathValue("device")
	if err := action(pin); err != nil {
		// Log the detail; never return it to the caller.
		if s.log != nil {
			s.log.Warn("webui: pin action failed", "pin", pin, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// handleEvents streams a state event whenever the pin snapshot changes. It
// polls Status on a tick and diffs, rather than hooking the manager, so a
// transition is noticed even without an event source; it emits only on change
// and sends a keepalive comment every keepAlive.
func (s *LocalServer) handleEvents(w http.ResponseWriter, r *http.Request) {
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

	// Watch the session so a logout ends the stream immediately; the ticker
	// catches idle expiry, since serving the stream is not session activity.
	var sessionDone <-chan struct{}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if done, ok := s.sessions.Watch(cookie.Value); ok {
			sessionDone = done
		}
	}

	tick := time.NewTicker(s.sseTick)
	defer tick.Stop()
	keepAlive := time.NewTicker(s.keepAlive)
	defer keepAlive.Stop()
	sessionCheck := time.NewTicker(localSessionPollInterval)
	defer sessionCheck.Stop()

	prev := s.backend.Status()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sessionDone:
			return
		case <-sessionCheck.C:
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil || !s.sessions.Alive(cookie.Value) {
				return
			}
		case <-tick.C:
			cur := s.backend.Status()
			if !reflect.DeepEqual(prev, cur) {
				prev = cur
				// Send only the changed pins: the client re-reads /ui/api/state
				// on the event, so the SSE loop never blocks on a server probe.
				writeStateEvent(w, LocalState{Pins: cur})
				flusher.Flush()
			}
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleLogs serves the recent structured log records.
func (s *LocalServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit := DefaultLogsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be an integer between 1 and 500")
			return
		}
		if n > MaxLogsLimit {
			n = MaxLogsLimit
		}
		limit = n
	}
	if s.logs == nil {
		writeJSON(w, http.StatusOK, proto.LogsResponse{Entries: []proto.LogEntry{}, Next: after})
		return
	}
	resp := s.logs.Entries(after, limit)
	// Entries returns each Attrs map by reference; encode a shallow copy so a
	// concurrent reader cannot observe a partially written map. The local
	// server is the only reader and its records name no Pi sysfs paths, so no
	// path redaction is applied here (unlike the agent's /v1/logs).
	for i := range resp.Entries {
		if resp.Entries[i].Attrs != nil {
			attrs := make(map[string]string, len(resp.Entries[i].Attrs))
			for k, v := range resp.Entries[i].Attrs {
				attrs[k] = v
			}
			resp.Entries[i].Attrs = attrs
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// peerKey names the connection peer for the per-peer login limiter.
func (s *LocalServer) peerKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// setLoginCookie writes the short-lived binding cookie. It is HttpOnly and
// SameSite=Strict so a cross-site request neither reads nor carries it.
func setLoginCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     LoginCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(OneTimeCodeTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	})
}

// clearLoginCookie expires the binding cookie in the browser.
func clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     LoginCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	})
}

// validateLoopbackListen requires an explicit loopback IP; a name, a wildcard
// or a LAN address is rejected. This is the last line of defence behind the
// config validation.
func validateLoopbackListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("webui: listen %q: want host:port: %w", listen, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("webui: listen %q: host must be a loopback IP such as 127.0.0.1", listen)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("webui: listen %q: invalid port", listen)
	}
	return nil
}

// originMatchesHost reports whether the Origin header (when present) names the
// host the request was sent to. When Origin is absent it falls back to Referer,
// which older browsers send instead. Both absent (a direct navigation or a
// non-browser client) is allowed; a mismatched value is cross-origin.
func originMatchesHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isReadMethod reports whether a method is safe/read-only. Session-authenticated
// read methods do not need the CSRF header.
func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// writeStateEvent encodes one SSE "state" event.
func writeStateEvent(w http.ResponseWriter, st LocalState) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
}

// writeJSON writes a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes the standard error object.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

// loginLimiter enforces a per-peer "one login attempt per second" rate. It
// mirrors internal/api/ui.go: a peer is reserved on entry, so a concurrent
// burst cannot all pass before the first failure registers, and a valid code
// clears the reservation. The table is bounded with oldest-entry eviction.
type loginLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	max      int
	now      func() time.Time
	last     map[string]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		interval: time.Second,
		max:      maxLoginPeers,
		now:      time.Now,
		last:     make(map[string]time.Time),
	}
}

// allow reports whether an attempt from peer may proceed, recording it in the
// same critical section.
func (l *loginLimiter) allow(peer string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.last[peer]; ok {
		if now.Sub(last) < l.interval {
			return false
		}
	} else if len(l.last) >= l.max {
		l.evictOldestLocked()
	}
	l.last[peer] = now
	return true
}

// succeed clears a peer's reservation after a valid code.
func (l *loginLimiter) succeed(peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.last, peer)
}

// evictOldestLocked drops the entry with the oldest timestamp. The caller must
// hold l.mu.
func (l *loginLimiter) evictOldestLocked() {
	oldest := ""
	var at time.Time
	for peer, t := range l.last {
		if oldest == "" || t.Before(at) {
			oldest, at = peer, t
		}
	}
	delete(l.last, oldest)
}
