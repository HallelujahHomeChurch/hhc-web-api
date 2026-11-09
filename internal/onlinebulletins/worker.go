package onlinebulletins

import (
	"context"
	"errors"
	"time"
)

type ExtractionStore interface {
	ClaimOnlineBulletinLayout(context.Context, time.Time, time.Duration, int) (*LayoutJob, error)
	CompleteOnlineBulletinLayout(context.Context, LayoutJob, LayoutResult, time.Time) error
	FailOnlineBulletinLayout(context.Context, LayoutJob, string, []ReviewIssue, time.Time, int) error
	ClaimOnlineBulletinExtraction(context.Context, time.Time, time.Duration, int) (*ExtractionJob, error)
	CompleteOnlineBulletinExtraction(context.Context, ExtractionJob, DocumentCandidate, []ReviewIssue, time.Time) error
	FailOnlineBulletinExtraction(context.Context, ExtractionJob, string, time.Time, int) error
}

type ExtractionWorker struct {
	Store                  ExtractionStore
	AutoPublish            func(context.Context, time.Time) (bool, error)
	Update                 func(context.Context, UpdateJob) (UpdateEvidence, error)
	Extract                func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error)
	Recognize              func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error)
	Classify               func(context.Context, []AmbiguousBlock) ([]Classification, error)
	Compose                func(context.Context, Document, CanonicalMetadata) (LayoutResult, error)
	Validate               func(context.Context, Document, CanonicalMetadata) (LayoutResult, error)
	Convert                func(context.Context, ConversionJob) (Document, CanonicalMetadata, error)
	Timeout                time.Duration
	BatchSize, MaxAttempts int
}

var errExtractorPanic = errors.New("extractor_panic")
var errExtractorVersion = errors.New("extractor_update_required")

// RunBatch processes one document at a time. Extraction failures require manual
// retry; layout/conversion keep their existing retry policy. Store errors fail the run.
func (w ExtractionWorker) RunBatch(ctx context.Context) error {
	if w.Store == nil || w.Extract == nil || w.BatchSize < 1 || w.BatchSize > 10 || w.MaxAttempts < 1 || w.MaxAttempts > 10 || w.Timeout < 40*time.Second || w.Timeout > 120*time.Second {
		return ErrExtractionJob
	}
	if w.Recognize != nil && w.BatchSize != 1 {
		return ErrExtractionJob
	}
	for range w.BatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.AutoPublish != nil {
			published, err := w.AutoPublish(ctx, time.Now().UTC())
			if err != nil {
				return err
			}
			if published {
				continue
			}
		}
		if updates, ok := w.Store.(UpdateStore); ok && w.Update != nil {
			job, err := updates.ClaimOnlineBulletinUpdate(ctx, time.Now().UTC(), UpdateJobTimeout, w.MaxAttempts)
			if err != nil {
				return err
			}
			if job != nil {
				if err = w.runUpdate(ctx, updates, *job); err != nil && !errors.Is(err, ErrExtractionLease) {
					return err
				}
				continue
			}
		}
		layout, err := w.Store.ClaimOnlineBulletinLayout(ctx, time.Now().UTC(), w.Timeout, w.MaxAttempts)
		if err != nil {
			return err
		}
		if layout != nil {
			if err = w.runLayout(ctx, *layout); err != nil && !errors.Is(err, ErrLayoutLease) {
				return err
			}
			continue
		}
		if conversions, ok := w.Store.(ConversionStore); ok && w.Convert != nil {
			job, err := conversions.ClaimOnlineBulletinConversion(ctx, time.Now().UTC(), w.Timeout, w.MaxAttempts)
			if err != nil {
				return err
			}
			if job != nil {
				if err = w.runConversion(ctx, conversions, *job); err != nil && !errors.Is(err, ErrExtractionLease) {
					return err
				}
				continue
			}
		}
		lease := w.Timeout
		if w.Recognize != nil {
			lease = RecognitionJobTimeout
		}
		job, err := w.Store.ClaimOnlineBulletinExtraction(ctx, time.Now().UTC(), lease, w.MaxAttempts)
		if err != nil {
			return err
		}
		if job == nil {
			return nil
		}
		if err = w.runJob(ctx, *job); err != nil && !errors.Is(err, ErrExtractionLease) {
			return err
		}
	}
	return nil
}

func documentDeadline(parent context.Context, leaseUntil time.Time, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if leaseUntil.Before(deadline) {
		deadline = leaseUntil
	}
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return deadline
}

func (w ExtractionWorker) runLayout(parent context.Context, job LayoutJob) error {
	deadline := documentDeadline(parent, job.LeaseUntil, w.Timeout)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	processing, stop := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
	result, err := func() (result LayoutResult, err error) {
		defer func() {
			if recover() != nil {
				result = LayoutResult{}
				err = ErrLayoutRunner
			}
		}()
		if w.Validate == nil {
			return result, ErrLayoutRunner
		}
		if err := processing.Err(); err != nil {
			return result, err
		}
		return w.Validate(processing, job.Document, job.Canonical)
	}()
	stop()
	if err != nil {
		return w.Store.FailOnlineBulletinLayout(ctx, job, "layout_validation_failed", layoutReviewIssues(err), time.Now().UTC(), w.MaxAttempts)
	}
	return w.Store.CompleteOnlineBulletinLayout(ctx, job, result, time.Now().UTC())
}

func (w ExtractionWorker) runJob(parent context.Context, job ExtractionJob) error {
	timeout := w.Timeout
	if job.ExtractorVersion == RecognitionExtractorVersion() {
		timeout = RecognitionJobTimeout
	}
	deadline := documentDeadline(parent, job.LeaseUntil, timeout)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	// Reserve 20 seconds for the existing asset-grant revocation and 10 for
	// persistence within the lease. Only full recognition has the longer budget.
	processing, stop := context.WithDeadline(ctx, deadline.Add(-30*time.Second))
	candidate, issues, err := w.process(processing, job)
	stop()
	if err != nil {
		code := "extraction_failed"
		switch {
		case errors.Is(err, errExtractorVersion):
			code = "extractor_update_required"
		case errors.Is(err, errExtractorPanic):
			code = "extractor_panic"
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			code = "extraction_timeout"
		case errors.Is(err, ErrGeometryLimits):
			code = "source_limit"
		case errors.Is(err, ErrUnsupportedSource), errors.Is(err, ErrInvalidGeometry):
			code = "unsupported_source"
		case errors.Is(err, ErrTemplateAssets):
			code = "template_assets_unavailable"
		case errors.Is(err, ErrRecognition):
			code = "recognition_failed"
		}
		return w.Store.FailOnlineBulletinExtraction(ctx, job, code, time.Now().UTC(), w.MaxAttempts)
	}
	return w.Store.CompleteOnlineBulletinExtraction(ctx, job, candidate, issues, time.Now().UTC())
}

func (w ExtractionWorker) process(ctx context.Context, job ExtractionJob) (candidate DocumentCandidate, issues []ReviewIssue, err error) {
	fullRecognition := job.ExtractorVersion == RecognitionExtractorVersion()
	if job.TemplateVersion != "v1" || (!fullRecognition && job.ExtractorVersion != ExtractorVersion) {
		return candidate, nil, errExtractorVersion
	}
	defer func() {
		if recover() != nil {
			candidate = DocumentCandidate{}
			issues = nil
			err = errExtractorPanic
		}
	}()
	if err := ctx.Err(); err != nil {
		return candidate, nil, err
	}
	if fullRecognition {
		if w.Recognize == nil {
			return candidate, nil, errExtractorVersion
		}
		candidate, issues, err = w.Recognize(ctx, job)
		if err != nil {
			return candidate, issues, err
		}
		if candidate.Document == nil || !ValidRecognitionEvidence(candidate.Recognition, *candidate.Document) || candidate.Document.SourceAssetChecksum != job.SourceAssetChecksum {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		// Reconciliation changes IDs before persistence; measure the saved Local once.
		return candidate, append(issues, ReviewIssue{Code: "layout_validation_required", Blocking: true}), nil
	}
	candidate, issues, err = w.Extract(ctx, job)
	if err != nil {
		return candidate, issues, err
	}
	if candidate.Document == nil {
		return candidate, issues, nil
	}
	if job.Canonical.Metadata.IssueNumber >= 1739 {
		var metrics LearningMetrics
		candidate.LearningApplications, metrics, err = applyBodyKindRules(candidate.Document, candidate.Evidence, job.LearningRules)
		candidate.LearningMetrics = &metrics
		if err != nil {
			return candidate, issues, err
		}
	}
	unresolved := ClassifyBodySections(ctx, candidate.Document, w.Classify)
	kept := make([]ReviewIssue, 0, len(issues)+len(unresolved))
	for _, issue := range issues {
		if issue.Code != "unknown_body_kind" {
			kept = append(kept, issue)
		}
	}
	issues = append(kept, unresolved...)
	if w.Compose == nil {
		issues = append(issues, ReviewIssue{Code: "layout_validation_required", Blocking: true})
	} else if layout, layoutErr := w.Compose(ctx, *candidate.Document, job.Canonical.Metadata); layoutErr != nil {
		issues = append(issues, layoutReviewIssues(layoutErr)...)
	} else {
		candidate.Document = &layout.Document
	}
	return candidate, issues, nil
}

func layoutReviewIssues(err error) []ReviewIssue {
	var located *LayoutReviewError
	if errors.As(err, &located) && len(located.Issues) > 0 {
		return located.Issues
	}
	return []ReviewIssue{{Code: "layout_validation_failed", Blocking: true}}
}
