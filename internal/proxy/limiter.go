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
	waiters  []*limiterWaiter
	changed  chan struct{}
	onChange func(active, waiting int)
}

// 保证等待节点地址唯一：Go 可能复用零大小值的指针地址。
type limiterWaiter struct{ _ byte }

func (l *Limiter) SetOnChange(callback func(active, waiting int)) {
	l.mu.Lock()
	l.onChange = callback
	l.mu.Unlock()
}

func (l *Limiter) Acquire(ctx context.Context, limits func() (int, int)) error {
	maxActive, maxWaiting := limits()
	l.mu.Lock()
	l.ensureChangedLocked()
	if l.active < maxActive {
		l.active++
		active, waiting, callback := l.snapshotLocked()
		l.mu.Unlock()
		notifyLimiter(callback, active, waiting)
		return nil
	}
	if len(l.waiters) >= maxWaiting {
		l.mu.Unlock()
		return ErrQueueFull
	}
	waiter := &limiterWaiter{}
	l.waiters = append(l.waiters, waiter)
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
	for {
		l.mu.Lock()
		maxActive, _ = limits()
		if len(l.waiters) > 0 && l.waiters[0] == waiter && l.active < maxActive {
			l.active++
			l.waiters = l.waiters[1:]
			l.signalLocked()
			active, waiting, callback = l.snapshotLocked()
			l.mu.Unlock()
			notifyLimiter(callback, active, waiting)
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			l.removeWaiter(waiter)
			return ctx.Err()
		case <-changed:
		}
	}
}

func (l *Limiter) Release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.signalLocked()
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
}

func (l *Limiter) Stats() (active, waiting int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, len(l.waiters)
}

func (l *Limiter) removeWaiter(target *limiterWaiter) {
	l.mu.Lock()
	for index, waiter := range l.waiters {
		if waiter != target {
			continue
		}
		l.waiters = append(l.waiters[:index], l.waiters[index+1:]...)
		l.signalLocked()
		break
	}
	active, waiting, callback := l.snapshotLocked()
	l.mu.Unlock()
	notifyLimiter(callback, active, waiting)
}

func (l *Limiter) snapshotLocked() (int, int, func(int, int)) {
	return l.active, len(l.waiters), l.onChange
}

func (l *Limiter) ensureChangedLocked() {
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
}

func (l *Limiter) signalLocked() {
	l.ensureChangedLocked()
	close(l.changed)
	l.changed = make(chan struct{})
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
