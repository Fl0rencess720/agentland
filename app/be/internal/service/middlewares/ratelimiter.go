package middlewares

import (
	"net/http"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"golang.org/x/time/rate"
)

type ipRateLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type IPRateLimiter struct {
	ips             map[string]*ipRateLimitEntry
	mu              sync.Mutex
	r               rate.Limit
	b               int
	ttl             time.Duration
	cleanupInterval time.Duration
	lastCleanup     time.Time
	now             func() time.Time
}

func NewIPRateLimiter(r rate.Limit, b int) *IPRateLimiter {
	return &IPRateLimiter{
		ips:             make(map[string]*ipRateLimitEntry),
		r:               r,
		b:               b,
		ttl:             15 * time.Minute,
		cleanupInterval: time.Minute,
		now:             time.Now,
	}
}

func (i *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	now := i.now()
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.lastCleanup.IsZero() || now.Sub(i.lastCleanup) >= i.cleanupInterval {
		for key, entry := range i.ips {
			if now.Sub(entry.lastSeen) >= i.ttl {
				delete(i.ips, key)
			}
		}
		i.lastCleanup = now
	}
	entry, exists := i.ips[ip]
	if exists && now.Sub(entry.lastSeen) >= i.ttl {
		delete(i.ips, ip)
		exists = false
	}
	if !exists {
		entry = &ipRateLimitEntry{limiter: rate.NewLimiter(i.r, i.b)}
		i.ips[ip] = entry
	}
	entry.lastSeen = now
	return entry.limiter
}

func IPRateLimitMiddleware(limiter *IPRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.GetLimiter(c.ClientIP()).Allow() {
			response.ErrorResponse(c, http.StatusTooManyRequests, "rate_limited", gin.H{"type": "RATE_LIMIT_ERROR"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func NewDefaultIPRateLimiter() *IPRateLimiter {
	limiter := NewIPRateLimiter(10, 20)
	configureRateLimiterLifetime(limiter)
	return limiter
}

func NewPreviewIPRateLimiter() *IPRateLimiter {
	requestsPerSecond := viper.GetFloat64("rate_limit.preview.requests_per_second")
	if requestsPerSecond <= 0 {
		requestsPerSecond = 100
	}
	burst := viper.GetInt("rate_limit.preview.burst")
	if burst <= 0 {
		burst = 500
	}
	limiter := NewIPRateLimiter(rate.Limit(requestsPerSecond), burst)
	configureRateLimiterLifetime(limiter)
	return limiter
}

func configureRateLimiterLifetime(limiter *IPRateLimiter) {
	if ttl := viper.GetDuration("rate_limit.visitor_ttl"); ttl > 0 {
		limiter.ttl = ttl
	}
	if interval := viper.GetDuration("rate_limit.cleanup_interval"); interval > 0 {
		limiter.cleanupInterval = interval
	}
}
