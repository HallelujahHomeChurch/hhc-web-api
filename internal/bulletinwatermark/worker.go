package bulletinwatermark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type InvestigationRepository interface {
	ClaimWatermarkInvestigation(context.Context, time.Time) (Investigation, bool, error)
	FindWatermarkReceiptByOutputSHA256(context.Context, string, time.Time) (Receipt, error)
	FindWatermarkReceiptByPayload(context.Context, string, time.Time) (Receipt, error)
	ListWatermarkCandidates(context.Context, CandidateScope, time.Time, int) ([]Receipt, error)
	CompleteWatermarkInvestigation(context.Context, string, string, InvestigationCompletion, time.Time) error
}

type InvestigationByIDRepository interface {
	InvestigationRepository
	ClaimWatermarkInvestigationByID(context.Context, string, time.Time) (Investigation, error)
	ExhaustWatermarkInvestigation(context.Context, string, time.Time) error
}
type InvestigationAssets interface {
	AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error)
}

func (r *Runner) RunInvestigations(ctx context.Context, repo InvestigationRepository, assets InvestigationAssets) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, found, err := repo.ClaimWatermarkInvestigation(ctx, time.Now().UTC())
		if err != nil {
			slog.Error("watermark investigation claim failed")
		}
		if err == nil && found {
			processCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
			completion := r.investigate(processCtx, repo, assets, job)
			cancel()
			if ctx.Err() != nil {
				return
			} // The lease recovers shutdown interruptions.
			completeCtx, completeCancel := context.WithTimeout(ctx, 10*time.Second)
			err = repo.CompleteWatermarkInvestigation(completeCtx, job.ID, job.LeaseToken, completion, time.Now().UTC())
			completeCancel()
			if err != nil {
				slog.Error("watermark investigation completion failed")
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) ProcessInvestigation(ctx context.Context, repo InvestigationByIDRepository, assets InvestigationAssets, id string) error {
	started := time.Now()
	job, err := repo.ClaimWatermarkInvestigationByID(ctx, id, time.Now().UTC())
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.Info("watermark investigation started", "investigation_id", job.ID, "candidate_scopes", len(job.CandidateScopes()))
	processCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	completion := r.investigate(processCtx, repo, assets, job)
	if err = repo.CompleteWatermarkInvestigation(ctx, job.ID, job.LeaseToken, completion, time.Now().UTC()); err != nil {
		slog.Error("watermark investigation completion failed", "investigation_id", job.ID, "duration_ms", time.Since(started).Milliseconds())
		return err
	}
	slog.Info("watermark investigation completed", "investigation_id", job.ID, "outcome", completion.Outcome, "reason_code", completion.ReasonCode, "match_method", completion.MatchMethod, "duration_ms", time.Since(started).Milliseconds())
	return nil
}

func (r *Runner) investigate(ctx context.Context, repo InvestigationRepository, assets InvestigationAssets, job Investigation) InvestigationCompletion {
	files, err := UnpackEvidence(job.Evidence)
	if err != nil {
		return InvestigationCompletion{Outcome: "failed", ReasonCode: "processing_failed"}
	}
	now := time.Now().UTC()
	findings := []Finding{}
	type raster struct {
		data   []byte
		hint   *CandidateScope
		source string
	}
	rasters := []raster{}
	pdfs := [][]byte{}
	for _, file := range files {
		if file.Kind != EvidencePDF {
			rasters = append(rasters, raster{data: file.Data, source: "upload"})
			continue
		}
		pdfs = append(pdfs, file.Data)
		digest := sha256.Sum256(file.Data)
		proof := hex.EncodeToString(digest[:])
		receipt, lookupErr := repo.FindWatermarkReceiptByOutputSHA256(ctx, proof, now)
		if lookupErr == nil {
			findings = append(findings, Finding{Kind: FindingExact, ReceiptID: receipt.ID, Method: MatchOutputSHA256, Proof: proof})
		} else if !errors.Is(lookupErr, ErrNotFound) {
			findings = append(findings, Finding{Kind: FindingFailure, Reason: "processing_failed"})
		}
	}
	slog.Info("watermark investigation inputs", "investigation_id", job.ID, "files", len(files), "upload_rasters", len(rasters), "pdfs", len(pdfs), "candidate_scopes", len(job.CandidateScopes()))
	decodeRasters := func(start int) {
		for index, image := range rasters[start:] {
			decodeStarted := time.Now()
			decoded, decodeErr := r.Decode(ctx, image.data)
			status := "inconclusive"
			if r.decoder == nil {
				status = "disabled"
			} else if decodeErr != nil {
				status = "failed"
			} else if decoded.Accepted {
				status = "decoded"
			}
			attributes := []any{"investigation_id", job.ID, "raster_source", image.source, "raster_index", start + index + 1, "status", status, "duration_ms", time.Since(decodeStarted).Milliseconds()}
			if r.decoder != nil && decodeErr == nil {
				attributes = append(attributes, "quality", decoded.Quality)
			}
			slog.Info("watermark direct decode", attributes...)
			if decodeErr != nil {
				findings = append(findings, Finding{Kind: FindingFailure, Reason: "decoder_unavailable"})
				continue
			}
			if !decoded.Accepted {
				continue
			}
			receipt, lookupErr := repo.FindWatermarkReceiptByPayload(ctx, decoded.Payload, now)
			if lookupErr == nil {
				findings = append(findings, Finding{Kind: FindingDecoded, ReceiptID: receipt.ID, Method: MatchPayload, Proof: decoded.Payload})
			} else if errors.Is(lookupErr, ErrNotFound) {
				findings = append(findings, Finding{Kind: FindingInconclusive, Reason: "no_active_issuance"})
			} else {
				findings = append(findings, Finding{Kind: FindingFailure, Reason: "processing_failed"})
			}
		}
	}
	decodeRasters(0)
	if completion := AggregateFindings(findings); completion.Outcome == "matched" || completion.Outcome == "conflict" {
		return completion
	}
	pageCount := 0
	pdfRasterStart := len(rasters)
	for _, pdf := range pdfs {
		query, queryErr := r.QueryPDF(ctx, pdf)
		if queryErr != nil {
			findings = append(findings, Finding{Kind: FindingFailure, Reason: "processing_failed"})
			continue
		}
		pageCount += len(query.Images)
		if pageCount > MaxPages {
			return InvestigationCompletion{Outcome: "failed", ReasonCode: "processing_failed"}
		}
		var hint *CandidateScope
		if query.ScopeHint != nil {
			if scope, ok := query.ScopeHint.CompatibleScope(job.CandidateScopes()); ok {
				hint = &scope
				findings = append(findings, Finding{Kind: FindingScopeHint, Reason: scope.IssueID})
			}
		}
		for _, image := range query.Images {
			rasters = append(rasters, raster{data: image, hint: hint, source: "pdf_page"})
		}
	}
	decodeRasters(pdfRasterStart)
	if completion := AggregateFindings(findings); completion.Outcome == "matched" || completion.Outcome == "conflict" {
		return completion
	}
	type candidateScope struct {
		scope      CandidateScope
		candidates []Receipt
	}
	sets := make([]candidateScope, 0, len(job.CandidateScopes()))
	for _, scope := range job.CandidateScopes() {
		candidates, listErr := repo.ListWatermarkCandidates(ctx, scope, now, 64)
		if listErr != nil {
			findings = append(findings, Finding{Kind: FindingFailure, Reason: "candidate_set_unavailable"})
			continue
		}
		if len(candidates) > 0 {
			sets = append(sets, candidateScope{scope: scope, candidates: candidates})
		}
	}
	if len(sets) == 0 {
		findings = append(findings, Finding{Kind: FindingInconclusive, Reason: "no_active_issuance"})
	}
	comparableScopes, candidateCount := 0, 0
	for _, set := range sets {
		candidateCount += len(set.candidates)
		if len(set.candidates) >= 2 {
			comparableScopes++
		}
	}
	slog.Info("watermark candidate fallback", "investigation_id", job.ID, "candidate_scopes", len(job.CandidateScopes()), "candidate_sets", len(sets), "comparable_scopes", comparableScopes, "candidate_count", candidateCount, "rasters", len(rasters))
	sources := map[string][]byte{}
	for rasterIndex, image := range rasters {
		locationFailure := ""
		locations := []struct {
			set  candidateScope
			page int
		}{}
		for _, set := range sets {
			if len(set.candidates) < 2 || image.hint != nil && set.scope != *image.hint {
				continue
			}
			source, ok := sources[set.scope.SourceAssetID]
			if !ok {
				response, readErr := assets.AuthorizedDownload(ctx, http.MethodGet, set.scope.SourceAssetID, "", "")
				if readErr != nil || response == nil || response.StatusCode != http.StatusOK {
					if response != nil {
						response.Body.Close()
					}
					findings = append(findings, Finding{Kind: FindingFailure, Reason: "source_unavailable"})
					locationFailure = "source_unavailable"
					continue
				}
				source, readErr = io.ReadAll(io.LimitReader(response.Body, MaxInputBytes+1))
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || int64(len(source)) > MaxInputBytes {
					findings = append(findings, Finding{Kind: FindingFailure, Reason: "source_unavailable"})
					locationFailure = "source_unavailable"
					continue
				}
				sources[set.scope.SourceAssetID] = source
			}
			page, locateErr := r.Locate(ctx, bytes.NewReader(source), set.scope, image.data)
			if locateErr != nil {
				findings = append(findings, Finding{Kind: FindingFailure, Reason: "processing_failed"})
				if locationFailure == "" {
					locationFailure = "location_failed"
				}
				continue
			}
			if page > 0 {
				locations = append(locations, struct {
					set  candidateScope
					page int
				}{set: set, page: page})
			}
		}
		if len(locations) == 0 {
			status := "source_not_located"
			if locationFailure != "" {
				status = locationFailure
			}
			slog.Info("watermark candidate detection", "investigation_id", job.ID, "raster_source", image.source, "raster_index", rasterIndex+1, "status", status)
			findings = append(findings, Finding{Kind: FindingInconclusive, Reason: "watermark_inconclusive"})
			continue
		}
		if len(locations) > 1 {
			slog.Info("watermark candidate detection", "investigation_id", job.ID, "raster_source", image.source, "raster_index", rasterIndex+1, "status", "source_ambiguous", "located_scopes", len(locations))
			findings = append(findings, Finding{Kind: FindingInconclusive, Reason: "source_scope_ambiguous"})
			continue
		}
		location := locations[0]
		ident, detectErr := r.DetectLocated(ctx, bytes.NewReader(sources[location.set.scope.SourceAssetID]), Investigation{Scope: location.set.scope, Evidence: image.data}, location.set.candidates, location.page)
		if detectErr != nil {
			slog.Info("watermark candidate detection", "investigation_id", job.ID, "raster_source", image.source, "raster_index", rasterIndex+1, "status", "failed")
			findings = append(findings, Finding{Kind: FindingFailure, Reason: "processing_failed"})
		} else if ident == "" {
			slog.Info("watermark candidate detection", "investigation_id", job.ID, "raster_source", image.source, "raster_index", rasterIndex+1, "status", "inconclusive")
			findings = append(findings, Finding{Kind: FindingInconclusive, Reason: "watermark_inconclusive"})
		} else {
			slog.Info("watermark candidate detection", "investigation_id", job.ID, "raster_source", image.source, "raster_index", rasterIndex+1, "status", "matched")
			findings = append(findings, Finding{Kind: FindingCandidate, ReceiptID: ident, Method: MatchCandidate, Proof: CandidateSetHash(location.set.candidates)})
		}
	}
	return AggregateFindings(findings)
}
