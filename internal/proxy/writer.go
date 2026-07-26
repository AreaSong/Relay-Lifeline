package proxy

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type downstreamWriter struct {
	mu          sync.Mutex
	writer      http.ResponseWriter
	streaming   bool
	stop        chan struct{}
	done        chan struct{}
	onHeartbeat func()
}

func startDownstream(writer http.ResponseWriter, streaming bool, heartbeat time.Duration, callbacks ...func()) *downstreamWriter {
	if streaming {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("X-Accel-Buffering", "no")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.Header().Set("X-Relay-Lifeline", "1")
	writer.WriteHeader(http.StatusOK)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	downstream := &downstreamWriter{writer: writer, streaming: streaming, stop: make(chan struct{}), done: make(chan struct{})}
	if len(callbacks) > 0 {
		downstream.onHeartbeat = callbacks[0]
	}
	go downstream.heartbeat(heartbeat)
	return downstream
}

func (d *downstreamWriter) heartbeat(interval time.Duration) {
	defer close(d.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.mu.Lock()
			if d.streaming {
				_, _ = io.WriteString(d.writer, ": relay-lifeline keepalive\n\n")
			} else {
				_, _ = io.WriteString(d.writer, "\n")
			}
			if flusher, ok := d.writer.(http.Flusher); ok {
				flusher.Flush()
			}
			d.mu.Unlock()
			if d.onHeartbeat != nil {
				d.onHeartbeat()
			}
		}
	}
}

func (d *downstreamWriter) stopHeartbeat() {
	select {
	case <-d.stop:
	default:
		close(d.stop)
	}
	<-d.done
}

func (d *downstreamWriter) deliver(buffer *ReplayBuffer) error {
	d.stopHeartbeat()
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := buffer.WriteTo(d.writer)
	if flusher, ok := d.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}

func (d *downstreamWriter) fail(message string) {
	d.stopHeartbeat()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.streaming {
		_, _ = fmt.Fprintf(d.writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":%q}}\n\n", message)
	} else {
		_, _ = fmt.Fprintf(d.writer, "{\"error\":{\"message\":%q,\"type\":\"relay_lifeline_error\"}}", message)
	}
	if flusher, ok := d.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
