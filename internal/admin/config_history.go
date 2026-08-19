package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/recovery"
)

type configVersion struct {
	recovery.ConfigBackup
	Diff      config.ChangePlan `json:"diff"`
	ApplyPlan config.ChangePlan `json:"applyPlan"`
}

func (h *Handler) configBackups(writer http.ResponseWriter, locale, fallback string) {
	items, err := recovery.ConfigBackups(h.store.Path(), h.store.Desired().Server.ConfigBackupDir)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "CONFIG_BACKUP_READ_FAILED", l10n.M("api.config.backup_read_failed"), locale, fallback)
		return
	}
	versions := make([]configVersion, 0, len(items))
	for _, item := range items {
		version := configVersion{ConfigBackup: item}
		if item.Valid {
			_, target, loadErr := recovery.LoadConfigBackup(h.store.Path(), h.store.Desired().Server.ConfigBackupDir, item.Name)
			if loadErr == nil {
				version.Diff = config.PlanChanges(h.store.Desired(), target)
				version.ApplyPlan = config.PlanChanges(h.store.Active(), target)
			}
		}
		versions = append(versions, version)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": versions})
}

func (h *Handler) configBackup(writer http.ResponseWriter, request *http.Request, path, locale, fallback string, role Role) {
	name := strings.TrimSuffix(strings.TrimPrefix(path, "/config/backups/"), "/rollback")
	metadata, target, err := recovery.LoadConfigBackup(h.store.Path(), h.store.Desired().Server.ConfigBackupDir, name)
	if err != nil {
		status := http.StatusInternalServerError
		code := "CONFIG_BACKUP_READ_FAILED"
		message := l10n.M("api.config.backup_read_failed")
		if errors.Is(err, recovery.ErrConfigBackupNotFound) {
			status, code, message = http.StatusNotFound, "CONFIG_BACKUP_NOT_FOUND", l10n.M("api.config.backup_not_found")
		}
		h.writeError(writer, status, code, message, locale, fallback)
		return
	}
	version := configVersion{ConfigBackup: metadata, Diff: config.PlanChanges(h.store.Desired(), target), ApplyPlan: config.PlanChanges(h.store.Active(), target)}
	if request.Method == http.MethodGet {
		writeJSON(writer, http.StatusOK, version)
		return
	}
	if request.Method != http.MethodPost || !strings.HasSuffix(path, "/rollback") {
		h.writeError(writer, http.StatusNotFound, "ENDPOINT_NOT_FOUND", l10n.M("api.route.not_found"), locale, fallback)
		return
	}
	if !h.decodeRollbackConfirmation(writer, request, metadata.SHA256, locale, fallback) {
		return
	}
	authenticationChange := planChangesAuthentication(version.ApplyPlan)
	if authenticationChange && !role.allows(RoleSensitive) {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.rollback", Outcome: "denied", Details: map[string]any{"backup": name, "authenticationChange": true}})
		h.writeError(writer, http.StatusForbidden, "SENSITIVE_PERMISSION_REQUIRED", l10n.M("api.admin.permission_denied"), locale, fallback)
		return
	}
	expectedConfirmation := "rollback-config"
	if authenticationChange {
		expectedConfirmation = "rollback-config-auth"
	}
	if request.Header.Get("X-Relay-Lifeline-Confirm") != expectedConfirmation {
		h.writeError(writer, http.StatusPreconditionRequired, "CONFIG_ROLLBACK_CONFIRMATION_REQUIRED", l10n.M("api.config.rollback_confirmation_required"), locale, fallback)
		return
	}
	result, updateErr := h.store.UpdateWithResult(target, true)
	if updateErr != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.rollback", Outcome: "failed", Details: map[string]any{"backup": name}})
		h.writeConfigError(writer, updateErr, locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "config.rollback", Outcome: "succeeded", RestartRequired: monitoring.Bool(result.PendingRestart.RestartRequired), Details: map[string]any{"backup": name, "sha256": metadata.SHA256}})
	writeJSON(writer, http.StatusOK, map[string]any{"restored": true, "source": metadata, "result": result})
}

func (h *Handler) decodeRollbackConfirmation(writer http.ResponseWriter, request *http.Request, expectedSHA, locale, fallback string) bool {
	var input struct {
		SHA256 string `json:"sha256"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.SHA256 == "" || input.SHA256 != expectedSHA {
		h.writeError(writer, http.StatusConflict, "CONFIG_BACKUP_CHANGED", l10n.M("api.config.backup_changed"), locale, fallback)
		return false
	}
	return true
}
