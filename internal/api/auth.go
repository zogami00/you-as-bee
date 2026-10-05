package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/zogami00/you-as-bee/internal/webui"
)

// allowlisted reports whether the request's connection peer is inside the
// configured CIDR allowlist.
//
// Only r.RemoteAddr (the transport-level peer) is consulted. Forwarding headers
// such as X-Forwarded-For are deliberately ignored: a client must never be able
// to claim a source address it does not have.
func (s *Server) allowlisted(r *http.Request) bool {
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	for _, network := range s.allowed {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// tokenOK reports whether the Authorization header carries the configured
// bearer token, comparing in constant time.
func (s *Server) tokenOK(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return s.tokenEqual(strings.TrimPrefix(header, prefix))
}

// tokenEqual compares got with the configured token in constant time. It is the
// single comparison used by both bearer auth and the browser login form.
func (s *Server) tokenEqual(got string) bool {
	if s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// sessionID returns the id of the live session the request carries, if any,
// refreshing the session's idle clock. The bool is false when the request has
// no session cookie or the session is not live.
func (s *Server) sessionID(r *http.Request) (string, bool) {
	if !s.webUI || s.sessions == nil {
		return "", false
	}
	cookie, err := r.Cookie(webui.SessionCookieName)
	if err != nil {
		return "", false
	}
	if !s.sessions.Validate(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

// peerKey names the connection peer for the per-peer login limiter. The
// allowlist has already passed when this is called, so the address is the
// one the operator's client actually used.
func peerKey(r *http.Request) string {
	if ip := remoteIP(r); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

// sameOrigin is defence in depth for session-authenticated writes. When a
// browser sends an Origin header it must match the request host; a missing
// Origin (non-browser client) is allowed. It complements the CSRF header.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
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

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
