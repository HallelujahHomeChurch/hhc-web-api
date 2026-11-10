package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func runBatch(ctx context.Context) error {
	cfg, err := config.LoadExtractor()
	if err != nil {
		return errInvalidJob
	}
	if !cfg.Enabled {
		return nil
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(rendererArtifactSHA256) {
		return errInvalidJob
	}
	assetClient, err := assetclient.NewInternal(cfg.AssetAPIBaseURL)
	if err != nil {
		return errInvalidJob
	}
	if cfg.AssetWorkload.Enabled {
		assetClient, err = assetclient.NewWorkload(cfg.AssetAPIBaseURL, "hhc-web-extractor", cfg.AssetWorkload.Audience, cfg.AssetWorkload.ClientID, "")
		if err != nil {
			return errInvalidJob
		}
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	assets, err := onlinebulletins.LoadBundledTemplateAssets(templateManifestFile, templateManifestSHA256)
	if err != nil {
		return err
	}
	v2Assets, err := onlinebulletins.LoadBundledTemplateAssets(templateV2ManifestFile, templateV2ManifestSHA256)
	if err != nil {
		return err
	}
	// OpenCC preserves rare traditional glyphs that need the same serif fallback.
	for _, asset := range assets {
		if asset.Kind == "font" && (asset.FontRole == "body" || asset.FontRole == "emphasis") {
			v2Assets = append(v2Assets, asset)
		}
	}
	compose := func(ctx context.Context, document onlinebulletins.Document, metadata onlinebulletins.CanonicalMetadata) (onlinebulletins.LayoutResult, error) {
		renderer, err := layoutRunner(document.LayoutManifest.RendererVersion)
		if err != nil {
			return onlinebulletins.LayoutResult{}, err
		}
		return renderer.Compose(ctx, document, metadata)
	}
	converter := onlinebulletins.SimplifiedRunner{NodePath: "/usr/local/bin/node", ScriptPath: "/opt/bulletin-conversion/convert.mjs"}
	repository := postgres.New(db).WithAudit(postgres.NewAuditOutbox(db)).WithLLMBulletinExtraction(cfg.LLMEnabled)
	if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.online_bulletin_recognition_checkpoint c USING hhc_web.online_bulletin_extraction_job j WHERE c.job_id=j.id AND (j.superseded OR j.status='failed') AND j.updated_at<now()-interval '7 days'`); err != nil {
		return err
	}
	worker := onlinebulletins.ExtractionWorker{Store: repository, Timeout: cfg.Timeout, BatchSize: cfg.BatchSize, MaxAttempts: cfg.MaxAttempts,
		Compose: compose,
		// Component edits reflow with the same compiler before being measured.
		Validate: compose,
		Convert: func(ctx context.Context, job onlinebulletins.ConversionJob) (onlinebulletins.Document, onlinebulletins.CanonicalMetadata, error) {
			document, err := converter.ConvertSimplified(ctx, job.Source)
			if err != nil {
				return document, onlinebulletins.CanonicalMetadata{}, err
			}
			metadata, err := converter.ConvertMetadata(ctx, job.SourceMetadata)
			if err != nil {
				return document, metadata, err
			}
			document.TemplateVersion = "v2"
			document.LayoutManifest.TemplateVersion = "v2"
			if document.LayoutManifest.RendererVersion == "v11" {
				document.LayoutManifest.RendererArtifactSha256 = onlinebulletins.RendererV11SHA256
			} else if document.LayoutManifest.RendererVersion == "v10" {
				document.LayoutManifest.RendererArtifactSha256 = rendererArtifactSHA256
			} else if document.LayoutManifest.RendererVersion == "v9" {
				document.LayoutManifest.RendererArtifactSha256 = rendererV9ArtifactSHA256
			} else {
				document.LayoutManifest.RendererVersion = "v8"
				document.LayoutManifest.RendererArtifactSha256 = rendererV8ArtifactSHA256
			}
			document.LayoutManifest.Assets = v2Assets
			return document, metadata, nil
		},
		Extract: func(ctx context.Context, job onlinebulletins.ExtractionJob) (candidate onlinebulletins.DocumentCandidate, issues []onlinebulletins.ReviewIssue, err error) {
			source := assetclient.Asset{ID: job.SourceAssetID, OwnerID: job.Canonical.IssueID, Locale: job.Canonical.ContentLocale, ChecksumSHA256: job.SourceAssetChecksum}
			err = assetClient.WithBulletinSource(ctx, source, job.ID, job.Attempts, func(path string) error {
				images := map[string]onlinebulletins.SourceImage{}
				geometry, err := onlinebulletins.ParsePDFWithSourceImages(ctx, path, func(part onlinebulletins.SourcePart, png []byte) error {
					asset, _, err := assetClient.EnsureBulletinIllustration(ctx, job.Canonical.IssueID, part.ID, png)
					if err != nil {
						return err
					}
					sum := sha256.Sum256(png)
					images[part.ID] = onlinebulletins.SourceImage{AssetID: asset.ID, SHA256: hex.EncodeToString(sum[:]), SizeBytes: len(png)}
					return nil
				})
				if err != nil {
					return err
				}
				candidate, issues, err = candidateForJob(geometry, job.Canonical, job.TemplateSnapshot, assets)
				candidate.SourceImages = images
				return err
			})
			return candidate, issues, err
		},
	}

	if cfg.LLMEnabled {
		client, err := onlinebulletins.NewAzureExtractor(cfg.AzureEndpoint, onlinebulletins.RecognitionDeployment, cfg.AzureAPIKey, cfg.AzureRAIPolicy, nil)
		if err != nil {
			return err
		}
		runner := onlinebulletins.RecognitionRunner{Client: client, Store: repository, Assets: assets}
		worker.AutoPublish = repository.AutoPublishNextRecognition
		worker.AssessDerived = func(ctx context.Context, job onlinebulletins.ConversionJob, document onlinebulletins.Document, metadata onlinebulletins.CanonicalMetadata) error {
			input, err := onlinebulletins.SimplifiedAssessmentInput(job.Source, job.SourceMetadata, document, metadata)
			if err != nil {
				return err
			}
			if !job.InitializesMetadata {
				input.LocalMetadata = &job.TargetMetadata
			}
			var saved []onlinebulletins.UpdateAttempt
			for _, attempt := range job.AssessmentAttempts {
				if attempt.ErrorCode == "" {
					saved = append(saved, attempt)
				}
			}
			_, err = client.ResumeVerifiedUpdate(ctx, input, saved, func(attempt onlinebulletins.UpdateAttempt) error {
				deadline := time.Now().Add(5 * time.Second)
				if job.LeaseUntil.Before(deadline) {
					deadline = job.LeaseUntil
				}
				persistence, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
				defer cancel()
				return repository.SaveOnlineBulletinConversionAssessment(persistence, job, input, attempt, time.Now().UTC())
			})
			return err
		}
		worker.RepairLayout = func(ctx context.Context, job onlinebulletins.LayoutJob, issues []onlinebulletins.ReviewIssue) (onlinebulletins.LayoutResult, error) {
			var evidence onlinebulletins.LayoutRepairEvidence
			if job.Repair != nil {
				evidence = *job.Repair
			} else {
				inference, cancel := context.WithTimeout(ctx, 20*time.Second)
				var err error
				evidence, err = client.PlanLayoutRepair(inference, job, issues)
				cancel()
				if evidence.InputSHA256 != "" {
					persistence, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					saveErr := repository.SaveOnlineBulletinLayoutRepair(persistence, job, evidence, time.Now().UTC())
					stop()
					if saveErr != nil {
						return onlinebulletins.LayoutResult{}, saveErr
					}
				}
				if err != nil {
					return onlinebulletins.LayoutResult{}, err
				}
			}
			repaired, err := onlinebulletins.ApplyLayoutRepairEvidence(job, issues, evidence)
			if err != nil {
				return onlinebulletins.LayoutResult{}, err
			}
			return compose(ctx, repaired, job.Canonical)
		}
		worker.Update = func(ctx context.Context, job onlinebulletins.UpdateJob) (onlinebulletins.UpdateEvidence, error) {
			return client.ResumeVerifiedUpdate(ctx, job.Input, job.SavedAttempts, func(attempt onlinebulletins.UpdateAttempt) error {
				deadline := time.Now().Add(5 * time.Second)
				if job.LeaseUntil.Before(deadline) {
					deadline = job.LeaseUntil
				}
				persistence, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
				defer cancel()
				return repository.SaveOnlineBulletinUpdateAttempt(persistence, job, attempt, time.Now().UTC())
			})
		}
		worker.Recognize = func(ctx context.Context, job onlinebulletins.ExtractionJob) (onlinebulletins.DocumentCandidate, []onlinebulletins.ReviewIssue, error) {
			source := assetclient.Asset{ID: job.SourceAssetID, OwnerID: job.Canonical.IssueID, Locale: job.Canonical.ContentLocale, ChecksumSHA256: job.SourceAssetChecksum}
			cached, issues, err := repository.LoadReusableRecognition(ctx, job, time.Now().UTC())
			if err != nil {
				return onlinebulletins.DocumentCandidate{}, nil, err
			}
			if cached != nil {
				if len(cached.SourceCoverPNG) == 0 {
					err = assetClient.WithBulletinSource(ctx, source, job.ID, job.Attempts, func(path string) error {
						pages, err := onlinebulletins.PrepareRecognitionSource(ctx, path)
						if err != nil {
							return err
						}
						cached.SourceCoverPNG = append([]byte{}, pages[0].PNG...)
						return nil
					})
					if err != nil {
						return onlinebulletins.DocumentCandidate{}, nil, err
					}
				}
				return *cached, issues, nil
			}
			var geometry onlinebulletins.GeometryDocument
			var pages []onlinebulletins.RecognitionPage
			images := map[string]onlinebulletins.SourceImage{}
			err = assetClient.WithBulletinSource(ctx, source, job.ID, job.Attempts, func(path string) error {
				var err error
				geometry, err = onlinebulletins.ParseRecognitionPDFWithSourceImages(ctx, path, func(part onlinebulletins.SourcePart, png []byte) error {
					asset, _, err := assetClient.EnsureBulletinIllustration(ctx, job.Canonical.IssueID, part.ID, png)
					if err != nil {
						return err
					}
					sum := sha256.Sum256(png)
					images[part.ID] = onlinebulletins.SourceImage{AssetID: asset.ID, SHA256: hex.EncodeToString(sum[:]), SizeBytes: len(png)}
					return nil
				})
				if err != nil {
					return err
				}
				pages, err = onlinebulletins.PrepareRecognitionSource(ctx, path)
				return err
			})
			if err != nil {
				return onlinebulletins.DocumentCandidate{}, nil, err
			}
			return runner.Run(ctx, job, geometry, pages, images)
		}
	}
	if cfg.AzureEndpoint != "" && cfg.AzureDeployment != "" && cfg.AzureAPIKey != "" && cfg.AzureRAIPolicy != "" {
		worker.Classify = onlinebulletins.NewAzureClassifier(cfg.AzureEndpoint, cfg.AzureDeployment, cfg.AzureAPIKey, nil, cfg.AzureRAIPolicy).Classify
	}
	queue, err := onlinebulletins.NewWakeQueue(cfg.QueueURL, cfg.PoisonQueueURL, cfg.LLMEnabled)
	if err != nil {
		return errInvalidJob
	}
	return onlinebulletins.RunExtractionWake(ctx, queue, worker.RunBatch, repository.NextOnlineBulletinWork)
}

func candidateForJob(geometry onlinebulletins.GeometryDocument, canonical onlinebulletins.CanonicalIssue, snapshot *onlinebulletins.TemplateSettings, assets []onlinebulletins.Asset) (onlinebulletins.DocumentCandidate, []onlinebulletins.ReviewIssue, error) {
	manifest := onlinebulletins.LayoutManifest{TemplateVersion: "v1", RendererVersion: "v8", RendererArtifactSha256: rendererV8ArtifactSHA256, Assets: assets}
	if snapshot == nil {
		return onlinebulletins.ApplyTemplateV1(geometry, canonical, manifest)
	}
	manifest.RendererVersion, manifest.RendererArtifactSha256 = "v10", rendererArtifactSHA256
	candidate, issues, err := onlinebulletins.ApplyTemplateV10(geometry, canonical, manifest, *snapshot)
	if err == nil && candidate.Document != nil {
		candidate.Document.LayoutManifest.RendererVersion, candidate.Document.LayoutManifest.RendererArtifactSha256 = "v11", onlinebulletins.RendererV11SHA256
	}
	return candidate, issues, err
}

func layoutRunner(version string) (onlinebulletins.LayoutRunner, error) {
	runner := onlinebulletins.LayoutRunner{NodePath: "/usr/local/bin/node", AssetsDirectory: "/opt/bulletin-renderer/assets"}
	switch version {
	case "v1":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout.mjs", rendererV1ArtifactSHA256
	case "v2":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v2.mjs", rendererV2ArtifactSHA256
	case "v3":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v3.mjs", rendererV3ArtifactSHA256
	case "v4":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v4.mjs", rendererV4ArtifactSHA256
	case "v5":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v5.mjs", rendererV5ArtifactSHA256
	case "v6":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v6.mjs", rendererV6ArtifactSHA256
	case "v7":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v7.mjs", rendererV7ArtifactSHA256
	case "v8":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v8.mjs", rendererV8ArtifactSHA256
	case "v9":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v9.mjs", rendererV9ArtifactSHA256
	case "v11":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v11.mjs", onlinebulletins.RendererV11SHA256
	case "v10":
		runner.ScriptPath, runner.RendererArtifactSHA256 = "measure-bulletin-layout-v10.mjs", rendererArtifactSHA256
	default:
		return runner, errInvalidJob
	}
	runner.ScriptPath = "/opt/bulletin-renderer/scripts/" + runner.ScriptPath
	return runner, nil
}
