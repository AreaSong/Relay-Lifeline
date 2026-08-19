package admin

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/state"
)

const (
	realtimeProtocolVersion = 1
	realtimeEventCapacity   = 512
)

type streamSnapshot struct {
	Status    state.Snapshot      `json:"status"`
	Alerts    []risk.Alert        `json:"alerts"`
	Incidents []incident.Incident `json:"incidents"`
	Metrics   *monitoring.Metrics `json:"metrics,omitempty"`
	Repeats   []repeat.Task       `json:"repeatTasks"`
}

type realtimeEvent struct {
	Version     int             `json:"version"`
	Sequence    uint64          `json:"sequence"`
	Type        string          `json:"type"`
	GeneratedAt time.Time       `json:"generatedAt"`
	Data        json.RawMessage `json:"data"`
}

type realtimePage struct {
	Events []realtimeEvent
	Gap    bool
	Latest uint64
}

type realtimeFeed struct {
	mu     sync.Mutex
	next   uint64
	events []realtimeEvent
	hashes map[string][sha256.Size]byte
}

func newRealtimeFeed() *realtimeFeed {
	return &realtimeFeed{hashes: make(map[string][sha256.Size]byte)}
}

func (f *realtimeFeed) publish(eventType string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return f.publishData(eventType, data, data)
}

func (f *realtimeFeed) publishFingerprint(eventType string, value, fingerprint any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fingerprintData, err := json.Marshal(fingerprint)
	if err != nil {
		return err
	}
	return f.publishData(eventType, data, fingerprintData)
}

func (f *realtimeFeed) publishData(eventType string, data, fingerprint []byte) error {
	hash := sha256.Sum256(fingerprint)
	f.mu.Lock()
	defer f.mu.Unlock()
	if previous, ok := f.hashes[eventType]; ok && previous == hash {
		return nil
	}
	f.hashes[eventType] = hash
	f.next++
	f.events = append(f.events, realtimeEvent{Version: realtimeProtocolVersion, Sequence: f.next, Type: eventType, GeneratedAt: time.Now().UTC(), Data: data})
	if len(f.events) > realtimeEventCapacity {
		f.events = f.events[len(f.events)-realtimeEventCapacity:]
	}
	return nil
}

func (f *realtimeFeed) after(cursor uint64) realtimePage {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := realtimePage{Latest: f.next}
	if len(f.events) == 0 {
		return page
	}
	oldest := f.events[0].Sequence
	page.Gap = cursor > 0 && cursor < oldest-1
	if page.Gap {
		return page
	}
	for _, event := range f.events {
		if event.Sequence > cursor {
			page.Events = append(page.Events, event)
		}
	}
	return page
}

func (f *realtimeFeed) latest() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next
}

func (h *Handler) realtimeFeed(locale string) *realtimeFeed {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	feed := h.streamFeeds[locale]
	if feed == nil {
		feed = newRealtimeFeed()
		h.streamFeeds[locale] = feed
	}
	return feed
}

func (h *Handler) realtimeSnapshot(locale, fallback string) streamSnapshot {
	incidents := []incident.Incident{}
	if h.incidents != nil {
		incidents = h.incidents.List()
	}
	var metrics *monitoring.Metrics
	if h.monitor != nil {
		snapshot := h.monitor.Metrics(time.Hour)
		metrics = &snapshot
	}
	repeats := []repeat.Task{}
	if h.repeater != nil {
		repeats = h.repeater.List()
	}
	return streamSnapshot{
		Status: h.statusSnapshot(locale, fallback),
		Alerts: risk.Localize(h.risk.Recent(100), locale, fallback), Incidents: incidents, Metrics: metrics, Repeats: repeats,
	}
}

func (h *Handler) publishRealtime(feed *realtimeFeed, locale, fallback string) (streamSnapshot, error) {
	snapshot := h.realtimeSnapshot(locale, fallback)
	for _, item := range []struct {
		eventType string
		value     any
	}{
		{"status", snapshot.Status}, {"alerts", snapshot.Alerts}, {"incidents", snapshot.Incidents}, {"repeat_tasks", snapshot.Repeats},
	} {
		if err := feed.publish(item.eventType, item.value); err != nil {
			return streamSnapshot{}, err
		}
	}
	if snapshot.Metrics == nil {
		if err := feed.publish("metrics", nil); err != nil {
			return streamSnapshot{}, err
		}
	} else {
		fingerprint := *snapshot.Metrics
		fingerprint.GeneratedAt = time.Time{}
		fingerprint.To = time.Time{}
		if err := feed.publishFingerprint("metrics", snapshot.Metrics, fingerprint); err != nil {
			return streamSnapshot{}, err
		}
	}
	return snapshot, nil
}

func (h *Handler) stream(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		h.writeError(writer, http.StatusInternalServerError, "STREAM_UNSUPPORTED", l10n.M("api.stream.unsupported"), locale, fallback)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	feed := h.realtimeFeed(locale)
	snapshot, err := h.publishRealtime(feed, locale, fallback)
	if err != nil {
		return
	}
	cursor, supplied, valid := realtimeCursor(request)
	if !valid {
		h.writeError(writer, http.StatusBadRequest, "INVALID_STREAM_CURSOR", l10n.M("api.stream.cursor_invalid"), locale, fallback)
		return
	}
	if !supplied {
		cursor = feed.latest()
		if !writeRealtime(writer, flusher, cursor, "sync", realtimeEvent{Version: realtimeProtocolVersion, Sequence: cursor, Type: "sync", GeneratedAt: time.Now().UTC(), Data: mustJSON(snapshot)}) {
			return
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			snapshot, err = h.publishRealtime(feed, locale, fallback)
			if err != nil {
				return
			}
			page := feed.after(cursor)
			if page.Gap {
				cursor = page.Latest
				if !writeRealtime(writer, flusher, cursor, "reset", realtimeEvent{Version: realtimeProtocolVersion, Sequence: cursor, Type: "reset", GeneratedAt: time.Now().UTC(), Data: mustJSON(snapshot)}) {
					return
				}
				continue
			}
			for _, event := range page.Events {
				if !writeRealtime(writer, flusher, event.Sequence, "update", event) {
					return
				}
				cursor = event.Sequence
			}
		case <-heartbeat.C:
			if _, err := writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func realtimeCursor(request *http.Request) (uint64, bool, bool) {
	raw := strings.TrimSpace(request.URL.Query().Get("after"))
	if raw == "" {
		raw = strings.TrimSpace(request.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, false, true
	}
	cursor, err := strconv.ParseUint(raw, 10, 64)
	return cursor, true, err == nil
}

func writeRealtime(writer http.ResponseWriter, flusher http.Flusher, sequence uint64, eventType string, event realtimeEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	if _, err := writer.Write([]byte("id: " + strconv.FormatUint(sequence, 10) + "\nevent: " + eventType + "\ndata: " + string(data) + "\n\n")); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
