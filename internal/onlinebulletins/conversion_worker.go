package onlinebulletins

import (
	"context"
	"errors"
	"time"
)

var ErrConversionStale = errors.New("conversion_source_changed")

type ConversionStore interface {
	ClaimOnlineBulletinConversion(context.Context, time.Time, time.Duration, int) (*ConversionJob, error)
	CompleteOnlineBulletinConversion(context.Context, ConversionJob, Document, CanonicalMetadata, time.Time) error
	FailOnlineBulletinConversion(context.Context, ConversionJob, string, []ReviewIssue, time.Time, int) error
}

func (w ExtractionWorker) runConversion(parent context.Context, store ConversionStore, job ConversionJob) error {
	timeout := w.Timeout
	if w.AssessDerived != nil && job.PublicationEventID != "" {
		timeout = UpdateJobTimeout
	}
	deadline := documentDeadline(parent, job.LeaseUntil, timeout)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	processing, stop := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
	document, metadata, err := func() (document Document, metadata CanonicalMetadata, err error) {
		defer func() {
			if recover() != nil {
				document = Document{}
				metadata = CanonicalMetadata{}
				err = ErrSimplifiedConversion
			}
		}()
		if job.ConverterVersion != SimplifiedConverterVersion {
			return document, metadata, errExtractorVersion
		}
		if err := processing.Err(); err != nil {
			return document, metadata, err
		}
		document, metadata, err = w.Convert(processing, job)
		if err == nil && w.AssessDerived != nil && job.PublicationEventID != "" {
			err = w.AssessDerived(processing, job, document, metadata)
		}
		return document, metadata, err
	}()
	stop()
	if err != nil {
		code := "simplified_conversion_failed"
		attempts := w.MaxAttempts
		if errors.Is(err, ErrUpdateSource) {
			code = "derived_content_not_ready"
			attempts = 1
		}
		if errors.Is(err, errExtractorVersion) {
			code = "extractor_update_required"
		}
		var located *LayoutReviewError
		var issues []ReviewIssue
		if errors.As(err, &located) {
			issues = located.Issues
		}
		return store.FailOnlineBulletinConversion(ctx, job, code, issues, time.Now().UTC(), attempts)
	}
	err = store.CompleteOnlineBulletinConversion(ctx, job, document, metadata, time.Now().UTC())
	if errors.Is(err, ErrConversionStale) {
		return store.FailOnlineBulletinConversion(ctx, job, "conversion_source_changed", nil, time.Now().UTC(), 1)
	}
	return err
}
