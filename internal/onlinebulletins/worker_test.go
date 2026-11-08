package onlinebulletins

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type extractionStoreStub struct {
	layoutJobs   []LayoutJob
	layoutFailed []string
	layoutIssues [][]ReviewIssue
	jobs         []ExtractionJob
	saved        []DocumentCandidate
	issues       [][]ReviewIssue
	failed       []string
	err          error
}

func (s *extractionStoreStub) ClaimOnlineBulletinLayout(context.Context, time.Time, time.Duration, int) (*LayoutJob, error) {
	if len(s.layoutJobs) == 0 {
		return nil, nil
	}
	job := s.layoutJobs[0]
	s.layoutJobs = s.layoutJobs[1:]
	job.LeaseUntil = time.Now().Add(120 * time.Second)
	return &job, nil
}
func (s *extractionStoreStub) CompleteOnlineBulletinLayout(context.Context, LayoutJob, LayoutResult, time.Time) error {
	return s.err
}
func (s *extractionStoreStub) FailOnlineBulletinLayout(_ context.Context, _ LayoutJob, code string, issues []ReviewIssue, _ time.Time, _ int) error {
	s.layoutFailed = append(s.layoutFailed, code)
	s.layoutIssues = append(s.layoutIssues, issues)
	return s.err
}

func TestWorkerRetainsLocatedLayoutFailures(t *testing.T) {
	doc := validDocument(4)
	issue := ReviewIssue{Code: "layout_validation_failed", ComponentID: doc.LayoutManifest.Pages[1].Slots[0].ComponentID, BlockID: doc.LayoutManifest.Pages[1].Slots[0].BlockID, SourcePage: 2, Blocking: true}
	store := &extractionStoreStub{layoutJobs: []LayoutJob{{Document: doc}}, jobs: []ExtractionJob{{ID: "job", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}}}
	fail := func(context.Context, Document, CanonicalMetadata) (LayoutResult, error) {
		return LayoutResult{}, &LayoutReviewError{Issues: []ReviewIssue{issue}}
	}
	w := ExtractionWorker{Store: store, BatchSize: 2, MaxAttempts: 3, Timeout: 120 * time.Second, Compose: fail, Validate: fail, Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
		return DocumentCandidate{Document: &doc}, nil, nil
	}}
	if err := w.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.layoutIssues) != 1 || len(store.layoutIssues[0]) != 1 || store.layoutIssues[0][0] != issue || len(store.issues) != 1 || len(store.issues[0]) != 1 || store.issues[0][0] != issue {
		t.Fatal("lost editable source location")
	}
}

func (s *extractionStoreStub) ClaimOnlineBulletinExtraction(context.Context, time.Time, time.Duration, int) (*ExtractionJob, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.jobs) == 0 {
		return nil, nil
	}
	job := s.jobs[0]
	job.LeaseUntil = time.Now().Add(120 * time.Second)
	s.jobs = s.jobs[1:]
	return &job, nil
}
func (s *extractionStoreStub) CompleteOnlineBulletinExtraction(_ context.Context, _ ExtractionJob, candidate DocumentCandidate, issues []ReviewIssue, _ time.Time) error {
	s.saved = append(s.saved, candidate)
	s.issues = append(s.issues, issues)
	return s.err
}
func (s *extractionStoreStub) FailOnlineBulletinExtraction(_ context.Context, _ ExtractionJob, code string, _ time.Time, _ int) error {
	s.failed = append(s.failed, code)
	return s.err
}

func TestExtractionBatchPreservesCandidatesWhenAIUnavailable(t *testing.T) {
	for _, classify := range []func(context.Context, []AmbiguousBlock) ([]Classification, error){nil, func(context.Context, []AmbiguousBlock) ([]Classification, error) { return nil, ErrClassification }} {
		store := &extractionStoreStub{jobs: []ExtractionJob{{ID: "job", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}}}
		doc := validDocument(4)
		doc.Components[1].BodySection.Kind = "unknown"
		worker := ExtractionWorker{Store: store, BatchSize: 1, MaxAttempts: 3, Timeout: 120 * time.Second, Classify: classify, Extract: func(ctx context.Context, job ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 90*time.Second {
				t.Fatal("processing leaves no cleanup budget")
			}
			return DocumentCandidate{Document: &doc}, []ReviewIssue{{Code: "unknown_body_kind", ComponentID: "body", Blocking: true}}, nil
		}}
		if err := worker.RunBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(store.saved) != 1 || store.saved[0].Document == nil || len(store.issues[0]) != 1 || store.issues[0][0].Code != "layout_validation_required" || store.saved[0].Document.Components[1].BodySection.Kind != "unknown" {
			t.Fatal("candidate discarded or unresolved review lost")
		}
	}
}

func TestExtractionWorkerRejectsVersionDriftBeforeReadingSource(t *testing.T) {
	for _, job := range []ExtractionJob{{TemplateVersion: "v1", ExtractorVersion: "old-parser"}, {TemplateVersion: "v2", ExtractorVersion: ExtractorVersion}} {
		store := &extractionStoreStub{jobs: []ExtractionJob{job}}
		calls := 0
		worker := ExtractionWorker{Store: store, BatchSize: 1, MaxAttempts: 3, Timeout: time.Minute, Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			calls++
			return DocumentCandidate{}, nil, nil
		}}
		if err := worker.RunBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls != 0 || len(store.saved) != 0 || len(store.failed) != 1 || store.failed[0] != "extractor_update_required" {
			t.Fatal("source parsed under a different extraction identity")
		}
	}
}

func TestExtractionBatchContainsDocumentPanicAndContinues(t *testing.T) {
	store := &extractionStoreStub{jobs: []ExtractionJob{{ID: "bad", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}, {ID: "good", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}, {ID: "later", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}}}
	worker := ExtractionWorker{Store: store, BatchSize: 2, MaxAttempts: 3, Timeout: 120 * time.Second, Extract: func(_ context.Context, job ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
		if job.ID == "bad" {
			panic("private PDF content must not become error text")
		}
		doc := validDocument(4)
		return DocumentCandidate{Document: &doc}, nil, nil
	}}
	if err := worker.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.failed) != 1 || store.failed[0] != "extractor_panic" || len(store.saved) != 1 || len(store.jobs) != 1 {
		t.Fatal("batch bound or panic containment failed")
	}
	store.err = errors.New("database unavailable")
	if err := worker.RunBatch(context.Background()); !errors.Is(err, store.err) {
		t.Fatal("infrastructure failure hidden")
	}
}

func TestExtractionCompositionFailureRetainsSourceCandidateForReview(t *testing.T) {
	store := &extractionStoreStub{jobs: []ExtractionJob{{ID: "job", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}}}
	doc := validDocument(4)
	worker := ExtractionWorker{Store: store, BatchSize: 1, MaxAttempts: 3, Timeout: 120 * time.Second,
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{Document: &doc}, nil, nil
		},
		Compose: func(context.Context, Document, CanonicalMetadata) (LayoutResult, error) {
			return LayoutResult{}, ErrLayoutRunner
		},
	}
	if err := worker.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.failed) != 0 || len(store.saved) != 1 || store.saved[0].Document.Components[0].ID != "cover" || len(store.issues[0]) != 1 || store.issues[0][0].Code != "layout_validation_failed" || !store.issues[0][0].Blocking {
		t.Fatal("layout failure lost the editable candidate or review gate")
	}
}

func TestExtractionAndLayoutShareOneBoundedSequentialBatch(t *testing.T) {
	store := &extractionStoreStub{layoutJobs: []LayoutJob{{ID: "layout"}}, jobs: []ExtractionJob{{ID: "extract", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}, {ID: "later", TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}}}
	worker := ExtractionWorker{Store: store, BatchSize: 2, MaxAttempts: 3, Timeout: 120 * time.Second,
		Validate: func(context.Context, Document, CanonicalMetadata) (LayoutResult, error) { panic("private diagnostics") },
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{}, nil, nil
		},
	}
	if err := worker.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.layoutFailed) != 1 || store.layoutFailed[0] != "layout_validation_failed" || len(store.saved) != 1 || len(store.jobs) != 1 {
		t.Fatal("layout jobs bypassed batch limit, panic containment, or persistence")
	}
}

func TestFullRecognitionSkipsLegacyLearningClassificationAndDuplicateLayout(t *testing.T) {
	document := validDocument(4)
	document.LayoutManifest.RendererVersion, document.LayoutManifest.RendererArtifactSha256 = "v11", RendererV11SHA256
	snapshot := ConversionTemplateSettings()
	document.TemplateSnapshot = &snapshot
	evidence := &RecognitionEvidence{ProtocolSHA256: RecognitionProtocolSHA256(), Deployment: "hhc-bulletin-ai", ModelVersion: "2026-10-26", SourceSHA256: document.SourceAssetChecksum, Pages: []RecognitionPageEvidence{}}
	for page := 1; page <= 4; page++ {
		evidence.Pages = append(evidence.Pages, RecognitionPageEvidence{Page: page, InputSHA256: strings.Repeat("b", 64), SemanticSHA256: strings.Repeat("d", 64), Usage: RecognitionUsage{Model: "hhc-bulletin-ai", Requests: 2}})
	}
	worker := ExtractionWorker{
		Recognize: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{Document: &document, Recognition: evidence}, nil, nil
		},
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			t.Error("legacy extraction called")
			return DocumentCandidate{}, nil, nil
		},
		Classify: func(context.Context, []AmbiguousBlock) ([]Classification, error) {
			t.Error("duplicate classification called")
			return nil, nil
		},
		Compose: func(context.Context, Document, CanonicalMetadata) (LayoutResult, error) {
			t.Error("duplicate layout before persistence")
			return LayoutResult{}, nil
		},
	}
	job := ExtractionJob{TemplateVersion: "v1", ExtractorVersion: RecognitionExtractorVersion(), SourceAssetChecksum: document.SourceAssetChecksum}
	candidate, issues, err := worker.process(context.Background(), job)
	if err != nil || candidate.Document == nil || len(issues) != 1 || issues[0].Code != "layout_validation_required" {
		t.Fatal("recognition did not queue one normal layout gate", issues, err)
	}
}
