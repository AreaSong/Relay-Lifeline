package capture

import (
	"net/http"
	"time"
)

type Status struct {
	Available         bool      `json:"available"`
	UnavailableReason string    `json:"unavailableReason,omitempty"`
	Active            bool      `json:"active"`
	RemainingRequests int       `json:"remainingRequests"`
	Deadline          time.Time `json:"deadline,omitempty"`
	StorageBytes      int64     `json:"storageBytes"`
	MaxTotalBytes     int64     `json:"maxTotalBytes"`
	CaptureCount      int       `json:"captureCount"`
}

type BodyPart struct {
	Headers       http.Header `json:"headers,omitempty"`
	ContentType   string      `json:"contentType,omitempty"`
	Object        string      `json:"object,omitempty"`
	OriginalBytes int64       `json:"originalBytes"`
	StoredBytes   int64       `json:"storedBytes"`
	SHA256        string      `json:"sha256,omitempty"`
	Truncated     bool        `json:"truncated"`
}

type Attempt struct {
	Number     int       `json:"number"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	StatusCode int       `json:"statusCode,omitempty"`
	Error      string    `json:"error,omitempty"`
	Response   *BodyPart `json:"response,omitempty"`
}

type Record struct {
	ID            string    `json:"id"`
	RequestID     string    `json:"requestId"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	State         string    `json:"state"`
	StartedAt     time.Time `json:"startedAt"`
	CompletedAt   time.Time `json:"completedAt,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt"`
	WrappedKey    string    `json:"wrappedKey,omitempty"`
	Request       BodyPart  `json:"request"`
	Attempts      []Attempt `json:"attempts"`
	Final         *BodyPart `json:"final,omitempty"`
	CapturedBytes int64     `json:"capturedBytes"`
	Warnings      []string  `json:"warnings,omitempty"`
}

type PreviewPart struct {
	Name          string      `json:"name"`
	Attempt       int         `json:"attempt,omitempty"`
	StatusCode    int         `json:"statusCode,omitempty"`
	Headers       http.Header `json:"headers,omitempty"`
	ContentType   string      `json:"contentType,omitempty"`
	Body          string      `json:"body"`
	OriginalBytes int64       `json:"originalBytes"`
	Truncated     bool        `json:"truncated"`
}

type Preview struct {
	Record Record        `json:"record"`
	Parts  []PreviewPart `json:"parts"`
}
