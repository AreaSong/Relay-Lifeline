package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/areasong/relay-lifeline/internal/admin"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/proxy"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/webui"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	if err := validateAdminKey(cfg.Server.AdminEnabled, os.Getenv("RELAY_LIFELINE_ADMIN_KEY")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := newLogger(cfg.Logging.Level)
	store := config.NewStore(*configPath, cfg)
	registry := state.NewRegistry()
	controller := state.NewController()
	notifier := notify.New(store, logger)
	gateway := proxy.NewGateway(store, registry, controller, notifier, logger)
	adminHandler := admin.New(store, registry, controller)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("/favicon.ico", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	if cfg.Server.AdminEnabled {
		mux.Handle("/admin/api/", adminHandler)
		mux.Handle("/admin/", webui.Handler())
		mux.Handle("/admin", webui.Handler())
	}
	mux.Handle("/", gateway)

	server := &http.Server{
		Addr: cfg.Server.Listen, Handler: requestLogger(logger, mux),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go reloadOnSignal(store, logger)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), store.Get().Server.ShutdownTimeout.Duration)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("服务关闭失败", "error", err)
		}
	}()
	logger.Info("Relay Lifeline 已启动", "version", version, "listen", cfg.Server.Listen, "upstream", cfg.Upstream.BaseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("服务异常退出", "error", err)
		os.Exit(1)
	}
}

func validateAdminKey(adminEnabled bool, adminKey string) error {
	if adminEnabled && len(adminKey) < 24 {
		return errors.New("管理控制台已启用，RELAY_LIFELINE_ADMIN_KEY 至少需要 24 个字符")
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	if err := slogLevel.UnmarshalText([]byte(level)); err != nil {
		slogLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		logger.Debug("收到请求", "method", request.Method, "path", request.URL.Path)
		next.ServeHTTP(writer, request)
	})
}

func reloadOnSignal(store *config.Store, logger *slog.Logger) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	for range signals {
		if err := store.Reload(); err != nil {
			logger.Error("重新加载配置失败", "error", err)
			continue
		}
		logger.Info("配置已重新加载")
	}
}
