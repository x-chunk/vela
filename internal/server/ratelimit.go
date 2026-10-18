package server

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

// Failed authentication budget per client IP (spec section 7.6): a token
// bucket of authBurst tokens refilled at authPerMinute per minute. Only
// failures consume tokens, so a correctly configured client never hits the
// limit, while guessing keys is slowed to a crawl. Keys have 256-bit
// secrets, so the limit mostly protects logs and CPU, not the keys. The web
// sign-in form has its own, smaller budget (docs/SPEC-web.md).
const (
	authBurst      = 10
	authPerMinute  = 10
	loginBurst     = 5
	loginPerMinute = 5
	sweepEvery     = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

type authLimiter struct {
	mu        sync.Mutex
	now       func() time.Time
	burst     float64
	perMinute float64
	buckets   map[netip.Addr]*bucket
	lastSweep time.Time
}

func newAuthLimiter(now func() time.Time) *authLimiter {
	return newLimiter(now, authBurst, authPerMinute)
}

func newLoginLimiter(now func() time.Time) *authLimiter {
	return newLimiter(now, loginBurst, loginPerMinute)
}

func newLimiter(now func() time.Time, burst, perMinute float64) *authLimiter {
	return &authLimiter{now: now, burst: burst, perMinute: perMinute, buckets: map[netip.Addr]*bucket{}}
}

func (l *authLimiter) refill(b *bucket, now time.Time) {
	elapsed := now.Sub(b.last).Minutes()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.perMinute)
		b.last = now
	}
}

// blocked reports whether ip has exhausted its budget and, if so, how long
// until the next attempt is allowed.
func (l *authLimiter) blocked(ip netip.Addr) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[ip]
	if !ok {
		return false, 0
	}
	l.refill(b, now)
	if b.tokens >= 1 {
		return false, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perMinute * float64(time.Minute))
	return true, wait
}

// fail records a failed attempt.
func (l *authLimiter) fail(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	l.refill(b, now)
	b.tokens = math.Max(0, b.tokens-1)
}

// sweep drops buckets that have refilled completely, which bounds memory to
// the clients that failed recently.
func (l *authLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepEvery {
		return
	}
	l.lastSweep = now
	for ip, b := range l.buckets {
		l.refill(b, now)
		if b.tokens >= l.burst {
			delete(l.buckets, ip)
		}
	}
}
