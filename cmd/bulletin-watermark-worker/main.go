package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dbbudget"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := logging.Init(); err != nil {
		slog.Error("invalid logging configuration", "error", err)
		os.Exit(1)
	}
	once := flag.Bool("once", false, "process at most one queue message and exit (Container Apps Event Job execution)")
	preflight := flag.Bool("preflight", false, "verify configuration, database privileges, queue and Asset API access without consuming work, then exit")
	flag.Parse()
	if *once && *preflight {
		slog.Error("bulletin watermark worker flags --once and --preflight are mutually exclusive")
		os.Exit(2)
	}
	if *preflight {
		if !preflightMain() {
			os.Exit(1)
		}
		return
	}
	if err := run(*once); err != nil {
		slog.Error("bulletin watermark worker stopped", "error", err)
		os.Exit(1)
	}
}

// workerConfig is the configuration shared by the worker modes.
type workerConfig struct {
	cfg     config.Config
	privacy string
}

func loadWorkerConfig() (workerConfig, error) {
	cfg, err := config.LoadWorker()
	if err != nil {
		return workerConfig{}, err
	}
	privacy := strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_PRIVACY_VERSION"))
	if cfg.BulletinWatermarkQueueURL == "" || cfg.BulletinWatermarkPoisonQueueURL == "" || privacy == "" {
		return workerConfig{}, errors.New("bulletin watermark worker configuration is incomplete")
	}
	directDecode := strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED"))
	if directDecode != "" && directDecode != "false" && directDecode != "true" {
		return workerConfig{}, errors.New("invalid direct decoder configuration")
	}
	return workerConfig{cfg: cfg, privacy: privacy}, nil
}

func openDatabase(cfg config.Config) (*sql.DB, error) {
	db, err := dbbudget.Open(cfg.DatabaseURL, "hhc_web_jobs", 24, cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	if err != nil {
		return nil, err
	}
	// The Event Job sets 1/1 so every concurrent execution holds at most one
	// hhc_web_jobs connection; the persistent App keeps the 2/1 default.
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	return db, nil
}

func preflightMain() bool {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	loaded, err := loadWorkerConfig()
	var assets *assetclient.Client
	if err == nil {
		assets, err = newAssetClient(loaded.cfg, os.Getenv)
	}
	if err != nil {
		return runPreflight(ctx, preflightDeps{configErr: err})
	}
	deps := preflightDeps{assets: assets}
	if db, openErr := openDatabase(loaded.cfg); openErr == nil {
		defer db.Close()
		deps.db = sqlPreflightDatabase{db: db}
	}
	if queue, queueErr := bulletinwatermark.NewAzureQueue(loaded.cfg.BulletinWatermarkQueueURL, loaded.cfg.BulletinWatermarkPoisonQueueURL); queueErr == nil {
		deps.queue = queue
	}
	return runPreflight(ctx, deps)
}

func run(once bool) error {
	mainStarted := time.Now()
	loaded, err := loadWorkerConfig()
	if err != nil {
		return err
	}
	cfg := loaded.cfg
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := openDatabase(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	queue, err := bulletinwatermark.NewAzureQueue(cfg.BulletinWatermarkQueueURL, cfg.BulletinWatermarkPoisonQueueURL)
	if err != nil {
		return err
	}
	helper := strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_HELPER"))
	if helper == "" {
		helper = "/hhc-web-bulletin-watermark"
	}
	renderer, err := bulletinwatermark.NewRunner(helper, filepath.Join(os.TempDir(), "hhc-bulletin-watermark-worker"))
	if err != nil {
		return err
	}
	defer renderer.Close()
	// A one-shot execution never keeps the resident decoder: investigations
	// reload the model per execution (low volume, accepted in PLAN-v2 W5).
	renderer.ConfigureDirectDecoder(strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED")) == "true" && !once)
	repository := postgres.New(db, cfg.EnableFiveLocaleBulletinNotificationsAfterFluentReview)
	assets, err := newAssetClient(cfg, os.Getenv)
	if err != nil {
		return err
	}
	downloads := bulletinwatermark.NewDownloadWorker(repository, renderer, assets, loaded.privacy)
	investigations := bulletinwatermark.NewInvestigationWorker(renderer, repository, assets)
	startup := []any{"ms_init", time.Since(mainStarted).Milliseconds()}
	if age, ok := bulletinwatermark.ProcessAge(); ok {
		startup = append(startup, "ms_process_to_main", (age - time.Since(mainStarted)).Milliseconds())
	}
	startup = append(startup, "mode", map[bool]string{true: "once", false: "loop"}[once])
	slog.Info("bulletin watermark worker started", startup...)
	return serve(ctx, once, 2*time.Second, func(ctx context.Context) (bool, error) {
		return bulletinwatermark.ProcessOne(ctx, queue, downloads, investigations)
	})
}

// serve runs the worker loop. In once mode it performs exactly one dequeue
// attempt: an empty queue, a finished job, a terminal failure and a retryable
// failure (already released and backed off) all exit 0; only process-level
// errors (queue identity, configuration) return an error and exit 1, leaving
// the message to reappear after its visibility timeout.
func serve(ctx context.Context, once bool, idle time.Duration, step func(context.Context) (bool, error)) error {
	for ctx.Err() == nil {
		jobCtx := bulletinwatermark.WithPhaseTimer(ctx, bulletinwatermark.NewPhaseTimer(time.Now()))
		processed, err := step(jobCtx)
		if err != nil {
			return err
		}
		if once {
			if !processed {
				slog.Info("bulletin watermark execution found no message")
			}
			return nil
		}
		if !processed {
			select {
			case <-ctx.Done():
			case <-time.After(idle):
			}
		}
	}
	return nil
}
