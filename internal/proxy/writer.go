package proxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const downstreamCopyChunk = 32 * 1024

type downstreamWriter struct {
	mu           sync.Mutex
	writer       http.ResponseWriter
	streaming    bool
	stop         chan struct{}
	done         chan struct{}
	onHeartbeat  func()
	onDisconnect func(error)
	writeIdle    time.Duration
}

func startDownstream(writer http.ResponseWriter, streaming bool, heartbeat time.Duration, onHeartbeat func(), onDisconnect func(error), writeIdle ...time.Duration) *downstreamWriter {
	if streaming {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("X-Accel-Buffering", "no")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.Header().Set("X-Relay-Lifeline", "1")
	writer.WriteHeader(http.StatusOK)
	deadline := time.Duration(0)
	if len(writeIdle) > 0 {
		deadline = writeIdle[0]
	}
	downstream := &downstreamWriter{
		writer: writer, streaming: streaming, stop: make(chan struct{}), done: make(chan struct{}),
		onHeartbeat: onHeartbeat, onDisconnect: onDisconnect, writeIdle: deadline,
	}
	if err := flushResponse(writer); err != nil && downstream.onDisconnect != nil {
		downstream.onDisconnect(err)
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
			err := d.setWriteDeadline()
			if err == nil {
				if d.streaming {
					_, err = io.WriteString(d.writer, ": relay-lifeline keepalive\n\n")
				} else {
					_, err = io.WriteString(d.writer, "\n")
				}
			}
			if err == nil {
				err = flushResponse(d.writer)
			}
			clearErr := d.clearWriteDeadline()
			d.mu.Unlock()
			if err == nil {
				err = clearErr
			}
			if err != nil {
				if d.onDisconnect != nil {
					d.onDisconnect(err)
				}
				return
			}
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
	reader, err := buffer.Reader()
	if err != nil {
		return err
	}
	defer reader.Close()
	chunk := make([]byte, downstreamCopyChunk)
	for {
		count, readErr := reader.Read(chunk)
		if count > 0 {
			if err := d.setWriteDeadline(); err != nil {
				return err
			}
			written, writeErr := d.writer.Write(chunk[:count])
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := d.setWriteDeadline(); err != nil {
		return err
	}
	if err := flushResponse(d.writer); err != nil {
		return err
	}
	return d.clearWriteDeadline()
}

func (d *downstreamWriter) fail(message string) {
	d.stopHeartbeat()
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.setWriteDeadline()
	if d.streaming {
		_, _ = fmt.Fprintf(d.writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":%q}}\n\n", message)
	} else {
		_, _ = fmt.Fprintf(d.writer, "{\"error\":{\"message\":%q,\"type\":\"relay_lifeline_error\"}}", message)
	}
	_ = flushResponse(d.writer)
	_ = d.clearWriteDeadline()
}

func (d *downstreamWriter) setWriteDeadline() error {
	if d.writeIdle <= 0 {
		return nil
	}
	err := http.NewResponseController(d.writer).SetWriteDeadline(time.Now().Add(d.writeIdle))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (d *downstreamWriter) clearWriteDeadline() error {
	if d.writeIdle <= 0 {
		return nil
	}
	err := http.NewResponseController(d.writer).SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func flushResponse(writer http.ResponseWriter) error {
	err := http.NewResponseController(writer).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
