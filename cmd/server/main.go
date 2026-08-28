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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
	_ "github.com/jackc/pgx/v5/stdlib"
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
	repository := postgres.New(db, cfg.EnableFiveLocaleBulletinNotificationsAfterFluentReview)
	service := bulletins.NewService(repository, time.Now)
	contentService := content.NewService(repository, time.Now)
	siteSettingsService := sitesettings.NewService(postgres.NewSiteSettingsRepository(db), time.Now)
	assetClient := assetclient.New(cfg.AssetAPIBaseURL, cfg.InternalCallerAppID, cfg.PublicBaseURL)
	engagementClient := engagementclient.New(cfg.EngagementAPIBaseURL, cfg.InternalCallerAppID)
	var previewer httpapi.TranslationPreviewer
	if cfg.Translation.Enabled {
		generator := translation.NewAzureOpenAI(cfg.Translation.AzureEndpoint, cfg.Translation.AzureDeployment, cfg.Translation.AzureAPIKey, http.DefaultClient, cfg.Translation.ProviderTimeout, cfg.Translation.AzureRAIPolicy)
		previewer = translation.NewService(contentService, service, generator, repository, translation.ServiceConfig{
			Deployment: cfg.Translation.AzureDeployment, HandlerTimeout: cfg.Translation.HandlerTimeout, SourceCharLimit: cfg.Translation.SourceCharLimit,
			ActorLimit: cfg.Translation.ActorLimit, DeploymentLimit: cfg.Translation.DeploymentLimit,
			ActorDailyLimit: cfg.Translation.ActorDailyLimit, DeploymentDailyLimit: cfg.Translation.DeploymentDailyLimit, Cooldown: cfg.Translation.Cooldown, Now: time.Now,
		}, engagementClient)
	}
	handler := httpapi.NewWithTranslation(service, contentService, db, assetClient, cfg.AdminAllowedCaller, cfg.DaprAPIToken, cfg.AllowDevCaller, previewer, cfg.Translation.WriteDeadline, time.Now, engagementClient).WithSiteSettings(siteSettingsService)
	assets := publication.NewAssetAdapter(assetClient)
	worker := publication.NewWorker(repository, assets, cfg.OutboxMaxAttempts, engagementClient)
	go func() {
		if err := worker.Run(ctx); err != nil {
			slog.Error("publication worker stopped", "error", err)
			stop()
		}
	}()
	server := &http.Server{Addr: ":" + cfg.Port, Handler: newHTTPTraceHandler(handler.Routes()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
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
