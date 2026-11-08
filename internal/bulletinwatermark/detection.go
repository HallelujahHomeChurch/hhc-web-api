package bulletinwatermark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

type detectionCandidate struct {
	ID            string `json:"id"`
	Image         string `json:"image"`
	SourceSHA256  string `json:"source_sha256"`
	OutputSHA256  string `json:"output_sha256"`
	EngineVersion string `json:"engine_version"`
	ModelSHA256   string `json:"model_sha256"`
}

// Detect reconstructs the entire bounded candidate set. A missing source or
// changed renderer cannot fall back to a different publication or model.
func (r *Runner) Detect(ctx context.Context, source io.Reader, job Investigation, receipts []Receipt) (string, error) {
	return r.detect(ctx, source, job, receipts, 0)
}

func (r *Runner) DetectLocated(ctx context.Context, source io.Reader, job Investigation, receipts []Receipt, page int) (string, error) {
	if page < 1 || len(receipts) == 0 || page > len(receipts[0].Pages) {
		return "", ErrUnavailable
	}
	return r.detect(ctx, source, job, receipts, page)
}

func (r *Runner) detect(ctx context.Context, source io.Reader, job Investigation, receipts []Receipt, page int) (string, error) {
	if r == nil || len(receipts) < 2 || len(receipts) > 64 || len(job.Evidence) > 10<<20 {
		return "", ErrUnavailable
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-r.slots }()
	dir, err := os.MkdirTemp(r.root, "render-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err = writeSourceQuery(dir, source, job.Evidence, job.Scope.SourceSHA256); err != nil {
		return "", err
	}
	candidates := make([]map[string]string, len(receipts))
	for i, v := range receipts {
		if v.ValidateFingerprint() != nil || v.SourceSHA256 != job.Scope.SourceSHA256 || v.RendererVersion != job.Scope.RendererVersion || v.ModelSHA256 != job.Scope.ModelSHA256 || len(v.Pages) != len(receipts[0].Pages) {
			return "", ErrUnavailable
		}
		candidates[i] = map[string]string{"id": v.ID, "payload": v.FingerprintPayload}
	}
	var templates struct {
		Status          string               `json:"status"`
		Source          string               `json:"source"`
		PDFSourceSHA256 string               `json:"pdf_source_sha256"`
		RendererVersion string               `json:"pdf_engine_version"`
		ModelSHA256     string               `json:"model_sha256"`
		Page            int                  `json:"page"`
		Width           int                  `json:"width"`
		Height          int                  `json:"height"`
		Candidates      []detectionCandidate `json:"candidates"`
	}
	request := map[string]any{"operation": "templates", "source": "source.pdf", "query": "query.png", "candidates": candidates}
	if page > 0 {
		request["page"] = page
	}
	if err = r.run(ctx, dir, request, &templates); err != nil {
		return "", err
	}
	if templates.Status == "inconclusive" {
		return "", nil
	}
	if templates.Status != "templates" || templates.Source != "source.png" || templates.PDFSourceSHA256 != job.Scope.SourceSHA256 || templates.RendererVersion != job.Scope.RendererVersion || templates.ModelSHA256 != job.Scope.ModelSHA256 || templates.Page < 1 || templates.Page > len(receipts[0].Pages) || len(templates.Candidates) != len(receipts) {
		return "", ErrUnavailable
	}
	for i, c := range templates.Candidates {
		p := receipts[i].Pages[templates.Page-1]
		if c.ID != receipts[i].ID || c.SourceSHA256 != p.SourceSHA256 || c.OutputSHA256 != p.OutputSHA256 || c.ModelSHA256 != job.Scope.ModelSHA256 || templates.Width != p.Width || templates.Height != p.Height {
			return "", ErrUnavailable
		}
	}
	var result struct {
		Status string `json:"status"`
		ID     string `json:"candidate_id"`
	}
	if err = r.run(ctx, dir, map[string]any{"operation": "detect", "source": "source.png", "query": "query.png", "candidates": templates.Candidates}, &result); err != nil {
		return "", err
	}
	if result.Status == "inconclusive" && result.ID == "" {
		return "", nil
	}
	if result.Status == "matched" {
		for _, v := range receipts {
			if result.ID == v.ID {
				return result.ID, nil
			}
		}
	}
	return "", ErrUnavailable
}

func (r *Runner) Locate(ctx context.Context, source io.Reader, scope CandidateScope, query []byte) (int, error) {
	if r == nil || len(query) == 0 || len(query) > 10<<20 {
		return 0, ErrUnavailable
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-r.slots }()
	dir, err := os.MkdirTemp(r.root, "render-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	if err = writeSourceQuery(dir, source, query, scope.SourceSHA256); err != nil {
		return 0, err
	}
	var result struct {
		Status          string `json:"status"`
		Page            int    `json:"page"`
		PDFSourceSHA256 string `json:"pdf_source_sha256"`
		RendererVersion string `json:"pdf_engine_version"`
		ModelSHA256     string `json:"model_sha256"`
	}
	if err = r.run(ctx, dir, map[string]string{"operation": "locate", "source": "source.pdf", "query": "query.png"}, &result); err != nil {
		return 0, err
	}
	if result.PDFSourceSHA256 != scope.SourceSHA256 || result.RendererVersion != scope.RendererVersion || result.ModelSHA256 != scope.ModelSHA256 {
		return 0, ErrUnavailable
	}
	if result.Status == "inconclusive" && result.Page == 0 {
		return 0, nil
	}
	if result.Status != "located" || result.Page < 1 || result.Page > MaxPages {
		return 0, ErrUnavailable
	}
	return result.Page, nil
}

func writeSourceQuery(dir string, source io.Reader, query []byte, expectedHash string) error {
	f, err := os.OpenFile(filepath.Join(dir, "source.pdf"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(source, MaxInputBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || size > MaxInputBytes || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return ErrUnavailable
	}
	return os.WriteFile(filepath.Join(dir, "query.png"), query, 0600)
}
