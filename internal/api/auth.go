package api

import (
	"crypto/subtle"
	"net"
	"net/http"
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

// sessionOK reports whether the request carries a live session cookie. It does
// not consider the CSRF header; guard applies that separately for writes.
func (s *Server) sessionOK(r *http.Request) bool {
	if !s.webUI || s.sessions == nil {
		return false
	}
	cookie, err := r.Cookie(webui.SessionCookieName)
	if err != nil {
		return false
	}
	return s.sessions.Validate(cookie.Value)
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
