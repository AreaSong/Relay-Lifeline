package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type faultServer struct {
	scenarios []string
	delay     time.Duration
	requests  atomic.Uint64
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18317", "listen address")
	sequence := flag.String("sequence", "503,success", "comma-separated response sequence")
	delay := flag.Duration("delay", 2*time.Minute, "timeout scenario duration")
	flag.Parse()
	scenarios := splitScenarios(*sequence)
	if len(scenarios) == 0 {
		log.Fatal("sequence must contain at least one scenario")
	}
	server := &http.Server{Addr: *listen, Handler: &faultServer{scenarios: scenarios, delay: *delay}, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("fault upstream listening on %s sequence=%s", *listen, strings.Join(scenarios, ","))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func splitScenarios(raw string) []string {
	var result []string
	for _, item := range strings.Split(raw, ",") {
		if scenario := strings.TrimSpace(strings.ToLower(item)); scenario != "" {
			result = append(result, scenario)
		}
	}
	return result
}

func (s *faultServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(request.Body, 32<<20))
	index := int(s.requests.Add(1)) - 1
	if index >= len(s.scenarios) {
		index = len(s.scenarios) - 1
	}
	scenario := s.scenarios[index]
	log.Printf("request=%d scenario=%s method=%s path=%s", index+1, scenario, request.Method, request.URL.Path)
	if status, err := strconv.Atoi(scenario); err == nil && status >= 400 && status <= 599 {
		writer.Header().Set("Content-Type", "application/json")
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			writer.Header().Set("Retry-After", "1")
		}
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"message": fmt.Sprintf("fault drill HTTP %d", status), "type": "fault_drill"}})
		return
	}
	switch scenario {
	case "timeout":
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return
		case <-timer.C:
			writeSuccess(writer)
		}
	case "truncated-json":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"fault_drill","status":"completed"`))
	case "truncated-sse":
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"partial\"}\n\n"))
	case "invalid-json":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("not-json"))
	case "success":
		writeSuccess(writer)
	default:
		http.Error(writer, "unknown fault scenario", http.StatusBadRequest)
	}
}

func writeSuccess(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": "fault_drill", "object": "response", "status": "completed", "output": []any{},
	})
}
