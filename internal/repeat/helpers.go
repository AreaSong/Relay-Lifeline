package repeat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

func terminal(state State) bool {
	return state == StateStopped || state == StateExpired || state == StateInterrupted
}

func waitFor(ctx context.Context, wake <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-timer.C:
		return true
	}
}

func signal(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func cloneTemplate(template Template) Template {
	template.Headers = template.Headers.Clone()
	template.Body = append([]byte(nil), template.Body...)
	return template
}

func newID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return time.Now().Format("150405.000000")
	}
	return hex.EncodeToString(buffer)
}
