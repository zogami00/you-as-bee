package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
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
	if s.token == "" {
		return false
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
