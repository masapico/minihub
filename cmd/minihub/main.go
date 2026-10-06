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
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/bootstrap"
	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/realtime"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/shutdown"
	"github.com/masapico/minihub/internal/storage/backend"
	"github.com/masapico/minihub/web"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	logger := slog.Default()
	var logFile *os.File
	defer func() {
		if runErr != nil {
			logger.Error("minihub stopped", "error", runErr)
		}
		if logFile != nil {
			if err := logFile.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close server log: %w", err))
				fmt.Fprintln(os.Stderr, runErr)
			}
		}
	}()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return migrate(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "stop" {
		return stopServer(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "retention" {
		return retention(os.Args[2:])
	}
	configPath := flag.String("config", config.DefaultFilename, "configuration file")
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP or HTTPS listen address")
	basePath := flag.String("base-path", "", "URL base path (for example /hub)")
	dataDir := flag.String("data", "data", "persistent data directory")
	secureCookie := flag.Bool("secure-cookie", false, "send the session cookie only over HTTPS")
	flag.Parse()
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	cfg, loaded, err := config.Load(*configPath, explicit["config"])
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if loaded {
		if !explicit["addr"] {
			*addr = cfg.Server.ListenAddress
		}
		if !explicit["base-path"] {
			*basePath = cfg.Server.BasePath
		}
		if !explicit["data"] {
			*dataDir = cfg.Server.DataDir
		}
		if !explicit["secure-cookie"] {
			*secureCookie = cfg.Server.SecureCookie
		}
	}
	*basePath, err = config.NormalizeBasePath(*basePath)
	if err != nil {
		return err
	}

	tlsConfig, err := loadServerTLS(cfg.Server.TLS)
	if err != nil {
		return err
	}
	*secureCookie = *secureCookie || cfg.Server.TLS.Enabled
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory for server log: %w", err)
	}
	logFile, err = os.OpenFile(filepath.Join(*dataDir, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open server log: %w", err)
	}
	logger = slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: levels[cfg.Logging.Level]}))
	store, err := backend.Open(*dataDir, cfg.Storage.Type)
	if err != nil {
		logger.Error("initialize storage", "error", err)
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	deleted, cleanupErr := store.DeleteExpiredSessions(context.Background(), time.Now())
	logger.Info("expired session cleanup completed", "deleted", deleted)
	if cleanupErr != nil {
		logger.Warn("session cleanup incomplete", "error", cleanupErr)
	}
	admin := bootstrap.InitialAdmin{ID: bootstrap.AdminID, Name: bootstrap.AdminName, Password: os.Getenv("MINIHUB_ADMIN_PASSWORD")}
	if cfg.InitialAdmin != nil {
		admin = bootstrap.InitialAdmin{ID: cfg.InitialAdmin.ID, Name: cfg.InitialAdmin.Name, Password: cfg.InitialAdmin.Password}
	}
	_ = os.Unsetenv("MINIHUB_ADMIN_PASSWORD")
	created, err := bootstrap.EnsureInitialAdminConfig(context.Background(), store, admin)
	if err != nil {
		logger.Error("initialize administrator", "error", err)
		return err
	}
	if created {
		logger.Info("initial administrator created", "user", bootstrap.AdminID)
	}
	retention := time.Duration(cfg.Notifications.MentionRetentionDays) * 24 * time.Hour
	svc := service.NewWithOptions(store, cfg.Features.SelfPasswordChange, retention)
	ttl, _ := time.ParseDuration(cfg.Server.SessionTTL)
	sessions := auth.NewManagerWithCookiePath(store, *secureCookie, ttl, *basePath+"/")
	hub := realtime.New(sessions.Authenticate, svc.CanReadChannel, svc.CanPostChannel, logger)
	hub.SetPresenceCandidates(svc.MentionCandidateIDs)
	svc.SetMessagePublisher(hub)
	mux := http.NewServeMux()
	mux.Handle("/api/realtime", hub)
	mux.Handle("/api/", httpapi.New(svc, sessions, logger))
	mux.Handle("/", web.Handler(sessions, web.Options{BasePath: *basePath, WorkspaceTitle: cfg.UI.WorkspaceTitle, LoginMessage: cfg.UI.LoginMessage, NetworkPathMode: cfg.Features.NetworkPathMode, OSNotificationsEnabled: &cfg.Notifications.OSNotificationsEnabled}))
	server := &http.Server{Addr: *addr, Handler: web.Mount(*basePath, mux), ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	server.TLSConfig = tlsConfig
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	logger.Info("minihub listening", "scheme", scheme, "address", *addr, "basePath", *basePath, "data", *dataDir, "storage", cfg.Storage.Type)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if runtime.GOOS == "windows" {
		requested, closeEvent, err := shutdown.Listen(*dataDir)
		if err != nil {
			return fmt.Errorf("start shutdown listener: %w", err)
		}
		defer closeEvent()
		go func() {
			select {
			case <-requested:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- listenAndServe(server) }()
	select {
	case err := <-serverErr:
		hub.Close()
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Shutdown closes listeners before waiting for HTTP requests. Hijacked
		// WebSockets must be closed explicitly, and their handlers drained.
		done := make(chan error, 1)
		go func() { done <- server.Shutdown(shutdownCtx) }()
		hub.Close()
		if err := <-done; err != nil {
			_ = server.Close()
			return err
		}
	}
	return nil
}

func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultFilename, "configuration file")
	data := flags.String("data", "data", "persistent data directory")
	to := flags.String("to", "", "destination storage type (sqlite)")
	dry := flags.Bool("dry-run", false, "validate using a disposable DB without switching storage")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *to != "sqlite" || flags.NArg() != 0 {
		return errors.New("usage: minihub migrate [-config file] [-data directory] -to sqlite [-dry-run]")
	}
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	cfg, loaded, err := config.Load(*configPath, explicit["config"])
	if err != nil {
		return err
	}
	if loaded && !explicit["data"] {
		*data = cfg.Server.DataDir
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := backend.Migrate(ctx, *data, *dry)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		return errors.Join(err, encodeErr)
	}
	if err != nil {
		return err
	}
	if *dry {
		slog.Info("migration validation completed; storage unchanged")
	} else {
		slog.Info("migration completed; set storage.type to sqlite and restart")
	}
	return nil
}
