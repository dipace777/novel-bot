package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func retryAfter(w http.ResponseWriter, delay time.Duration) {
	seconds := max(int64(1), int64((delay+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

// Rate-limit before authentication and password hashing. All account routes
// share an IP bucket; caller-controlled credentials never become limiter keys.
func authenticationRateLimit(next http.Handler, options RouterOptions, logger *slog.Logger) http.Handler {
	count := options.AuthRequestsPerMinute
	if count <= 0 {
		count = 30
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/auth/") && r.URL.Path != "/v1/api-keys" && !strings.HasPrefix(r.URL.Path, "/v1/api-keys/") {
			next.ServeHTTP(w, r)
			return
		}
		delay, err := options.RateLimiter.Allow(r.Context(), "authentication", requestIP(r, options.TrustedProxies), count, time.Minute)
		if err != nil {
			logger.ErrorContext(r.Context(), "authentication rate limiter unavailable", "request_id", r.Context().Value(requestIDKey))
			writeError(w, r, http.StatusServiceUnavailable, "rate_limiter_unavailable", "Request admission temporarily unavailable")
			return
		}
		if delay > 0 {
			retryAfter(w, delay)
			writeError(w, r, http.StatusTooManyRequests, "authentication_rate_limit_exceeded", "Authentication request rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Only trust X-Forwarded-For when the direct peer is explicitly trusted. Walk
// from right to left, discarding trusted proxies and ignoring spoofed left hops.
func requestIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown-peer"
	}
	peer = peer.Unmap()
	isTrusted := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if isTrusted(peer) && len(r.Header.Values("X-Forwarded-For")) == 1 {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(hops) <= 32 {
			current := peer
			valid := true
			for i := len(hops) - 1; i >= 0 && isTrusted(current); i-- {
				ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil {
					valid = false
					break
				}
				current = ip.Unmap()
			}
			if valid {
				peer = current
			}
		}
	}
	// Group IPv6 privacy addresses by subnet to avoid trivial address rotation.
	if peer.Is6() {
		return netip.PrefixFrom(peer, 64).Masked().String()
	}
	return peer.String()
}
