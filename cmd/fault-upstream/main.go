package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type faultServer struct {
	name       string
	scenarios  []string
	delay      time.Duration
	chunkDelay time.Duration
	readChunk  int
	events     io.Writer
	eventMu    sync.Mutex
	requests   atomic.Uint64
}

type faultEvent struct {
	Request         int    `json:"request"`
	Scenario        string `json:"scenario"`
	Name            string `json:"name,omitempty"`
	Method          string `json:"method"`
	Path            string `json:"path"`
	BodyBytes       int64  `json:"bodyBytes"`
	IdempotencyKey  string `json:"idempotencyKey,omitempty"`
	ContextCanceled bool   `json:"contextCanceled,omitempty"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18317", "listen address")
	sequence := flag.String("sequence", "503,success", "comma-separated response sequence")
	delay := flag.Duration("delay", 2*time.Minute, "timeout scenario duration")
	chunkDelay := flag.Duration("chunk-delay", 100*time.Millisecond, "delay between slow scenario chunks")
	readChunk := flag.Int("read-chunk", 1024, "request bytes read per slow-upload chunk")
	name := flag.String("name", "", "upstream name included in structured events")
	eventsPath := flag.String("events", "", "optional JSONL event output path")
	flag.Parse()
	scenarios := splitScenarios(*sequence)
	if len(scenarios) == 0 {
		log.Fatal("sequence must contain at least one scenario")
	}
	var events io.Writer
	var eventsFile *os.File
	if *eventsPath != "" {
		file, err := os.OpenFile(*eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		eventsFile, events = file, file
		defer eventsFile.Close()
	}
	server := &http.Server{Addr: *listen, Handler: &faultServer{name: *name, scenarios: scenarios, delay: *delay, chunkDelay: *chunkDelay, readChunk: *readChunk, events: events}, ReadHeaderTimeout: 5 * time.Second}
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
	index := int(s.requests.Add(1)) - 1
	if index >= len(s.scenarios) {
		index = len(s.scenarios) - 1
	}
	scenario := s.scenarios[index]
	bodyBytes := int64(0)
	defer func() {
		s.writeEvent(faultEvent{
			Request: index + 1, Scenario: scenario, Name: s.name, Method: request.Method, Path: request.URL.Path,
			BodyBytes: bodyBytes, IdempotencyKey: request.Header.Get("Idempotency-Key"), ContextCanceled: request.Context().Err() != nil,
		})
	}()
	var readErr error
	if scenario == "slow-upload" {
		bodyBytes, readErr = s.readBodySlowly(request)
	} else {
		bodyBytes, readErr = io.Copy(io.Discard, io.LimitReader(request.Body, 32<<20))
	}
	if readErr != nil {
		return
	}
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
	case "success-sse":
		writeSuccessSSE(writer)
	case "slow-sse":
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"slow\"}\n\n"))
		flush(writer)
		if !s.wait(request.Context(), s.chunkDelay) {
			return
		}
		_, _ = writer.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
		flush(writer)
	case "stalled-sse":
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"stalled\"}\n\n"))
		flush(writer)
		_ = s.wait(request.Context(), s.delay)
	case "slow-download":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"fault_drill","object":"response",`))
		flush(writer)
		if !s.wait(request.Context(), s.chunkDelay) {
			return
		}
		_, _ = writer.Write([]byte(`"status":"completed","output":[]}`))
		flush(writer)
	case "slow-upload":
		writeSuccess(writer)
	case "abrupt-close":
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			http.Error(writer, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err == nil {
			_ = connection.Close()
		}
	case "invalid-json":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("not-json"))
	case "success":
		writeSuccess(writer)
	default:
		http.Error(writer, "unknown fault scenario", http.StatusBadRequest)
	}
}

func (s *faultServer) readBodySlowly(request *http.Request) (int64, error) {
	chunkSize := s.readChunk
	if chunkSize < 1 || chunkSize > 1024*1024 {
		chunkSize = 1024
	}
	buffer := make([]byte, chunkSize)
	reader := io.LimitReader(request.Body, 32<<20)
	var total int64
	for {
		n, err := reader.Read(buffer)
		total += int64(n)
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		if !s.wait(request.Context(), s.chunkDelay) {
			return total, request.Context().Err()
		}
	}
}

func (s *faultServer) wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *faultServer) writeEvent(event faultEvent) {
	if s.events == nil {
		return
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if err := json.NewEncoder(s.events).Encode(event); err != nil {
		log.Printf("write fault event: %v", err)
	}
}

func flush(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSuccess(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": "fault_drill", "object": "response", "status": "completed", "output": []any{},
	})
}

func writeSuccessSSE(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = writer.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	flush(writer)
}
