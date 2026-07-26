package proxy

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("proxy.queue_full")

type Limiter struct {
	mu       sync.Mutex
	active   int
	waiting  int
	onChange func(active, waiting int)
}

func (l *Limiter) SetOnChange(callback func(active, waiting int)) {
	l.mu.Lock()
	l.onChange = callback
	l.mu.Unlock()
}

func (l *Limiter) Acquire(ctx context.Context, limits func() (int, int)) error {
	maxActive, maxWaiting := limits()
	l.mu.Lock()
	if l.active < maxActive {
		l.active++
		active, waiting, callback := l.snapshotLocked()
		l.mu.Unlock()
		notifyLimiter(callback, active, waiting)
		return nil
	}
	if l.waiting >= maxWaiting {
		l.mu.Unlock()
		return ErrQueueFull
	}
	l.waiting++
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
	queued := true
	defer func() {
		if queued {
			l.leaveQueue()
		}
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			maxActive, _ = limits()
			l.mu.Lock()
			if l.active < maxActive {
				l.active++
				l.waiting--
				queued = false
				active, waiting, callback := l.snapshotLocked()
				l.mu.Unlock()
				notifyLimiter(callback, active, waiting)
				return nil
			}
			l.mu.Unlock()
		}
	}
}

func (l *Limiter) Release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
}

func (l *Limiter) Stats() (active, waiting int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, l.waiting
}

func (l *Limiter) leaveQueue() {
	l.mu.Lock()
	if l.waiting > 0 {
		l.waiting--
	}
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
}

func (l *Limiter) snapshotLocked() (int, int, func(int, int)) {
	return l.active, l.waiting, l.onChange
}

func notifyLimiter(callback func(int, int), active, waiting int) {
	if callback != nil {
		callback(active, waiting)
	}
}

type RetryGate struct {
	mu        sync.Mutex
	nextStart time.Time
}

func (g *RetryGate) Wait(ctx context.Context, spacing time.Duration) error {
	if spacing <= 0 {
		return nil
	}
	g.mu.Lock()
	now := time.Now()
	start := now
	if g.nextStart.After(now) {
		start = g.nextStart
	}
	g.nextStart = start.Add(spacing)
	g.mu.Unlock()
	timer := time.NewTimer(time.Until(start))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
