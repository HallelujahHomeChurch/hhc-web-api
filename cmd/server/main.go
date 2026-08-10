package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/engagementclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/postgres"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("hhc web api stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTelemetry := configureTelemetry(ctx)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			slog.Warn("OpenTelemetry shutdown failed", "error", err)
		}
	}()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	repository := postgres.New(db)
	service := bulletins.NewService(repository, time.Now)
	assetClient := assetclient.New(cfg.AssetAPIBaseURL, cfg.InternalCallerAppID, cfg.PublicBaseURL)
	engagementClient := engagementclient.New(cfg.EngagementAPIBaseURL, cfg.InternalCallerAppID)
	handler := httpapi.NewWithContent(service, content.NewService(repository, time.Now), db, assetClient, cfg.AdminAllowedCaller, cfg.DaprAPIToken, cfg.AllowDevCaller, engagementClient)
	assets := publication.NewAssetAdapter(assetClient)
	worker := publication.NewWorker(repository, assets, cfg.OutboxMaxAttempts, engagementClient)
	go func() {
		if err := worker.Run(ctx); err != nil {
			slog.Error("publication worker stopped", "error", err)
			stop()
		}
	}()
	server := &http.Server{Addr: ":" + cfg.Port, Handler: otelhttp.NewHandler(handler.Routes(), "hhc-web-api"), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	serverErrors := make(chan error, 1)
	go func() { slog.Info("hhc web api listening", "port", cfg.Port); serverErrors <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	return server.Shutdown(shutdown)
}
