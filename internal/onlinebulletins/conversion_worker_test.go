package onlinebulletins

import (
	"context"
	"errors"
	"testing"
	"time"
)

type conversionStoreStub struct {
	extractionStoreStub
	conversions      []ConversionJob
	conversionSaved  int
	conversionFailed []string
	conversionIssues [][]ReviewIssue
}

func (s *conversionStoreStub) ClaimOnlineBulletinConversion(context.Context, time.Time, time.Duration, int) (*ConversionJob, error) {
	if len(s.conversions) == 0 {
		return nil, nil
	}
	job := s.conversions[0]
	s.conversions = s.conversions[1:]
	job.LeaseUntil = time.Now().Add(120 * time.Second)
	return &job, nil
}
func (s *conversionStoreStub) CompleteOnlineBulletinConversion(context.Context, ConversionJob, Document, CanonicalMetadata, time.Time) error {
	s.conversionSaved++
	return nil
}
func (s *conversionStoreStub) FailOnlineBulletinConversion(_ context.Context, _ ConversionJob, code string, issues []ReviewIssue, _ time.Time, _ int) error {
	s.conversionFailed = append(s.conversionFailed, code)
	s.conversionIssues = append(s.conversionIssues, issues)
	return nil
}

func TestSimplifiedWorkerPreservesLayoutDiagnostics(t *testing.T) {
	store := &conversionStoreStub{conversions: []ConversionJob{{ConverterVersion: SimplifiedConverterVersion}}}
	issue := ReviewIssue{Code: "layout_validation_failed", SourcePage: 2, BlockID: "title", Blocking: true}
	worker := ExtractionWorker{Store: store, Timeout: 120 * time.Second, BatchSize: 1, MaxAttempts: 3, Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
		panic("unexpected extraction")
	}, Convert: func(context.Context, ConversionJob) (Document, CanonicalMetadata, error) {
		return Document{}, CanonicalMetadata{}, &LayoutReviewError{Issues: []ReviewIssue{issue}}
	}}
	if err := worker.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.conversionSaved != 0 || len(store.conversionIssues) != 1 || len(store.conversionIssues[0]) != 1 || store.conversionIssues[0][0] != issue {
		t.Fatal("conversion lost layout findings or saved failed result")
	}
}

func TestSimplifiedWorkerSharesBatchAndContainsFailure(t *testing.T) {
	store := &conversionStoreStub{conversions: []ConversionJob{{ID: "bad", ConverterVersion: SimplifiedConverterVersion}, {ID: "good", ConverterVersion: SimplifiedConverterVersion}, {ID: "later", ConverterVersion: SimplifiedConverterVersion}}}
	worker := ExtractionWorker{Store: store, Timeout: 120 * time.Second, BatchSize: 2, MaxAttempts: 3,
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{}, nil, errors.New("unexpected PDF extraction")
		},
		Convert: func(ctx context.Context, job ConversionJob) (Document, CanonicalMetadata, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 110*time.Second {
				t.Fatal("missing bounded deadline")
			}
			if job.ID == "bad" {
				panic("private text")
			}
			return Document{}, CanonicalMetadata{}, nil
		}}
	if err := worker.RunBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.conversionSaved != 1 || len(store.conversionFailed) != 1 || store.conversionFailed[0] != "simplified_conversion_failed" || len(store.conversions) != 1 {
		t.Fatal("conversion batch did not safely advance", store)
	}
}
