package capture

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxPreviewBytes = 1 << 20

func (m *Manager) Preview(id string) (Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return Preview{}, os.ErrNotExist
	}
	key, err := unwrapKey(m.masterKey, record.WrappedKey)
	if err != nil {
		return Preview{}, err
	}
	preview := Preview{Record: publicRecord(record)}
	requestBody, requestTruncated, err := m.filteredObjectLocked(record, key, record.Request, maxPreviewBytes)
	if err != nil {
		return Preview{}, err
	}
	preview.Parts = append(preview.Parts, PreviewPart{Name: "request", Headers: record.Request.Headers.Clone(), ContentType: record.Request.ContentType, Body: string(requestBody), OriginalBytes: record.Request.OriginalBytes, Truncated: record.Request.Truncated || requestTruncated})
	for _, attempt := range record.Attempts {
		if attempt.Response == nil {
			continue
		}
		body, truncated, readErr := m.filteredObjectLocked(record, key, *attempt.Response, maxPreviewBytes)
		if readErr != nil {
			return Preview{}, readErr
		}
		preview.Parts = append(preview.Parts, PreviewPart{Name: "attempt", Attempt: attempt.Number, StatusCode: attempt.StatusCode, Headers: attempt.Response.Headers.Clone(), ContentType: attempt.Response.ContentType, Body: string(body), OriginalBytes: attempt.Response.OriginalBytes, Truncated: attempt.Response.Truncated || truncated})
	}
	if record.Final != nil {
		body, truncated, readErr := m.filteredObjectLocked(record, key, *record.Final, maxPreviewBytes)
		if readErr != nil {
			return Preview{}, readErr
		}
		preview.Parts = append(preview.Parts, PreviewPart{Name: "final", Headers: record.Final.Headers.Clone(), ContentType: record.Final.ContentType, Body: string(body), OriginalBytes: record.Final.OriginalBytes, Truncated: record.Final.Truncated || truncated})
	}
	return preview, nil
}

func (m *Manager) Export(id, mode string, timeline any, destination io.Writer) error {
	if mode != "raw" && mode != "filtered" {
		return errors.New("export mode must be raw or filtered")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return os.ErrNotExist
	}
	key, err := unwrapKey(m.masterKey, record.WrappedKey)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(destination)
	defer archive.Close()
	if err := writeJSONEntry(archive, "manifest.json", publicRecord(record)); err != nil {
		return err
	}
	if timeline != nil {
		if err := writeJSONEntry(archive, "timeline.json", timeline); err != nil {
			return err
		}
	}
	if err := writeJSONEntry(archive, "request/headers.json", record.Request.Headers); err != nil {
		return err
	}
	requestExt := bodyExtension(record.Request.ContentType)
	if err := m.writeBodyEntryLocked(archive, record, key, record.Request, "request/body"+requestExt, mode); err != nil {
		return err
	}
	for _, attempt := range record.Attempts {
		prefix := fmt.Sprintf("attempts/%03d/", attempt.Number)
		if err := writeJSONEntry(archive, prefix+"metadata.json", attempt); err != nil {
			return err
		}
		if attempt.Response == nil {
			continue
		}
		if err := writeJSONEntry(archive, prefix+"headers.json", attempt.Response.Headers); err != nil {
			return err
		}
		if err := m.writeBodyEntryLocked(archive, record, key, *attempt.Response, prefix+"body"+bodyExtension(attempt.Response.ContentType), mode); err != nil {
			return err
		}
	}
	if record.Final != nil {
		if err := writeJSONEntry(archive, "final/headers.json", record.Final.Headers); err != nil {
			return err
		}
		if err := m.writeBodyEntryLocked(archive, record, key, *record.Final, "final/body"+bodyExtension(record.Final.ContentType), mode); err != nil {
			return err
		}
	}
	return archive.Close()
}

func (m *Manager) writeBodyEntryLocked(archive *zip.Writer, record Record, key []byte, part BodyPart, name, mode string) error {
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(m.recordDir(record.ID), part.Object))
	if err != nil {
		return err
	}
	defer file.Close()
	if mode == "raw" {
		return decryptChunks(key, file, entry)
	}
	var plain bytes.Buffer
	if err := decryptChunks(key, file, &plain); err != nil {
		return err
	}
	_, err = entry.Write(FilterBody(plain.Bytes(), part.ContentType))
	return err
}

func (m *Manager) filteredObjectLocked(record Record, key []byte, part BodyPart, limit int64) ([]byte, bool, error) {
	file, err := os.Open(filepath.Join(m.recordDir(record.ID), part.Object))
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	var plain bytes.Buffer
	limited := &limitedWriter{writer: &plain, remaining: limit}
	err = decryptChunks(key, file, limited)
	if err != nil && !errors.Is(err, errPreviewLimit) {
		return nil, false, err
	}
	return FilterBody(plain.Bytes(), part.ContentType), errors.Is(err, errPreviewLimit), nil
}

var errPreviewLimit = errors.New("preview limit reached")

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errPreviewLimit
	}
	if int64(len(data)) > w.remaining {
		data = data[:w.remaining]
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	if err == nil && w.remaining == 0 {
		err = errPreviewLimit
	}
	return n, err
}

func writeJSONEntry(archive *zip.Writer, name string, value any) error {
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(entry)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func bodyExtension(contentType string) string {
	lower := strings.ToLower(contentType)
	if strings.Contains(lower, "event-stream") {
		return ".sse"
	}
	if strings.Contains(lower, "json") {
		return ".json"
	}
	return ".txt"
}
