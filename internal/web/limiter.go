package web

import (
	"sync"
	"time"
)

// limiter counts failures per key inside a sliding window. It's in memory:
// a restart forgives everyone, which is fine for a household app.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

// maxKeys bounds memory if someone sprays random emails.
const maxKeys = 10000

func newLimiter(maxFails int, window time.Duration) *limiter {
	return &limiter{max: maxFails, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) prune(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	list := l.fails[key]
	i := 0
	for i < len(list) && !list[i].After(cutoff) {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = list
	return list
}

// blocked reports whether key has used up its failures.
func (l *limiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, now)) >= l.max
}

// fail records one failure.
func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.fails[key]; !ok && len(l.fails) >= maxKeys {
		for k := range l.fails { // drop expired keys; if none, drop nothing
			l.prune(k, now)
		}
		if len(l.fails) >= maxKeys {
			return
		}
	}
	l.fails[key] = append(l.prune(key, now), now)
}

// reset forgets a key after a successful sign-in.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
