package admin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

var errInvalidQuery = errors.New("invalid query")

type listCursor struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

type listQuery struct {
	Limit  int
	Cursor *listCursor
	From   time.Time
	To     time.Time
	State  string
	Search string
}

type historyPage struct {
	Items      []timeline.Record `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
	HasMore    bool              `json:"hasMore"`
}

type incidentPage struct {
	Items      []incident.Incident `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
	HasMore    bool                `json:"hasMore"`
}

type incidentDetail struct {
	Incident                  incident.Incident `json:"incident"`
	Requests                  []timeline.Record `json:"requests"`
	Timeline                  []incidentEvent   `json:"timeline"`
	AffectedRequestsTruncated bool              `json:"affectedRequestsTruncated"`
}

type incidentEvent struct {
	Time             time.Time `json:"time"`
	Type             string    `json:"type"`
	RequestID        string    `json:"requestId,omitempty"`
	Attempt          int       `json:"attempt,omitempty"`
	StatusCode       int       `json:"statusCode,omitempty"`
	Category         string    `json:"category,omitempty"`
	Message          string    `json:"message"`
	WaitMilliseconds int64     `json:"waitMilliseconds,omitempty"`
	AttemptPhase     string    `json:"attemptPhase,omitempty"`
}

func buildIncidentTimeline(item incident.Incident, records []timeline.Record, lifecycleMessage func(string) string) []incidentEvent {
	events := []incidentEvent{{Time: item.StartedAt, Type: "incident_opened", Message: lifecycleMessage("incident_opened")}}
	for _, record := range records {
		for _, event := range record.Events {
			if event.Time.Before(item.StartedAt) || item.ResolvedAt != nil && event.Time.After(*item.ResolvedAt) {
				continue
			}
			events = append(events, incidentEvent{
				Time: event.Time, Type: event.Type, RequestID: record.ID, Attempt: event.Attempt,
				StatusCode: event.StatusCode, Category: event.Category, Message: event.Message,
				WaitMilliseconds: event.WaitMilliseconds, AttemptPhase: event.AttemptPhase,
			})
		}
	}
	if item.RecoveryStarted != nil {
		events = append(events, incidentEvent{Time: *item.RecoveryStarted, Type: "incident_recovering", Message: lifecycleMessage("incident_recovering")})
	}
	if item.ResolvedAt != nil {
		events = append(events, incidentEvent{Time: *item.ResolvedAt, Type: "incident_resolved", Message: lifecycleMessage("incident_resolved")})
	}
	sort.SliceStable(events, func(left, right int) bool { return events[left].Time.Before(events[right].Time) })
	return events
}

func parseListQuery(values url.Values, maximum int) (listQuery, error) {
	query := listQuery{Limit: 100, State: strings.TrimSpace(values.Get("state")), Search: strings.ToLower(strings.TrimSpace(values.Get("q")))}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maximum {
			return listQuery{}, errInvalidQuery
		}
		query.Limit = limit
	}
	if len(query.Search) > 128 || len(query.State) > 32 {
		return listQuery{}, errInvalidQuery
	}
	if raw := values.Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return listQuery{}, errInvalidQuery
		}
		query.From = parsed
	}
	if raw := values.Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return listQuery{}, errInvalidQuery
		}
		query.To = parsed
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.From.After(query.To) {
		return listQuery{}, errInvalidQuery
	}
	if raw := values.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return listQuery{}, errInvalidQuery
		}
		var cursor listCursor
		if json.Unmarshal(decoded, &cursor) != nil || cursor.Time.IsZero() || cursor.ID == "" {
			return listQuery{}, errInvalidQuery
		}
		query.Cursor = &cursor
	}
	return query, nil
}

func queryHistory(records []timeline.Record, query listQuery) historyPage {
	sort.Slice(records, func(left, right int) bool {
		if records[left].CompletedAt.Equal(records[right].CompletedAt) {
			return records[left].ID > records[right].ID
		}
		return records[left].CompletedAt.After(records[right].CompletedAt)
	})
	filtered := make([]timeline.Record, 0, min(len(records), query.Limit+1))
	for _, record := range records {
		if !matchesCursor(record.CompletedAt, record.ID, query.Cursor) || !matchesRange(record.StartedAt, query) || query.State != "" && record.State != query.State {
			continue
		}
		if query.Search != "" && !containsFold(query.Search, record.ID, record.ClientID, record.TaskID, record.Method, record.Path, record.LastError) {
			continue
		}
		filtered = append(filtered, record)
		if len(filtered) > query.Limit {
			break
		}
	}
	page := historyPage{Items: filtered, HasMore: len(filtered) > query.Limit}
	if page.HasMore {
		page.Items = page.Items[:query.Limit]
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.CompletedAt, last.ID)
	}
	return page
}

func queryIncidents(items []incident.Incident, query listQuery) incidentPage {
	sort.Slice(items, func(left, right int) bool {
		if items[left].StartedAt.Equal(items[right].StartedAt) {
			return items[left].ID > items[right].ID
		}
		return items[left].StartedAt.After(items[right].StartedAt)
	})
	filtered := make([]incident.Incident, 0, min(len(items), query.Limit+1))
	for _, item := range items {
		if !matchesCursor(item.StartedAt, item.ID, query.Cursor) || !matchesRange(item.StartedAt, query) || query.State != "" && item.State != query.State {
			continue
		}
		if query.Search != "" && !incidentContains(item, query.Search) {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) > query.Limit {
			break
		}
	}
	page := incidentPage{Items: filtered, HasMore: len(filtered) > query.Limit}
	if page.HasMore {
		page.Items = page.Items[:query.Limit]
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.StartedAt, last.ID)
	}
	return page
}

func matchesCursor(itemTime time.Time, itemID string, cursor *listCursor) bool {
	if cursor == nil {
		return true
	}
	return itemTime.Before(cursor.Time) || itemTime.Equal(cursor.Time) && itemID < cursor.ID
}

func matchesRange(itemTime time.Time, query listQuery) bool {
	return (query.From.IsZero() || !itemTime.Before(query.From)) && (query.To.IsZero() || !itemTime.After(query.To))
}

func containsFold(search string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

func incidentContains(item incident.Incident, search string) bool {
	if containsFold(search, item.ID, strings.Join(item.AffectedRequests, " ")) {
		return true
	}
	for category := range item.Categories {
		if containsFold(search, category) {
			return true
		}
	}
	for status := range item.StatusCodes {
		if strings.Contains(strconv.Itoa(status), search) {
			return true
		}
	}
	return false
}

func encodeCursor(itemTime time.Time, itemID string) string {
	data, _ := json.Marshal(listCursor{Time: itemTime, ID: itemID})
	return base64.RawURLEncoding.EncodeToString(data)
}
