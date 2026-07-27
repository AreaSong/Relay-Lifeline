package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/areasong/relay-lifeline/internal/admin"
	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/proxy"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
	"github.com/areasong/relay-lifeline/internal/webui"
)

var version = "dev"

func main() {
	preLocale, localeFromEnvironment := environmentLocale(os.Getenv("LANG"))
	localeExplicit := hasLocaleArgument(os.Args[1:])
	configPath := flag.String("config", "config.yaml", l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.config_path")))
	showVersion := flag.Bool("version", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.version")))
	localeFlag := flag.String("locale", preLocale, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.locale")))
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		message := l10n.M("cli.config_load_failed", map[string]any{"Error": l10n.Default.Error(preLocale, l10n.LocaleEnglish, err)})
		fmt.Fprintln(os.Stderr, l10n.Default.Text(preLocale, l10n.LocaleEnglish, message))
		os.Exit(1)
	}
	cliLocale := cfg.Localization.DefaultLocale
	if localeExplicit || localeFromEnvironment {
		cliLocale = l10n.Normalize(*localeFlag)
	}
	if err := validateAdminKey(cfg.Server.AdminEnabled, os.Getenv("RELAY_LIFELINE_ADMIN_KEY")); err != nil {
		fmt.Fprintln(os.Stderr, l10n.Default.Error(cliLocale, cfg.Localization.FallbackLocale, err))
		os.Exit(1)
	}
	logger := newLogger(cfg.Logging.Level)
	startedAt := time.Now()
	store := config.NewStore(*configPath, cfg)
	timelineStore := timeline.New(func() timeline.Limits {
		current := store.Get().History
		return timeline.Limits{MaxItems: current.MaxItems, Retention: current.Retention.Duration}
	})
	registry := state.NewRegistry(timelineStore)
	controller := state.NewController()
	riskManager := risk.New()
	monitoringStore := monitoring.New()
	runLogStore := runlog.New(func() runlog.Limits {
		current := store.Get().Capture
		return runlog.Limits{MaxItems: current.LogMaxItems, Retention: current.LogRetention.Duration}
	})
	captureManager := capture.New(func() config.CaptureConfig { return store.Get().Capture }, os.Getenv("RELAY_LIFELINE_CAPTURE_KEY"))
	captureManager.SetEventSink(func(event, message string, fields map[string]any) {
		runLogStore.Add(runlog.Entry{Level: "info", Event: event, Message: message, Fields: fields})
	})
	notifier := notify.New(store, logger)
	defer notifier.Close()
	gateway := proxy.NewGateway(store, registry, controller, notifier, logger, riskManager)
	gateway.SetCaptureManager(captureManager)
	gateway.SetRunLog(runLogStore)
	gateway.SetMonitoring(monitoringStore)
	diagnosticService := diagnostics.New(store, version, startedAt)
	adminHandler := admin.NewWithExtendedServices(store, registry, controller, riskManager, diagnosticService, notifier, captureManager, runLogStore)
	adminHandler.SetMonitoring(monitoringStore)

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
		Addr: cfg.Server.Listen, Handler: requestLogger(store, logger, mux),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go captureManager.StartCleaner(ctx)
	go reloadOnSignal(store, logger)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), store.Get().Server.ShutdownTimeout.Duration)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			current := store.Get()
			logger.Error(logText(current, "log.shutdown_failed"), "event", "service.shutdown_failed", "error", err)
		}
	}()
	logger.Info(logText(cfg, "log.service_started"), "event", "service.started", "version", version, "listen", cfg.Server.Listen, "upstream", cfg.Upstream.BaseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error(logText(store.Get(), "log.service_exit_failed"), "event", "service.exit_failed", "error", err)
		os.Exit(1)
	}
}

func validateAdminKey(adminEnabled bool, adminKey string) error {
	if adminEnabled && len(adminKey) < 24 {
		return l10n.E("cli.admin_key_short", nil)
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

func requestLogger(store *config.Store, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		logger.Debug(logText(store.Get(), "log.request_received"), "event", "request.received", "method", request.Method, "path", request.URL.Path)
		next.ServeHTTP(writer, request)
	})
}

func reloadOnSignal(store *config.Store, logger *slog.Logger) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	for range signals {
		if err := store.Reload(); err != nil {
			cfg := store.Get()
			logger.Error(logText(cfg, "log.config_reload_failed"), "event", "config.reload_failed", "error", l10n.Default.Error(cfg.Logging.Locale, cfg.Localization.FallbackLocale, err))
			continue
		}
		logger.Info(logText(store.Get(), "log.config_reloaded"), "event", "config.reloaded")
	}
}

func logText(cfg config.Config, messageID string) string {
	return l10n.Default.Text(cfg.Logging.Locale, cfg.Localization.FallbackLocale, l10n.M(messageID))
}

func environmentLocale(raw string) (string, bool) {
	value := strings.Split(raw, ".")[0]
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" || value == "C" || value == "POSIX" {
		return l10n.LocaleEnglish, false
	}
	return l10n.Normalize(value), true
}

func hasLocaleArgument(args []string) bool {
	for _, argument := range args {
		if argument == "-locale" || argument == "--locale" || strings.HasPrefix(argument, "-locale=") || strings.HasPrefix(argument, "--locale=") {
			return true
		}
	}
	return false
}
