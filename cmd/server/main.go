package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dbphase"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dependencyhealth"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/engagementclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/postgres"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
	"github.com/jackc/pgx/v5"
)

func main() {
	if err := logging.Init(); err != nil {
		slog.Error("invalid logging configuration", "error", err)
		os.Exit(1)
	}
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
			slog.Warn("OpenTelemetry shutdown failed", "reason", "export_unavailable")
		}
	}()
	dbConfig, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	db := dbphase.OpenReported(*dbConfig, func(ctx context.Context, phase string, elapsed time.Duration, failed bool) {
		diagnostics.RecordDatabaseOutcome(ctx, logging.Access, phase, elapsed, failed)
	})
	defer db.Close()
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	monitorPort := cfg.DaprHTTPPort
	if !cfg.DaprEnabled {
		monitorPort = 0
	}
	stopDependencyHealth := dependencyhealth.Launch(ctx, db, monitorPort, slog.Default())
	defer stopDependencyHealth()
	var contention *diagnostics.Recorder
	if cfg.DiagnosticsEnabled {
		var stopDiagnostics func()
		contention, stopDiagnostics = diagnostics.Launch(ctx, db.Stats, slog.Default(), os.Getenv("RELEASE"))
		defer stopDiagnostics()
		ctx = diagnostics.WithRecorder(ctx, contention)
	}
	auditOutbox := postgres.NewAuditOutbox(db)
	repository := postgres.New(db, cfg.EnableFiveLocaleBulletinNotificationsAfterFluentReview).WithAudit(auditOutbox).WithAutomaticBulletinExtraction(cfg.BulletinAutoExtractionEnabled).WithLLMBulletinExtraction(cfg.BulletinLLMExtractionEnabled).WithExplicitOnlinePublication(cfg.BulletinExplicitOnlinePublicationEnabled).WithSimplifiedPublicationRecovery(cfg.BulletinSimplifiedPublicationRecoveryEnabled)
	service := bulletins.NewService(repository, time.Now)
	contentService := content.NewService(repository, time.Now)
	siteSettingsService := sitesettings.NewService(postgres.NewSiteSettingsRepository(db).WithAudit(auditOutbox), time.Now)
	assetClient := assetclient.New(cfg.AssetAPIBaseURL, cfg.InternalCallerAppID, cfg.PublicBaseURL)
	if cfg.AssetPrivateHTTPS {
		assetClient, err = assetclient.NewInternal("https://asset-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io", cfg.PublicBaseURL)
		if err != nil {
			return fmt.Errorf("configure Asset private HTTPS: %w", err)
		}
	}
	if cfg.AssetWorkload.Enabled {
		if !cfg.AssetPrivateHTTPS {
			return errors.New("Asset workload authentication requires private HTTPS")
		}
		assetClient, err = assetclient.NewWorkload("https://asset-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io", "hhc-web-api", cfg.AssetWorkload.Audience, cfg.AssetWorkload.ClientID, cfg.PublicBaseURL)
		if err != nil {
			return errors.New("invalid Asset workload authentication")
		}
	}
	if err = assetClient.ConfigureVerifiedCapabilities(cfg.AssetWorkload.OwnerContent, cfg.AssetWorkload.TrustedDerivation); err != nil {
		return err
	}
	engagementClient := engagementclient.New(cfg.EngagementAPIBaseURL, cfg.InternalCallerAppID)
	engagementClient.ConfigurePrivateHTTPS(cfg.EngagementPrivateToken)
	accountClient := accountclient.New(cfg.AccountAPIBaseURL, cfg.InternalCallerAppID)
	accountClient.ConfigurePrivateHTTPS(cfg.AccountPrivateToken)
	identityProjection := bulletinwatermark.NewIdentityProjectionReconciler(repository, accountClient)
	entitlementClient := operationsclient.New(cfg.OperationsAPIBaseURL)
	entitlementClient.ConfigurePrivateHTTPS(cfg.OperationsPrivateToken)
	var previewer httpapi.TranslationPreviewer
	var summaryGenerator publication.SummaryGenerator
	if cfg.Translation.Enabled {
		generator := translation.NewAzureOpenAI(cfg.Translation.AzureEndpoint, cfg.Translation.AzureDeployment, cfg.Translation.AzureAPIKey, http.DefaultClient, cfg.Translation.ProviderTimeout, cfg.Translation.AzureRAIPolicy)
		aiService := translation.NewService(contentService, service, generator, repository, translation.ServiceConfig{
			Deployment: cfg.Translation.AzureDeployment, HandlerTimeout: cfg.Translation.HandlerTimeout, SourceCharLimit: cfg.Translation.SourceCharLimit,
			ActorLimit: cfg.Translation.ActorLimit, DeploymentLimit: cfg.Translation.DeploymentLimit,
			ActorDailyLimit: cfg.Translation.ActorDailyLimit, DeploymentDailyLimit: cfg.Translation.DeploymentDailyLimit, Cooldown: cfg.Translation.Cooldown, Now: time.Now,
		}, engagementClient)
		previewer, summaryGenerator = aiService, aiService
	}
	handler := httpapi.NewWithTranslation(service, contentService, db, assetClient, cfg.AdminAllowedCaller, cfg.DaprAPIToken, cfg.AllowDevCaller, previewer, cfg.Translation.WriteDeadline, time.Now, engagementClient).
		WithAuditOutbox(auditOutbox, time.Now).
		WithSiteSettings(siteSettingsService).
		WithGatewayLocationsToken(cfg.GatewayLocationsToken).
		WithPrivateServiceTokens(cfg.PrivateServiceTokens).
		WithGatewayPrivateTokens(cfg.GatewayPrivateToken, cfg.GatewayLivePrivateToken).
		WithoutDapr(!cfg.DaprEnabled).
		WithBulletinEntitlements(entitlementClient).
		WithLegal(legal.NewService(postgres.NewLegalRepository(db)), entitlementClient).
		WithMemberLegalEnforcement(cfg.MemberLegalEnforcementEnabled, accountClient).
		WithAssetDownloads(assetClient).
		WithBulletinWatermark(repository, accountClient, strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_PRIVACY_VERSION")))
	var recordingService *recordings.Service
	if cfg.MediaBaseURL != "" {
		key, _ := hex.DecodeString(cfg.MediaWatermarkKeyHex)
		recordingRepo := postgres.NewRecordingRepository(db).WithAudit(auditOutbox).WithVODRepublish(cfg.RecordingVODRepublishEnabled)
		recordingService = recordings.NewService(recordingRepo, cfg.MediaBaseURL, key, time.Now).WithPackages(assetClient).WithPublicationAuthorizer(accountClient).WithWatermarkTrace(cfg.RecordingWatermarkTraceEnabled).WithBroadcastWriters(cfg.BroadcastWritersEnabled).WithStudioWriters(cfg.StudioWritersEnabled).WithVideoInteractions(cfg.VideoInteractionsEnabled).WithVideoAnalytics(cfg.VideoAnalyticsEnabled)

		if cfg.StreamKeyEncryptionID != "" {
			cipher, e := recordings.NewStreamKeyCipher(cfg.StreamKeyEncryptionID, cfg.StreamKeyEncryptionKeys)
			if e != nil {
				return e
			}
			recordingService.WithStreamKeyCipher(cipher)
		}
		handler.WithRecordings(recordingService)
	}
	assets := publication.NewAssetAdapter(assetClient)
	worker := publication.NewWorker(repository, assets, cfg.OutboxMaxAttempts, engagementClient)
	if recordingService != nil {
		worker.WithRecordingPolicy(recordingService.NotificationAvailable)
	}
	worker.WithSummaries(summaryGenerator)
	if cfg.AuditDispatchEnabled {
		auditClient, auditErr := auditclient.New(cfg.DaprHTTPPort, cfg.AuditAppID, cfg.AuditToken, cfg.AuditPrivateHTTPS)
		if auditErr != nil {
			return auditErr
		}
		worker.WithAudit(auditClient, auditOutbox, slog.Default())
	}
	if cfg.BulletinWatermarkQueueURL != "" || cfg.BulletinWatermarkPoisonQueueURL != "" {
		queue, queueErr := bulletinwatermark.NewAzureQueue(cfg.BulletinWatermarkQueueURL, cfg.BulletinWatermarkPoisonQueueURL)
		if queueErr != nil {
			return queueErr
		}
		worker.WithWatermarkQueue(queue)
	}
	if cfg.BulletinExtractionQueueURL != "" || cfg.BulletinExtractionPoisonQueueURL != "" {
		queue, err := onlinebulletins.NewWakeQueue(cfg.BulletinExtractionQueueURL, cfg.BulletinExtractionPoisonQueueURL, cfg.BulletinLLMExtractionEnabled)
		if err != nil {
			return err
		}
		worker.WithExtractionQueue(queue)
	}
	workers := apiWorkers{
		publication:        worker.Run,
		identityProjection: identityProjection.Run,
	}
	if recordingService != nil {
		workers.recordingAutoPublication = func(ctx context.Context) { runRecordingAutoPublication(ctx, recordingService.ReconcileAutoPublication) }
		workers.recordingBroadcasts = func(ctx context.Context) { runRecordingBroadcasts(ctx, recordingService) }
		workers.recordingCaptures = func(ctx context.Context) { runRecordingCaptures(ctx, recordingService) }
		if cfg.RecordingLifecycleReconcileEnabled {
			workers.recordingLifecycle = func(ctx context.Context) { runRecordingLifecycle(ctx, recordingService) }
		}
	}
	// All fallible construction is complete. Cancel and join every owned worker
	// before the earlier database-close defer runs, including HTTP startup errors.
	stopWorkers := workers.start(ctx, stop)
	defer stopWorkers()

	routes := handler.RoutesWithMiddleware(func(routes http.Handler) http.Handler {
		if contention != nil {
			routes = contention.Handler(routes)
		}
		return newHTTPTraceHandler(routes)
	})
	server := &http.Server{Addr: ":" + cfg.Port, Handler: observeHTTPEntry(routes, db), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
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
