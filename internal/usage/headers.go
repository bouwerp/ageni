package usage

import (
	"net/http"
	"strconv"
	"time"
)

// parseRateLimitHeaders reads standard rate-limit response headers into r.
// limitHeader, remainingHeader: integer counts.
// resetHeader: either a relative duration string like "58s" or an RFC3339 timestamp.
func parseRateLimitHeaders(h http.Header, limitHeader, remainingHeader, resetHeader string, r *Report) {
	if v := h.Get(limitHeader); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			r.RateLimitRequests = ptr(n)
		}
	}
	if v := h.Get(remainingHeader); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			r.RateLimitRemaining = ptr(n)
		}
	}
	if v := h.Get(resetHeader); v != "" {
		// If it looks like an RFC3339 timestamp, convert to relative "in Xs".
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			d := time.Until(t).Round(time.Second)
			if d > 0 {
				r.RateLimitReset = "in " + d.String()
			} else {
				r.RateLimitReset = "now"
			}
		} else {
			r.RateLimitReset = v
		}
	}
}
