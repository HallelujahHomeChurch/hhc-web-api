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
	deadline := documentDeadline(parent, job.LeaseUntil, w.Timeout)
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
		return w.Convert(processing, job)
	}()
	stop()
	if err != nil {
		code := "simplified_conversion_failed"
		if errors.Is(err, errExtractorVersion) {
			code = "extractor_update_required"
		}
		var located *LayoutReviewError
		var issues []ReviewIssue
		if errors.As(err, &located) {
			issues = located.Issues
		}
		return store.FailOnlineBulletinConversion(ctx, job, code, issues, time.Now().UTC(), w.MaxAttempts)
	}
	err = store.CompleteOnlineBulletinConversion(ctx, job, document, metadata, time.Now().UTC())
	if errors.Is(err, ErrConversionStale) {
		return store.FailOnlineBulletinConversion(ctx, job, "conversion_source_changed", nil, time.Now().UTC(), 1)
	}
	return err
}
