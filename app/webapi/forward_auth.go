package webapi

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

func newForwardAuthMiddleware(header string, emails, proxyCIDRs []string) (func(http.Handler) http.Handler, error) {
	header = http.CanonicalHeaderKey(strings.TrimSpace(header))
	if header == "" {
		header = "X-Auth-Request-Email"
	}
	allowed := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" {
			allowed[email] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("forward auth requires at least one allowed email")
	}
	networks := make([]*net.IPNet, 0, len(proxyCIDRs))
	for _, raw := range proxyCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid forward auth proxy CIDR %q: %w", raw, err)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("forward auth requires at least one trusted proxy CIDR")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip := net.ParseIP(strings.TrimSpace(host))
			trusted := false
			for _, network := range networks {
				if ip != nil && network.Contains(ip) {
					trusted = true
					break
				}
			}
			if !trusted {
				http.Error(w, "untrusted authentication proxy", http.StatusForbidden)
				return
			}
			email := strings.ToLower(strings.TrimSpace(r.Header.Get(header)))
			if _, ok := allowed[email]; !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
