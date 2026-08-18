package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/areasong/relay-lifeline/internal/admin"
	"github.com/areasong/relay-lifeline/internal/buildinfo"
	"github.com/areasong/relay-lifeline/internal/capture"
	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/diagnostics"
	"github.com/areasong/relay-lifeline/internal/incident"
	"github.com/areasong/relay-lifeline/internal/journal"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/areasong/relay-lifeline/internal/notify"
	"github.com/areasong/relay-lifeline/internal/proxy"
	"github.com/areasong/relay-lifeline/internal/recovery"
	"github.com/areasong/relay-lifeline/internal/repeat"
	"github.com/areasong/relay-lifeline/internal/risk"
	"github.com/areasong/relay-lifeline/internal/runlog"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/areasong/relay-lifeline/internal/timeline"
	"github.com/areasong/relay-lifeline/internal/webui"
)

var (
	version  = "dev"
	revision = "unknown"
	builtAt  = "unknown"
)

func main() {
	preLocale, localeFromEnvironment := environmentLocale(os.Getenv("LANG"))
	localeExplicit := hasLocaleArgument(os.Args[1:])
	configPath := flag.String("config", "config.yaml", l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.config_path")))
	showVersion := flag.Bool("version", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.version")))
	validateOnly := flag.Bool("config-validate", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.config_validate")))
	migrateConfig := flag.Bool("config-migrate", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.config_migrate")))
	recoveryCheck := flag.Bool("recovery-check", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.recovery_check")))
	doctor := flag.Bool("doctor", false, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.doctor")))
	verifyJournal := flag.String("journal-verify", "", l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.journal_verify")))
	localeFlag := flag.String("locale", preLocale, l10n.Default.Text(preLocale, l10n.LocaleEnglish, l10n.M("cli.locale")))
	flag.Parse()
	if *showVersion {
		fmt.Printf("%s revision=%s built=%s\n", version, revision, builtAt)
		return
	}
	if *verifyJournal != "" {
		entries, err := journal.Verify(*verifyJournal)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("journal valid: %d entries\n", len(entries))
		return
	}

	cfg, sourceSchemaVersion, err := config.LoadWithSourceVersion(*configPath)
	if err != nil {
		message := l10n.M("cli.config_load_failed", map[string]any{"Error": l10n.Default.Error(preLocale, l10n.LocaleEnglish, err)})
		fmt.Fprintln(os.Stderr, l10n.Default.Text(preLocale, l10n.LocaleEnglish, message))
		os.Exit(1)
	}
	cliLocale := cfg.Localization.DefaultLocale
	if localeExplicit || localeFromEnvironment {
		cliLocale = l10n.Normalize(*localeFlag)
	}
	if *validateOnly {
		fmt.Printf("configuration valid: schema-version %d\n", cfg.SchemaVersion)
		return
	}
	if *migrateConfig {
		if sourceSchemaVersion == config.CurrentSchemaVersion {
			fmt.Printf("configuration already current: schema-version %d\n", config.CurrentSchemaVersion)
			return
		}
		result, updateErr := config.NewStore(*configPath, cfg).UpdateWithResult(cfg, true)
		if updateErr != nil {
			fmt.Fprintln(os.Stderr, l10n.Default.Error(cliLocale, cfg.Localization.FallbackLocale, updateErr))
			os.Exit(1)
		}
		fmt.Printf("configuration migrated: schema-version %d -> %d backup=%s\n", sourceSchemaVersion, config.CurrentSchemaVersion, result.BackupPath)
		return
	}
	if *recoveryCheck {
		report := recovery.Verify(*configPath, cfg)
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if !report.Healthy {
			os.Exit(1)
		}
		return
	}
	if !*doctor {
		if err := validateManagementKeys(cfg.Server.AdminEnabled, os.Getenv("RELAY_LIFELINE_ADMIN_KEY"), os.Getenv("RELAY_LIFELINE_VIEWER_KEY"), os.Getenv("RELAY_LIFELINE_SENSITIVE_KEY")); err != nil {
			fmt.Fprintln(os.Stderr, l10n.Default.Error(cliLocale, cfg.Localization.FallbackLocale, err))
			os.Exit(1)
		}
	}
	logger := newLogger(cfg.Logging.Level)
	startedAt := time.Now()
	runtimeInfo := buildinfo.New(version, revision, builtAt, os.Getenv("RELAY_LIFELINE_IMAGE_REF"), startedAt)
	store := config.NewStore(*configPath, cfg)
	timelineLimits := func() timeline.Limits {
		current := store.Get().History
		return timeline.Limits{MaxItems: current.MaxItems, Retention: current.Retention.Duration}
	}
	var eventJournal *journal.Store
	var incidentJournal, repeatJournal *journal.Store
	if cfg.Persistence.Enabled && !*doctor {
		journalPath := filepath.Join(cfg.Persistence.Directory, "requests.jsonl")
		eventJournal, err = journal.Open(journalPath, cfg.Persistence.SyncWrites)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open verified event journal: %v\n", err)
			os.Exit(1)
		}
		defer eventJournal.Close()
		incidentJournal, err = journal.Open(filepath.Join(cfg.Persistence.Directory, "incidents.jsonl"), cfg.Persistence.SyncWrites)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open verified incident journal: %v\n", err)
			os.Exit(1)
		}
		defer incidentJournal.Close()
		repeatJournal, err = journal.Open(filepath.Join(cfg.Persistence.Directory, "repeat-tasks.jsonl"), cfg.Persistence.SyncWrites)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open verified repeat task journal: %v\n", err)
			os.Exit(1)
		}
		defer repeatJournal.Close()
		if _, err := eventJournal.Compact(time.Now().Add(-cfg.Persistence.Retention.Duration)); err != nil {
			fmt.Fprintf(os.Stderr, "compact request journal: %v\n", err)
			os.Exit(1)
		}
		if _, err := incidentJournal.Compact(time.Now().Add(-cfg.Incidents.Retention.Duration)); err != nil {
			fmt.Fprintf(os.Stderr, "compact incident journal: %v\n", err)
			os.Exit(1)
		}
		if _, err := repeatJournal.Compact(time.Now().Add(-cfg.Persistence.Retention.Duration)); err != nil {
			fmt.Fprintf(os.Stderr, "compact repeat task journal: %v\n", err)
			os.Exit(1)
		}
	}
	if *doctor {
		diagnosticStore := config.NewStore(*configPath, cfg)
		service := diagnostics.New(diagnosticStore, version, time.Now())
		service.SetJournal(eventJournal)
		report := service.Run(context.Background(), cliLocale, cfg.Localization.FallbackLocale)
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if !report.Healthy {
			os.Exit(1)
		}
		return
	}
	timelineStore, err := timeline.NewPersistent(timelineLimits, eventJournal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore request timeline: %v\n", err)
		os.Exit(1)
	}
	registry := state.NewRegistry(timelineStore)
	controller := state.NewController()
	riskManager := risk.New()
	monitoringStore := monitoring.New()
	if eventJournal != nil {
		monitoringStore.SetPersistenceProvider(func() []monitoring.PersistenceMetric {
			return []monitoring.PersistenceMetric{
				journalMetric("requests", eventJournal), journalMetric("incidents", incidentJournal), journalMetric("repeat-tasks", repeatJournal),
			}
		})
	}
	incidentStore, err := incident.New(func() config.IncidentConfig { return store.Get().Incidents }, incidentJournal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore incidents: %v\n", err)
		os.Exit(1)
	}
	defer incidentStore.Close()
	runLogStore := runlog.New(func() runlog.Limits {
		current := store.Get().Capture
		return runlog.Limits{MaxItems: current.LogMaxItems, Retention: current.LogRetention.Duration}
	})
	captureManager := capture.NewFromEnvironment(func() config.CaptureConfig { return store.Get().Capture })
	captureManager.SetEventSink(func(event, message string, fields map[string]any) {
		runLogStore.Add(runlog.Entry{Level: "info", Event: event, Message: message, Fields: fields})
	})
	signing := notify.SigningConfig{KeyID: os.Getenv(notify.SigningKeyIDEnvironment), Secret: os.Getenv(notify.SigningSecretEnvironment)}
	if err := notify.ValidateSigningConfig(signing, cfg.Notifications.WebhookURL != ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	notifier := notify.NewWithSigning(store, logger, signing)
	defer notifier.Close()
	gateway := proxy.NewGateway(store, registry, controller, notifier, logger, riskManager)
	gateway.SetCaptureManager(captureManager)
	gateway.SetRunLog(runLogStore)
	gateway.SetMonitoring(monitoringStore)
	gateway.SetIncidents(incidentStore)
	repeatManager, err := repeat.New(repeatJournal, gateway.ExecuteRepeat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore repeat tasks: %v\n", err)
		os.Exit(1)
	}
	defer repeatManager.Close()
	gateway.SetRepeatManager(repeatManager)
	diagnosticService := diagnostics.New(store, version, startedAt)
	diagnosticService.SetJournal(eventJournal)
	adminHandler := admin.NewWithExtendedServices(store, registry, controller, riskManager, diagnosticService, notifier, captureManager, runLogStore)
	adminHandler.SetMonitoring(monitoringStore)
	adminHandler.SetIncidents(incidentStore)
	adminHandler.SetRepeatManager(repeatManager)
	adminHandler.SetJournals(eventJournal, incidentJournal, repeatJournal)
	adminHandler.SetRuntimeInfo(func() buildinfo.Info { return runtimeInfo.Snapshot(config.CurrentSchemaVersion) })

	mux := http.NewServeMux()
	var ready atomic.Bool
	ready.Store(true)
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/readyz", readinessHandler(&ready, func() error {
		if eventJournal == nil {
			return nil
		}
		if err := eventJournal.Health(); err != nil {
			return err
		}
		if err := incidentJournal.Health(); err != nil {
			return err
		}
		return repeatJournal.Health()
	}))
	mux.HandleFunc("/favicon.ico", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	if cfg.Server.AdminEnabled {
		mux.Handle("/admin/api/", adminHandler)
		mux.Handle("/admin/", webui.Handler())
		mux.Handle("/admin", webui.Handler())
	}
	if cfg.MetricsExport.Enabled {
		mux.Handle(cfg.MetricsExport.Path, monitoringStore.PrometheusHandler())
	}
	mux.Handle("/", gateway)

	server := &http.Server{
		Addr: cfg.Server.Listen, Handler: requestLogger(store, logger, mux),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go captureManager.StartCleaner(ctx)
	if eventJournal != nil {
		go maintainJournals(ctx, store, logger, eventJournal, incidentJournal, repeatJournal)
	}
	go reloadOnSignal(store, logger)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		ready.Store(false)
		repeatManager.Close()
		registry.RetryWaiting()
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
	if ctx.Err() != nil {
		<-shutdownDone
	}
}

func maintainJournals(ctx context.Context, store *config.Store, logger *slog.Logger, requestJournal, incidentJournal, repeatJournal *journal.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cfg := store.Get()
			if _, err := requestJournal.Compact(now.Add(-cfg.Persistence.Retention.Duration)); err != nil {
				logger.Error("compact request journal", "event", "journal.compaction_failed", "journal", "requests", "error", err)
			}
			if _, err := incidentJournal.Compact(now.Add(-cfg.Incidents.Retention.Duration)); err != nil {
				logger.Error("compact incident journal", "event", "journal.compaction_failed", "journal", "incidents", "error", err)
			}
			if _, err := repeatJournal.Compact(now.Add(-cfg.Persistence.Retention.Duration)); err != nil {
				logger.Error("compact repeat task journal", "event", "journal.compaction_failed", "journal", "repeat-tasks", "error", err)
			}
		}
	}
}

func readinessHandler(ready *atomic.Bool, checks ...func() error) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if !ready.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":"draining"}`))
			return
		}
		for _, check := range checks {
			if check != nil && check() != nil {
				writer.WriteHeader(http.StatusServiceUnavailable)
				_, _ = writer.Write([]byte(`{"status":"unavailable"}`))
				return
			}
		}
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
}

func journalMetric(name string, store *journal.Store) monitoring.PersistenceMetric {
	stats := store.Stats()
	lastCompaction := float64(0)
	if !stats.LastCompactionAt.IsZero() {
		lastCompaction = float64(stats.LastCompactionAt.Unix())
	}
	return monitoring.PersistenceMetric{
		Journal: name, Entries: stats.Entries, SizeBytes: stats.SizeBytes,
		ReplayDurationSeconds: stats.ReplayDuration.Seconds(), LastCompactionTimestamp: lastCompaction,
		LastCompactionSeconds: stats.LastCompactionDuration.Seconds(), LastCompactionRemoved: stats.LastCompactionRemoved,
		Healthy: store.Health() == nil, CompactionHealthy: stats.CompactionHealthy,
	}
}

func validateManagementKeys(adminEnabled bool, operatorKey, viewerKey, sensitiveKey string) error {
	if !adminEnabled {
		return nil
	}
	if len(operatorKey) < 24 {
		return l10n.E("cli.admin_key_short", nil)
	}
	if viewerKey != "" && len(viewerKey) < 24 {
		return l10n.E("cli.viewer_key_short", nil)
	}
	if len(sensitiveKey) < 24 {
		return l10n.E("cli.sensitive_key_short", nil)
	}
	if operatorKey == sensitiveKey || viewerKey != "" && (viewerKey == operatorKey || viewerKey == sensitiveKey) {
		return l10n.E("cli.management_keys_distinct", nil)
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
