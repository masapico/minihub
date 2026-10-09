package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/authserver"
	"github.com/masapico/minihub/internal/authstorage"
	"github.com/masapico/minihub/internal/bootstrap"
	"github.com/masapico/minihub/internal/shutdown"
	authweb "github.com/masapico/minihub/web/auth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "miniauth error:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.Default()

	configPath := flag.String("config", authserver.DefaultConfigFilename, "configuration file")
	addr := flag.String("addr", "", "HTTP listen address")
	dataDir := flag.String("data", "", "persistent data directory")
	flag.Parse()

	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	cfg, loaded, err := authserver.LoadConfig(*configPath, explicit["config"])
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if loaded {
		if !explicit["addr"] && cfg.Server.ListenAddress != "" {
			*addr = cfg.Server.ListenAddress
		}
		if !explicit["data"] && cfg.Server.DataDir != "" {
			*dataDir = cfg.Server.DataDir
		}
	}
	if *addr == "" {
		*addr = "127.0.0.1:8090"
	}
	if *dataDir == "" {
		*dataDir = "auth_data"
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	store, err := authstorage.Open(*dataDir, cfg.Storage.Type)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	// Initial admin user
	admin := bootstrap.InitialAdmin{
		ID:       bootstrap.AdminID,
		Name:     bootstrap.AdminName,
		Password: os.Getenv("MINIAUTH_ADMIN_PASSWORD"),
	}
	if cfg.InitialAdmin != nil {
		admin = bootstrap.InitialAdmin{
			ID:       cfg.InitialAdmin.ID,
			Name:     cfg.InitialAdmin.Name,
			Password: cfg.InitialAdmin.Password,
		}
	}
	if admin.Password == "" {
		admin.Password = "admin12345"
	}
	_ = os.Unsetenv("MINIAUTH_ADMIN_PASSWORD")

	created, err := bootstrap.EnsureInitialAdminConfig(context.Background(), store, admin)
	if err != nil {
		return fmt.Errorf("ensure admin: %w", err)
	}
	if created {
		logger.Info("initial administrator created", "user", admin.ID)
	}

	ttl, _ := time.ParseDuration(cfg.Server.SessionTTL)
	sessions := auth.NewManagerWithCookieConfig(store, cfg.Server.SecureCookie, ttl, cfg.Server.BasePath+"/", "miniauth_session", http.SameSiteLaxMode)

	srv := authserver.NewServer(cfg, store, sessions, logger)
	apiRoutes := srv.Routes()

	webHandler := authweb.NewHandler(sessions, authweb.Options{
		BasePath:       cfg.Server.BasePath,
		WorkspaceTitle: cfg.UI.WorkspaceTitle,
		LoginMessage:   cfg.UI.LoginMessage,
	})

	mux := http.NewServeMux()
	mux.Handle("/.well-known/", apiRoutes)
	mux.Handle("/oauth/", apiRoutes)
	mux.Handle("/api/", apiRoutes)
	mux.Handle("/", webHandler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("miniauth listening", "address", *addr, "dataDir", *dataDir, "issuer", cfg.OIDC.Issuer)

	if runtime.GOOS == "windows" {
		requested, closeEvent, err := shutdown.Listen(*dataDir)
		if err == nil {
			defer closeEvent()
			go func() {
				select {
				case <-requested:
					stop()
				case <-ctx.Done():
				}
			}()
		}
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}

	return nil
}
