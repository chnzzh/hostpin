package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chnzzh/hostpin/internal/alerting"
	"github.com/chnzzh/hostpin/internal/backup"
	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/core"
	"github.com/chnzzh/hostpin/internal/httpapi"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/notification"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/chnzzh/hostpin/internal/theme"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hostpin-server:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printHelp()
		return nil
	}
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(args)
	case "version":
		fmt.Printf("hostpin-server %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return nil
	case "migrate":
		return migrateDatabase(args)
	case "help":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func printHelp() {
	fmt.Println(`Hostpin self-hosted monitoring server

Usage:
  hostpin-server serve [--config PATH]
  hostpin-server migrate sqlite-to-postgres --source PATH --target POSTGRES_DSN
  hostpin-server version`)
}

func migrateDatabase(args []string) error {
	if len(args) == 0 || args[0] != "sqlite-to-postgres" {
		return errors.New("usage: hostpin-server migrate sqlite-to-postgres --source PATH --target POSTGRES_DSN")
	}
	flags := flag.NewFlagSet("migrate sqlite-to-postgres", flag.ContinueOnError)
	source := flags.String("source", "", "source Hostpin SQLite database path")
	target := flags.String("target", "", "empty PostgreSQL 16+ DSN")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*source) == "" || strings.TrimSpace(*target) == "" {
		return errors.New("--source and --target are required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := sqlstore.TransferSQLiteToPostgres(ctx, *source, *target)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", envOr("HOSTPIN_CONFIG", "hostpin.yaml"), "path to YAML configuration")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var receipt *backup.RestoreReceipt
	for ctx.Err() == nil {
		applied, restoreErr := backup.ApplyPendingRestore(ctx, cfg)
		if restoreErr != nil {
			logger.Error("pending backup restore failed; continuing with current data", "error", restoreErr)
		}
		if applied != nil {
			receipt = applied
			logger.Warn("backup restore applied", "created_at", applied.Manifest.CreatedAt,
				"source_version", applied.Manifest.HostpinVersion, "database_rollback", applied.DatabaseSaved)
		}
		reload, err := serveInstance(ctx, cfg, logger, receipt)
		receipt = nil
		if err != nil {
			return err
		}
		if !reload {
			return nil
		}
	}
	return nil
}

func serveInstance(parent context.Context, cfg config.Config, logger *slog.Logger, receipt *backup.RestoreReceipt) (bool, error) {
	ctx, cancelRuntime := context.WithCancel(parent)
	defer cancelRuntime()
	key, err := security.LoadOrCreateMasterKey(cfg.DataDir, cfg.Security.MasterKey)
	if err != nil {
		return false, err
	}
	secretBox, err := security.NewSecretBox(key)
	if err != nil {
		return false, err
	}
	repository, err := sqlstore.Open(ctx, cfg.Database)
	if err != nil {
		return false, err
	}
	defer repository.Close()
	if receipt != nil {
		actor := strings.TrimSpace(receipt.Actor)
		if actor == "" {
			actor = "system"
		}
		_ = repository.AppendAudit(ctx, actor, "backup.restore", "site", receipt.Manifest.CreatedAt.Format(time.RFC3339), time.Now().UTC())
	}

	hub := core.NewHub()
	if err := hydrateHub(ctx, repository, hub); err != nil {
		logger.Warn("could not hydrate latest metrics", "error", err)
	}
	persister := core.NewPersister(repository, logger, cfg.Runtime.PersistQueueSize)
	persister.Start(ctx)
	defer func() {
		cancelRuntime()
		persister.Stop()
	}()
	alertEngine := alerting.New(repository, hub, cfg.PublicURL, cfg.Runtime.OfflineAfter, logger)
	alertEngine.Start(ctx)
	notifier := notification.New(repository, secretBox, cfg.PublicURL, logger)
	notifier.Start(ctx)
	themeManager, err := theme.New(repository, cfg.DataDir)
	if err != nil {
		return false, err
	}
	restoreChannel := make(chan struct{}, 1)
	requestRestore := func() {
		select {
		case restoreChannel <- struct{}{}:
		default:
		}
	}
	backupManager := backup.NewManager(cfg.DataDir, cfg.Database.DSN, buildinfo.Version, key, strings.TrimSpace(cfg.Security.MasterKey) != "", repository)
	httpapi.SetVersion(buildinfo.Version, buildinfo.Commit)
	api := httpapi.New(cfg, repository, hub, persister, secretBox, alertEngine, notifier, themeManager, backupManager, requestRestore, logger)
	defer api.Shutdown()

	server := &http.Server{
		Addr: cfg.Listen, Handler: api.Router(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
	}
	errChannel := make(chan error, 1)
	go func() {
		logger.Info("Hostpin listening", "address", cfg.Listen, "public_url", cfg.PublicURL,
			"database", repository.Driver(), "version", buildinfo.Version)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChannel <- err
		}
	}()
	go runMaintenance(ctx, repository, logger)

	reload := false
	select {
	case err := <-errChannel:
		return false, err
	case <-parent.Done():
	case <-restoreChannel:
		reload = true
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Runtime.ShutdownTimeout)
	defer cancel()
	if reload {
		logger.Warn("reloading Hostpin to apply imported backup")
	} else {
		logger.Info("shutting down Hostpin")
	}
	api.Shutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	cancelRuntime()
	if shutdownErr != nil {
		return false, shutdownErr
	}
	return reload, nil
}

type hydrationRepository interface {
	ListNodes(context.Context, bool) ([]model.Node, error)
	LatestMetrics(context.Context, []string) (map[string]model.MetricSample, error)
}

type maintenanceRepository interface {
	EnsureMetricPartitions(context.Context, time.Time) error
	Rollup(context.Context, time.Time) error
	SiteSettings(context.Context) (model.SiteSettings, error)
	ApplyRetention(context.Context, model.SiteSettings, time.Time) error
	DeleteExpiredSessions(context.Context, time.Time) error
}

func hydrateHub(ctx context.Context, repository hydrationRepository, hub *core.Hub) error {
	nodes, err := repository.ListNodes(ctx, true)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	latest, err := repository.LatestMetrics(ctx, ids)
	if err != nil {
		return err
	}
	hub.Load(latest)
	return nil
}

func runMaintenance(ctx context.Context, repository maintenanceRepository, logger *slog.Logger) {
	rollup := time.NewTicker(5 * time.Minute)
	cleanup := time.NewTicker(time.Hour)
	defer rollup.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-rollup.C:
			jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			if err := repository.EnsureMetricPartitions(jobCtx, now.UTC()); err != nil {
				logger.Error("metric partition maintenance failed", "error", err)
			} else if err := repository.Rollup(jobCtx, now.UTC()); err != nil {
				logger.Error("rollup failed", "error", err)
			}
			cancel()
		case now := <-cleanup.C:
			jobCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			settings, err := repository.SiteSettings(jobCtx)
			if err == nil {
				err = repository.ApplyRetention(jobCtx, settings, now.UTC())
			}
			if err != nil {
				logger.Error("retention cleanup failed", "error", err)
			}
			_ = repository.DeleteExpiredSessions(jobCtx, now.UTC())
			cancel()
		}
	}
}

func newLogger(level string) *slog.Logger {
	parsed := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
