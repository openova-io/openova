package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/valkey-io/valkey-go"
)

// RateLimiter enforces per-IP request limits using a sliding window counter in Valkey.
type RateLimiter struct {
	client         valkey.Client
	rpm            int // requests per minute
	trustedProxies []*net.IPNet
}

// NewRateLimiter creates a RateLimiter with the given Valkey client and requests-per-minute limit.
func NewRateLimiter(client valkey.Client, rpm int, trustedProxies []*net.IPNet) *RateLimiter {
	return &RateLimiter{client: client, rpm: rpm, trustedProxies: trustedProxies}
}

// Middleware returns HTTP middleware that enforces the rate limit.
// If Valkey is unavailable, requests are allowed through (fail open).
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.client == nil {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r, rl.trustedProxies)
		now := time.Now().UTC()
		minute := now.Format("2006-01-02T15:04")
		key := fmt.Sprintf("rl:%s:%s", ip, minute)

		ctx := context.Background()

		// INCR the counter; creates key with value 1 if it doesn't exist.
		result := rl.client.Do(ctx, rl.client.B().Incr().Key(key).Build())
		count, err := result.AsInt64()
		if err != nil {
			slog.Warn("rate limiter: valkey INCR failed, allowing request", "error", err)
			next.ServeHTTP(w, r)
			return
		}

		// Set expiry on first increment so the key auto-cleans.
		if count == 1 {
			rl.client.Do(ctx, rl.client.B().Expire().Key(key).Seconds(60).Build())
		}

		if int(count) > rl.rpm {
			retryAfter := 60 - now.Second()
			respond.JSON(w, http.StatusTooManyRequests, map[string]any{
				"error":       "rate limit exceeded",
				"retry_after": retryAfter,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// parseTrustedProxies parses a comma-separated list of CIDRs into net.IPNet slices.
// Invalid entries are logged and skipped. Empty input returns nil (no trusted proxies).
func parseTrustedProxies(csv string) []*net.IPNet {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, raw := range strings.Split(csv, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		// Allow bare IPs by appending the host-mask.
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				if ip.To4() != nil {
					entry += "/32"
				} else {
					entry += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(entry)
		if err != nil {
			slog.Warn("trusted proxy: ignoring invalid CIDR", "entry", raw, "error", err)
			continue
		}
		nets = append(nets, n)
	}
	return nets
}

// isTrustedProxy reports whether the given IP is inside any trusted-proxy CIDR.
func isTrustedProxy(ip net.IP, trusted []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteIP returns the IP portion of r.RemoteAddr, or an empty string if it
// cannot be parsed.
func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// clientIP returns the originating client IP for rate-limiting / logging.
//
// Security model: X-Forwarded-For / X-Real-IP are trivially forged by any
// caller that can reach the gateway directly. Honoring them unconditionally
// lets an attacker rotate a fresh rate-limit bucket per request by setting
// a random header value. Therefore the forwarded headers are ONLY consulted
// when r.RemoteAddr belongs to a trusted-proxy CIDR (e.g. the Traefik pod
// subnet). Otherwise we fall back to the transport-level RemoteAddr.
//
// When XFF is trusted, we walk the list right-to-left and return the first
// address that is NOT itself a trusted proxy — that is the untrusted-most hop
// and is what a legitimate reverse-proxy chain would consider the true client.
// This is the same algorithm Traefik, nginx, and Go's httputil use.
func clientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	rip := remoteIP(r)

	// If the direct peer is not a trusted proxy, return its IP verbatim and
	// IGNORE any forwarded headers the client may have set.
	if !isTrustedProxy(rip, trustedProxies) {
		if rip != nil {
			return rip.String()
		}
		return r.RemoteAddr
	}

	// Direct peer is a trusted proxy. Consider X-Real-IP first (single value,
	// simpler) then X-Forwarded-For (chain).
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		// Walk right-to-left: each entry is a hop closer to the client.
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" {
				continue
			}
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if !isTrustedProxy(ip, trustedProxies) {
				return ip.String()
			}
		}
		// All hops were trusted proxies — use the left-most entry as a best
		// effort (matches Traefik/nginx behaviour).
		first := strings.TrimSpace(parts[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}

	// Forwarded headers absent or unparseable — fall back to the peer.
	if rip != nil {
		return rip.String()
	}
	return r.RemoteAddr
}
