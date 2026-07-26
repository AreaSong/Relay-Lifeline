package admin

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/state"
)

type Handler struct {
	store      *config.Store
	registry   *state.Registry
	controller *state.Controller
	adminKey   string
}

func New(store *config.Store, registry *state.Registry, controller *state.Controller) *Handler {
	return &Handler{store: store, registry: registry, controller: controller, adminKey: os.Getenv("RELAY_LIFELINE_ADMIN_KEY")}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	if !h.authorized(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "管理密钥无效"})
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/admin/api")
	switch {
	case request.Method == http.MethodGet && path == "/session":
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
	case request.Method == http.MethodGet && path == "/status":
		writeJSON(writer, http.StatusOK, h.registry.Snapshot(h.controller.IsPaused()))
	case request.Method == http.MethodGet && path == "/config":
		writeJSON(writer, http.StatusOK, h.store.Get())
	case request.Method == http.MethodPut && path == "/config":
		h.updateConfig(writer, request)
	case request.Method == http.MethodPost && path == "/config/reload":
		if err := h.store.Reload(); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"reloaded": true})
	case request.Method == http.MethodPost && path == "/control/pause":
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": h.controller.Pause(), "paused": true})
	case request.Method == http.MethodPost && path == "/control/resume":
		writeJSON(writer, http.StatusOK, map[string]bool{"changed": h.controller.Resume(), "paused": false})
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/retry"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/retry")
		h.requestAction(writer, h.registry.RetryNow(id))
	case request.Method == http.MethodDelete && strings.HasPrefix(path, "/requests/"):
		id := strings.TrimPrefix(path, "/requests/")
		h.requestAction(writer, h.registry.Cancel(id))
	default:
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "接口不存在"})
	}
}

func (h *Handler) authorized(request *http.Request) bool {
	if h.adminKey == "" {
		return false
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if len(provided) != len(h.adminKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminKey)) == 1
}

func (h *Handler) updateConfig(writer http.ResponseWriter, request *http.Request) {
	reader := io.LimitReader(request.Body, 1<<20)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var cfg config.Config
	if err := decoder.Decode(&cfg); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "配置 JSON 无效"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "配置 JSON 包含多余内容"})
		return
	}
	before := h.store.Get()
	if err := h.store.Update(cfg, true); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	restartRequired := before.Server.Listen != cfg.Server.Listen || before.Server.AdminEnabled != cfg.Server.AdminEnabled || before.Upstream != cfg.Upstream || before.Server.ReadHeaderTimeout != cfg.Server.ReadHeaderTimeout || before.Server.ShutdownTimeout != cfg.Server.ShutdownTimeout || before.Logging.Level != cfg.Logging.Level
	writeJSON(writer, http.StatusOK, map[string]bool{"saved": true, "restartRequired": restartRequired})
}

func (h *Handler) requestAction(writer http.ResponseWriter, found bool) {
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "请求不存在"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"accepted": true})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func setSecurityHeaders(headers http.Header) {
	headers.Set("Cache-Control", "no-store")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
	headers.Set("Referrer-Policy", "no-referrer")
}
