package bulletinwatermark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type workerRepositoryStub struct {
	candidates []Receipt
	err        error
	calls      int
	job        Investigation
	completion InvestigationCompletion
	completed  bool
}

func (s *workerRepositoryStub) ClaimWatermarkInvestigation(context.Context, time.Time) (Investigation, bool, error) {
	panic("not used")
}
func (s *workerRepositoryStub) ClaimWatermarkInvestigationByID(_ context.Context, id string, _ time.Time) (Investigation, error) {
	if s.job.ID != id {
		return Investigation{}, ErrNotFound
	}
	return s.job, nil
}
func (s *workerRepositoryStub) ExhaustWatermarkInvestigation(context.Context, string, time.Time) error {
	panic("not used")
}
func (s *workerRepositoryStub) CompleteWatermarkInvestigation(_ context.Context, id, _ string, completion InvestigationCompletion, _ time.Time) error {
	if id != s.job.ID {
		return ErrNotFound
	}
	s.completion, s.completed = completion, true
	return nil
}
func (s *workerRepositoryStub) FindWatermarkReceiptByOutputSHA256(_ context.Context, value string, _ time.Time) (Receipt, error) {
	for _, receipt := range s.candidates {
		if receipt.OutputSHA256 == value {
			return receipt, nil
		}
	}
	return Receipt{}, ErrNotFound
}
func (s *workerRepositoryStub) FindWatermarkReceiptByPayload(_ context.Context, value string, _ time.Time) (Receipt, error) {
	for _, receipt := range s.candidates {
		if receipt.FingerprintPayload == value {
			return receipt, nil
		}
	}
	return Receipt{}, ErrNotFound
}
func (s *workerRepositoryStub) ListWatermarkCandidates(context.Context, CandidateScope, time.Time, int) ([]Receipt, error) {
	s.calls++
	return s.candidates, s.err
}

type workerAssetsStub struct {
	calls int
	err   error
	body  *workerBody
}

func (s *workerAssetsStub) AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{StatusCode: 200, Body: s.body}, nil
}

type workerBody struct {
	io.Reader
	closed  bool
	onClose func()
}

func (b *workerBody) Close() error {
	b.closed = true
	if b.onClose != nil {
		b.onClose()
		b.onClose = nil
	}
	return nil
}

// This subprocess emits the template protocol without loading a model.
func TestInvestigationProcess(t *testing.T) {
	dir := ""
	for i, arg := range os.Args {
		if arg == "--job-dir" && i+1 < len(os.Args) {
			dir = os.Args[i+1]
		}
	}
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		os.Exit(2)
	}
	var request struct {
		Operation  string               `json:"operation"`
		Candidates []detectionCandidate `json:"candidates"`
	}
	if json.Unmarshal(b, &request) != nil {
		os.Exit(2)
	}
	if request.Operation == "detect" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "matched", "candidate_id": request.Candidates[0].ID})
		os.Exit(0)
	}
	if request.Operation == "query_pdf" {
		names := make([]string, 31)
		for i := range names {
			names[i] = fmt.Sprintf("query-%02d.png", i+1)
			_ = os.WriteFile(filepath.Join(dir, names[i]), append([]byte("\x89PNG\r\n\x1a\n"), byte(i)), 0600)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "query_pdf", "images": names, "scope_hint": nil})
		os.Exit(0)
	}
	hash := sha256.Sum256([]byte("source"))
	if request.Operation == "locate" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "located", "page": 1, "pdf_source_sha256": hex.EncodeToString(hash[:]), "pdf_engine_version": "test-pdf-invisible", "model_sha256": strings.Repeat("c", 64)})
		os.Exit(0)
	}
	candidates := request.Candidates
	for i := range candidates {
		candidates[i].Image = "candidate.png"
		candidates[i].SourceSHA256 = strings.Repeat("a", 64)
		candidates[i].OutputSHA256 = strings.Repeat("b", 64)
		candidates[i].ModelSHA256 = strings.Repeat("c", 64)
		candidates[i].EngineVersion = "test"
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "templates", "source": "source.png", "pdf_source_sha256": hex.EncodeToString(hash[:]), "pdf_engine_version": "test-pdf-invisible", "model_sha256": strings.Repeat("c", 64), "page": 1, "width": 10, "height": 10, "candidates": candidates})
	os.Exit(0)
}

func TestInvestigationWorkerFailsClosed(t *testing.T) {
	binary, _ := os.Executable()
	script := filepath.Join(t.TempDir(), "helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=TestInvestigationProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(script, filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	sourceHash := sha256.Sum256([]byte("source"))
	scope := CandidateScope{IssueID: uuid.NewString(), Locale: "zh-Hant", SourceAssetID: uuid.NewString(), Revision: 1, SourceSHA256: hex.EncodeToString(sourceHash[:]), RendererVersion: "test-pdf-invisible", ModelSHA256: strings.Repeat("c", 64)}
	first := Receipt{ID: "first", SourceSHA256: scope.SourceSHA256, RendererVersion: scope.RendererVersion, ModelSHA256: scope.ModelSHA256, FingerprintPayload: strings.Repeat("0", 61), Pages: []PageMetadata{{Page: 1, Width: 10, Height: 10, SourceSHA256: strings.Repeat("a", 64), OutputSHA256: strings.Repeat("b", 64)}}}
	second := first
	second.ID = "second"
	second.FingerprintPayload = strings.Repeat("1", 61)
	tests := []struct {
		name                  string
		candidates            []Receipt
		repoErr, errorSource  error
		source                string
		wantOutcome, wantCode string
		waitForSlot           bool
		wantAssetCalls        int
	}{
		{name: "candidate overflow", repoErr: ErrCandidateLimit, wantOutcome: "failed", wantCode: "candidate_set_unavailable"},
		{name: "one candidate", candidates: []Receipt{first}, source: "source", wantOutcome: "inconclusive", wantCode: "watermark_inconclusive"},
		{name: "source unavailable", candidates: []Receipt{first, second}, errorSource: errors.New("unavailable"), wantOutcome: "failed", wantCode: "source_unavailable", wantAssetCalls: 1},
		{name: "source hash changed", candidates: []Receipt{first, second}, source: "different", wantOutcome: "failed", wantCode: "processing_failed", wantAssetCalls: 1},
		{name: "source closed before waiting for occupied render slot", candidates: []Receipt{first, second}, source: "source", wantOutcome: "matched", wantAssetCalls: 1, waitForSlot: true},
		{name: "complete candidate snapshot", candidates: []Receipt{first, second}, source: "source", wantOutcome: "matched", wantAssetCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &workerRepositoryStub{candidates: tc.candidates, err: tc.repoErr}
			body := &workerBody{Reader: strings.NewReader(tc.source)}
			assets := &workerAssetsStub{err: tc.errorSource, body: body}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if tc.waitForSlot {
				runner.slots <- struct{}{}
				body.onClose = func() { <-runner.slots }
			}
			completion := runner.investigate(ctx, repo, assets, Investigation{ActorID: "actor", Scope: scope, Evidence: append([]byte("\x89PNG\r\n\x1a\n"), []byte("synthetic")...)})
			if completion.Outcome != tc.wantOutcome || completion.ReasonCode != tc.wantCode {
				t.Fatalf("outcome=%s code=%s", completion.Outcome, completion.ReasonCode)
			}
			if assets.calls != tc.wantAssetCalls {
				t.Fatalf("unexpected asset calls: %d", assets.calls)
			}
			if completion.Outcome == "matched" {
				if completion.ReceiptID != first.ID || completion.MatchMethod != MatchCandidate || completion.CandidateSetHash != CandidateSetHash(tc.candidates) {
					t.Fatal("incorrect candidate snapshot")
				}
			} else if completion.ReceiptID != "" || completion.CandidateSetHash != "" {
				t.Fatal("failed job retained result")
			}
			if assets.calls > 0 && assets.err == nil && !body.closed {
				t.Fatal("source body left open")
			}
			entries, err := os.ReadDir(runner.root)
			if err != nil || len(entries) != 1 || entries[0].Name() != ".lock" {
				t.Fatal("private evidence retained")
			}
		})
	}
}

func TestInvestigationExactPDFHashStopsBeforeRendering(t *testing.T) {
	pdf := []byte("%PDF-1.7\nbody\n%%EOF\n")
	hash := sha256.Sum256(pdf)
	receipt := Receipt{ID: uuid.NewString(), UserID: uuid.NewString(), OutputSHA256: hex.EncodeToString(hash[:])}
	repo := &workerRepositoryStub{candidates: []Receipt{receipt}}
	assets := &workerAssetsStub{}
	evidence, err := PackEvidence([]EvidenceFile{{Data: pdf}})
	if err != nil {
		t.Fatal(err)
	}
	completion := new(Runner).investigate(context.Background(), repo, assets, Investigation{ActorID: "actor", Scope: CandidateScope{IssueID: "issue", Locale: "zh-Hant"}, Evidence: evidence})
	if completion.Outcome != "matched" || completion.ReceiptID != receipt.ID || completion.MatchMethod != MatchOutputSHA256 || completion.Proof != receipt.OutputSHA256 || completion.CandidateSetHash != "" || completion.ReasonCode != "" || assets.calls != 0 {
		t.Fatalf("completion=%+v calls=%d", completion, assets.calls)
	}
}

func TestInvestigationRejectsMoreThanSixtyPagesAcrossPDFs(t *testing.T) {
	binary, _ := os.Executable()
	script := filepath.Join(t.TempDir(), "helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=TestInvestigationProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(script, filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Close() })
	evidence, err := PackEvidence([]EvidenceFile{{Data: []byte("%PDF-1.7\na\n%%EOF")}, {Data: []byte("%PDF-1.7\nb\n%%EOF")}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &workerRepositoryStub{candidates: []Receipt{{ID: "a", UserID: "user-a"}, {ID: "b", UserID: "user-b"}}}
	completion := runner.investigate(context.Background(), repo, &workerAssetsStub{}, Investigation{Scope: CandidateScope{IssueID: "issue", Locale: "zh-Hant"}, Evidence: evidence})
	if completion.Outcome != "failed" || completion.ReasonCode != "processing_failed" {
		t.Fatalf("completion=%+v", completion)
	}
}

func investigationRunner(t *testing.T, direct bool) *Runner {
	t.Helper()
	binary, _ := os.Executable()
	script := filepath.Join(t.TempDir(), "investigation-helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	content := "#!/bin/sh\nif [ \"$1\" = \"--decode-server\" ]; then exec " + quote(binary) + " -test.run=TestDecoderProcess -- \"$@\"; fi\nexec " + quote(binary) + " -test.run=TestInvestigationProcess -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(script, filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	runner.ConfigureDirectDecoder(direct)
	t.Cleanup(func() { runner.Close() })
	return runner
}

func TestInvestigationDirectDecodeFallbackAndCrossMethodConflict(t *testing.T) {
	pdf := []byte("%PDF-1.7\nbody\n%%EOF\n")
	pdfHash := sha256.Sum256(pdf)
	direct := Receipt{ID: "direct", FingerprintPayload: strings.Repeat("1", 61)}
	exact := Receipt{ID: "exact", OutputSHA256: hex.EncodeToString(pdfHash[:])}
	scope := CandidateScope{IssueID: "issue", Locale: "zh-Hant"}

	t.Run("direct match", func(t *testing.T) {
		completion := investigationRunner(t, true).investigate(context.Background(), &workerRepositoryStub{candidates: []Receipt{direct}}, &workerAssetsStub{}, Investigation{Scope: scope, Evidence: decoderImage("first")})
		if completion.Outcome != "matched" || completion.ReceiptID != direct.ID || completion.MatchMethod != MatchPayload || completion.Proof != direct.FingerprintPayload {
			t.Fatalf("completion=%+v", completion)
		}
	})
	t.Run("disabled falls back", func(t *testing.T) {
		completion := investigationRunner(t, false).investigate(context.Background(), &workerRepositoryStub{candidates: []Receipt{direct}}, &workerAssetsStub{}, Investigation{Scope: scope, Evidence: decoderImage("first")})
		if completion.Outcome != "inconclusive" || completion.ReasonCode != "watermark_inconclusive" {
			t.Fatalf("completion=%+v", completion)
		}
	})
	t.Run("cross method conflict", func(t *testing.T) {
		evidence, err := PackEvidence([]EvidenceFile{{Data: pdf}, {Data: decoderImage("first")}})
		if err != nil {
			t.Fatal(err)
		}
		completion := investigationRunner(t, true).investigate(context.Background(), &workerRepositoryStub{candidates: []Receipt{exact, direct}}, &workerAssetsStub{}, Investigation{Scope: scope, Evidence: evidence})
		if completion.Outcome != "conflict" || completion.ReasonCode != "evidence_conflict" {
			t.Fatalf("completion=%+v", completion)
		}
	})
}

func TestInvestigationTelemetryReportsQualityAndOutcomeWithoutSecrets(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const privatePayload = "1111111111111111111111111111111111111111111111111111111111111"
	evidence, err := PackEvidence([]EvidenceFile{{Name: "private-filename.png", Data: decoderImage("inconclusive-private-image")}})
	if err != nil {
		t.Fatal(err)
	}
	job := Investigation{
		ID:         uuid.NewString(),
		ActorID:    "private-actor-id",
		LeaseToken: uuid.NewString(),
		Scope:      CandidateScope{IssueID: uuid.NewString(), Locale: "zh-Hant"},
		Evidence:   evidence,
	}
	runner := investigationRunner(t, true)
	repo := &workerRepositoryStub{job: job, candidates: []Receipt{{ID: "private-receipt-id", UserID: "private-account-id", FingerprintPayload: privatePayload}}}
	if err = runner.ProcessInvestigation(context.Background(), repo, &workerAssetsStub{}, job.ID); err != nil {
		t.Fatal(err)
	}
	if !repo.completed || repo.completion.Outcome != "inconclusive" {
		t.Fatalf("completion=%+v completed=%v", repo.completion, repo.completed)
	}
	matchedJob := job
	matchedJob.ID = uuid.NewString()
	matchedJob.Evidence, err = PackEvidence([]EvidenceFile{{Name: "private-matched-filename.png", Data: decoderImage("decoded-private-image")}})
	if err != nil {
		t.Fatal(err)
	}
	matchedRepo := &workerRepositoryStub{job: matchedJob, candidates: repo.candidates}
	if err := runner.ProcessInvestigation(context.Background(), matchedRepo, &workerAssetsStub{}, matchedJob.ID); err != nil {
		t.Fatal(err)
	}
	if !matchedRepo.completed || matchedRepo.completion.Outcome != "matched" {
		t.Fatalf("matched completion=%+v completed=%v", matchedRepo.completion, matchedRepo.completed)
	}

	logs := output.String()
	for _, expected := range []string{
		`"msg":"watermark investigation started"`,
		`"investigation_id":"` + job.ID + `"`,
		`"msg":"watermark investigation inputs"`,
		`"files":1`, `"upload_rasters":1`, `"pdfs":0`,
		`"msg":"watermark direct decode"`, `"status":"inconclusive"`, `"quality":0.7`, `"status":"decoded"`, `"quality":0.95`, `"raster_source":"upload"`,
		`"msg":"watermark candidate fallback"`, `"candidate_sets":1`, `"comparable_scopes":0`, `"candidate_count":1`,
		`"msg":"watermark investigation completed"`, `"outcome":"inconclusive"`, `"reason_code":"watermark_inconclusive"`, `"duration_ms":`,
		`"outcome":"matched"`, `"match_method":"payload"`,
	} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("missing %s in logs:\n%s", expected, logs)
		}
	}
	for _, secret := range []string{privatePayload, "private-receipt-id", "private-actor-id", "private-account-id", "private-filename.png", "private-matched-filename.png", "inconclusive-private-image", "decoded-private-image"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("private value escaped logs: %q", secret)
		}
	}
}

func TestInvestigationTelemetryDoesNotReportFailedSourceAsNotLocated(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sourceHash := sha256.Sum256([]byte("source"))
	scope := CandidateScope{IssueID: uuid.NewString(), Locale: "zh-Hant", SourceAssetID: uuid.NewString(), Revision: 1, SourceSHA256: hex.EncodeToString(sourceHash[:]), RendererVersion: "test-pdf-invisible", ModelSHA256: strings.Repeat("c", 64)}
	receipts := []Receipt{{ID: "a"}, {ID: "b"}}
	job := Investigation{ID: uuid.NewString(), Scope: scope, Evidence: decoderImage("image")}
	repo := &workerRepositoryStub{job: job, candidates: receipts}
	if err := investigationRunner(t, false).ProcessInvestigation(context.Background(), repo, &workerAssetsStub{err: errors.New("unavailable")}, job.ID); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	if !strings.Contains(logs, `"status":"source_unavailable"`) || strings.Contains(logs, `"status":"source_not_located"`) {
		t.Fatalf("misleading source status:\n%s", logs)
	}
}

func TestInvestigationRequiresOneUniqueSourceScope(t *testing.T) {
	runner := investigationRunner(t, false)
	sourceHash := sha256.Sum256([]byte("source"))
	base := CandidateScope{IssueID: uuid.NewString(), Locale: "zh-Hant", Revision: 1, SourceAssetID: uuid.NewString(), SourceSHA256: hex.EncodeToString(sourceHash[:]), RendererVersion: "test-pdf-invisible", ModelSHA256: strings.Repeat("c", 64)}
	other := base
	other.Revision = 2
	receipts := []Receipt{{ID: "a"}, {ID: "b"}}
	assets := &workerAssetsStub{body: &workerBody{Reader: strings.NewReader("source")}}
	completion := runner.investigate(context.Background(), &workerRepositoryStub{candidates: receipts}, assets, Investigation{Scope: base, Scopes: []CandidateScope{base, other}, Evidence: decoderImage("image")})
	if completion.Outcome != "inconclusive" || completion.ReasonCode != "source_scope_ambiguous" || assets.calls != 1 {
		t.Fatalf("completion=%+v assetCalls=%d", completion, assets.calls)
	}
}
