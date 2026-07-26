package proxy

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("等待队列已满")

type Limiter struct {
	mu      sync.Mutex
	active  int
	waiting int
}

func (l *Limiter) Acquire(ctx context.Context, limits func() (int, int)) error {
	maxActive, maxWaiting := limits()
	l.mu.Lock()
	if l.active < maxActive {
		l.active++
		l.mu.Unlock()
		return nil
	}
	if l.waiting >= maxWaiting {
		l.mu.Unlock()
		return ErrQueueFull
	}
	l.waiting++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.waiting--
		l.mu.Unlock()
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
				l.mu.Unlock()
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
	l.mu.Unlock()
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
