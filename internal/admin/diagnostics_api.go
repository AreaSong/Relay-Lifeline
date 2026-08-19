package admin

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"time"

	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/recovery"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/timeline"
)

func (h *Handler) runDiagnostics(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	report := h.diagnostics.Run(request.Context(), locale, fallback)
	h.recordDiagnosticAlerts(report)
	writeJSON(writer, http.StatusOK, report)
}

func (h *Handler) exportDiagnostics(writer http.ResponseWriter, request *http.Request) {
	locale, fallback := h.requestLocales(request)
	report := h.diagnostics.Run(request.Context(), locale, fallback)
	h.recordDiagnosticAlerts(report)
	history := h.registry.History()
	if len(history) > 50 {
		history = history[:50]
	}
	logs := []runlog.Entry{}
	if h.runLogs != nil {
		logs = h.runLogs.List(0, "", "", "")
		if len(logs) > 200 {
			logs = logs[len(logs)-200:]
		}
	}
	var metrics *monitoring.Metrics
	var metricErrors *monitoring.Errors
	if h.monitor != nil {
		snapshot := h.monitor.Metrics(time.Hour)
		errors := h.monitor.ErrorsFor(time.Hour)
		metrics, metricErrors = &snapshot, &errors
	}
	incidents := []incident.Incident{}
	if h.incidents != nil {
		incidents = h.incidents.List()
	}
	recoveryReport := recovery.Verify(h.store.Path(), h.store.Get())
	backups, backupErr := recovery.ConfigBackups(h.store.Path(), h.store.Get().Server.ConfigBackupDir)
	backupResult := map[string]any{"items": backups}
	if backupErr != nil {
		backupResult["error"] = backupErr.Error()
	}
	files := map[string]any{
		"manifest.json":         map[string]any{"schemaVersion": 2, "generatedAt": time.Now(), "containsRawBodies": false},
		"report.json":           report,
		"config.redacted.json":  diagnostics.RedactedConfig(h.store.Get()),
		"status.json":           h.statusSnapshot(locale, fallback),
		"history.redacted.json": timeline.LocalizeRecords(timeline.WithoutErrorDetails(history), locale, fallback),
		"alerts.json":           risk.Localize(h.risk.Recent(50), locale, fallback),
		"runtime-logs.json":     logs,
		"metrics.json":          metrics,
		"metric-errors.json":    metricErrors,
		"incidents.json":        incidents,
		"recovery-check.json":   recoveryReport,
		"journal-summary.json":  h.journalSummary(recoveryReport),
		"config-backups.json":   backupResult,
	}
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", "attachment; filename=relay-lifeline-diagnostics.zip")
	archive := zip.NewWriter(writer)
	for _, name := range []string{"manifest.json", "report.json", "config.redacted.json", "status.json", "history.redacted.json", "alerts.json", "runtime-logs.json", "metrics.json", "metric-errors.json", "incidents.json", "recovery-check.json", "journal-summary.json", "config-backups.json"} {
		entry, err := archive.Create(name)
		if err != nil {
			_ = archive.Close()
			return
		}
		encoder := json.NewEncoder(entry)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(files[name]); err != nil {
			_ = archive.Close()
			return
		}
	}
	_ = archive.Close()
}

func (h *Handler) journalSummary(report recovery.Report) map[string]any {
	result := make(map[string]any, len(h.journals))
	checks := make(map[string]recovery.Check, len(report.Checks))
	for _, check := range report.Checks {
		checks[check.Name] = check
	}
	for name, store := range h.journals {
		if store == nil {
			continue
		}
		stats := store.Stats()
		status := store.Status()
		health := "healthy"
		if err := store.Health(); err != nil {
			health = err.Error()
		}
		verification := checks[name+"_journal"]
		result[name] = map[string]any{
			"entries": stats.Entries, "sizeBytes": stats.SizeBytes,
			"replayDurationSeconds":         stats.ReplayDuration.Seconds(),
			"lastCompactionAt":              stats.LastCompactionAt,
			"lastCompactionDurationSeconds": stats.LastCompactionDuration.Seconds(),
			"lastCompactionRemovedEntries":  stats.LastCompactionRemoved,
			"healthy":                       health == "healthy", "healthError": nullableDiagnosticError(health),
			"state": status.State, "failedAt": status.FailedAt, "failedStage": status.FailedStage,
			"failureCount":      status.FailureCount,
			"compactionHealthy": stats.CompactionHealthy,
			"hashChainValid":    verification.Status == "pass", "verificationStatus": verification.Status,
		}
	}
	return result
}

func nullableDiagnosticError(value string) any {
	if value == "healthy" {
		return nil
	}
	return value
}

func (h *Handler) recordDiagnosticAlerts(report diagnostics.Report) {
	for _, check := range report.Checks {
		if check.ID != "disk" {
			continue
		}
		if check.Status != "fail" {
			h.risk.ResolveGlobal("disk_pressure")
			return
		}
		message := l10n.M(check.MessageCode, check.MessageDetails)
		for _, alert := range h.risk.RecordGlobalMessage("disk_pressure", "warning", message) {
			if h.notifier != nil {
				h.notifier.Send(notify.Event{Type: alert.Type, MessageCode: alert.MessageCode, MessageDetails: alert.MessageDetails})
			}
		}
	}
}
