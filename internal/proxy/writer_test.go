package proxy

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) WriteHeader(int) {}

func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream disconnected")
}

func (w *failingResponseWriter) Flush() {}

func TestHeartbeatWriteFailureSignalsDisconnect(t *testing.T) {
	disconnected := make(chan error, 1)
	writer := &failingResponseWriter{header: make(http.Header)}
	downstream := startDownstream(writer, true, time.Millisecond, nil, func(err error) {
		disconnected <- err
	})
	defer downstream.stopHeartbeat()

	select {
	case err := <-disconnected:
		if err == nil {
			t.Fatal("断联回调缺少写入错误")
		}
	case <-time.After(time.Second):
		t.Fatal("心跳写失败未触发断联回调")
	}
}
