package onlinebulletins

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// Keep external work at the store/processor boundary; exercise the real batch.
type cancellationStore struct {
	conversionStoreStub
	kind                                       string
	claims                                     []string
	claim                                      func(context.Context) error
	finish                                     func(context.Context, string) error
	failedJobID, failedLeaseToken, failureCode string
}

func (s *cancellationStore) claimed(ctx context.Context, kind string) (bool, error) {
	s.claims = append(s.claims, kind)
	if kind != s.kind {
		return false, nil
	}
	if s.claim != nil {
		return false, s.claim(ctx)
	}
	return true, nil
}

func (s *cancellationStore) ClaimOnlineBulletinLayout(ctx context.Context, _ time.Time, _ time.Duration, _ int) (*LayoutJob, error) {
	ok, err := s.claimed(ctx, "layout")
	if !ok {
		return nil, err
	}
	return &LayoutJob{LeaseUntil: time.Now().Add(time.Minute)}, nil
}
func (s *cancellationStore) ClaimOnlineBulletinConversion(ctx context.Context, _ time.Time, _ time.Duration, _ int) (*ConversionJob, error) {
	ok, err := s.claimed(ctx, "conversion")
	if !ok {
		return nil, err
	}
	return &ConversionJob{LeaseUntil: time.Now().Add(time.Minute), ConverterVersion: SimplifiedConverterVersion}, nil
}
func (s *cancellationStore) ClaimOnlineBulletinExtraction(ctx context.Context, _ time.Time, _ time.Duration, _ int) (*ExtractionJob, error) {
	ok, err := s.claimed(ctx, "extraction")
	if !ok {
		return nil, err
	}
	return &ExtractionJob{LeaseUntil: time.Now().Add(time.Minute), TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}, nil
}
func (s *cancellationStore) CompleteOnlineBulletinLayout(ctx context.Context, _ LayoutJob, _ LayoutResult, _ time.Time) error {
	return s.finish(ctx, "complete")
}
func (s *cancellationStore) FailOnlineBulletinLayout(ctx context.Context, job LayoutJob, code string, _ []ReviewIssue, _ time.Time, _ int) error {
	s.failedJobID, s.failedLeaseToken, s.failureCode = job.ID, job.LeaseToken, code
	return s.finish(ctx, "fail")
}
func (s *cancellationStore) CompleteOnlineBulletinConversion(ctx context.Context, _ ConversionJob, _ Document, _ CanonicalMetadata, _ time.Time) error {
	return s.finish(ctx, "complete")
}
func (s *cancellationStore) FailOnlineBulletinConversion(ctx context.Context, job ConversionJob, code string, _ []ReviewIssue, _ time.Time, _ int) error {
	s.failedJobID, s.failedLeaseToken, s.failureCode = job.ID, job.LeaseToken, code
	return s.finish(ctx, "fail")
}
func (s *cancellationStore) CompleteOnlineBulletinExtraction(ctx context.Context, _ ExtractionJob, _ DocumentCandidate, _ []ReviewIssue, _ time.Time) error {
	return s.finish(ctx, "complete")
}
func (s *cancellationStore) FailOnlineBulletinExtraction(ctx context.Context, job ExtractionJob, code string, _ time.Time, _ int) error {
	s.failedJobID, s.failedLeaseToken, s.failureCode = job.ID, job.LeaseToken, code
	return s.finish(ctx, "fail")
}

func TestExtractionBatchParentCancellation(t *testing.T) {
	for _, kind := range []string{"layout", "conversion", "extraction"} {
		for _, phase := range []string{"before_claim", "claim", "processing", "complete", "fail"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				awaitCancellation := func(ctx context.Context) error {
					cancel()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(2 * time.Second):
						t.Fatal("work detached from canceled parent")
						return nil
					}
				}
				store := &cancellationStore{kind: kind}
				if phase == "before_claim" {
					cancel()
				}
				if phase == "claim" {
					store.claim = awaitCancellation
				}
				process := func(ctx context.Context) error {
					if phase == "processing" {
						return awaitCancellation(ctx)
					}
					if phase == "fail" {
						return errors.New("processing failed")
					}
					return nil
				}
				finished := 0
				store.finish = func(ctx context.Context, action string) error {
					finished++
					want := "complete"
					if phase == "processing" || phase == "fail" {
						want = "fail"
					}
					if action != want {
						t.Fatalf("persistence=%s, want %s", action, want)
					}
					if phase == "processing" {
						if !errors.Is(ctx.Err(), context.Canceled) {
							t.Fatal("persistence detached from canceled parent")
						}
						return nil
					}
					if err := ctx.Err(); err != nil {
						t.Fatalf("processing cleanup canceled persistence: %v", err)
					}
					return awaitCancellation(ctx)
				}
				worker := ExtractionWorker{Store: store, BatchSize: 2, MaxAttempts: 3, Timeout: time.Minute,
					Extract: func(ctx context.Context, _ ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
						return DocumentCandidate{}, nil, process(ctx)
					},
					Validate: func(ctx context.Context, _ Document, _ CanonicalMetadata) (LayoutResult, error) {
						return LayoutResult{}, process(ctx)
					},
					Convert: func(ctx context.Context, _ ConversionJob) (Document, CanonicalMetadata, error) {
						return Document{}, CanonicalMetadata{}, process(ctx)
					},
				}
				if err := worker.RunBatch(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("batch error=%v, want canceled", err)
				}
				var wantClaims []string
				wantFinished := 0
				if phase != "before_claim" {
					wantClaims = []string{"layout"}
					if kind != "layout" {
						wantClaims = append(wantClaims, "conversion")
					}
					if kind == "extraction" {
						wantClaims = append(wantClaims, "extraction")
					}
					if phase != "claim" {
						wantFinished = 1
					}
				}
				if !reflect.DeepEqual(store.claims, wantClaims) || finished != wantFinished {
					t.Fatalf("claims=%v, persistence calls=%d; want %v, %d", store.claims, finished, wantClaims, wantFinished)
				}
			})
		}
	}
}

func TestExtractionDocumentDeadlineReserve(t *testing.T) {
	for _, kind := range []string{"layout", "conversion", "extraction"} {
		for _, tc := range []struct {
			name                          string
			parent, lease, timeout, final time.Duration
		}{
			{"parent_first", 60 * time.Second, 110 * time.Second, 120 * time.Second, 60 * time.Second},
			{"lease_first", 100 * time.Second, 50 * time.Second, 120 * time.Second, 50 * time.Second},
			{"timeout_first", 200 * time.Second, 200 * time.Second, 60 * time.Second, 60 * time.Second},
			{"no_parent_deadline", 0, 100 * time.Second, 80 * time.Second, 80 * time.Second},
			{"expired_processing", 5 * time.Second, 100 * time.Second, 60 * time.Second, 5 * time.Second},
		} {
			for _, fail := range []bool{false, true} {
				t.Run(kind+"/"+tc.name+"/"+map[bool]string{false: "complete", true: "fail"}[fail], func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						now := time.Now()
						parent := context.Background()
						if tc.parent > 0 {
							var cancel context.CancelFunc
							parent, cancel = context.WithTimeout(parent, tc.parent)
							defer cancel()
						}
						reserve := 10 * time.Second
						if kind == "extraction" {
							reserve = 30 * time.Second
						}
						deadline := now.Add(tc.final)
						expired := tc.final <= reserve
						calls, finishes := 0, 0
						var processing context.Context
						process := func(ctx context.Context) error {
							calls++
							processing = ctx
							got, ok := ctx.Deadline()
							if !ok || !got.Equal(deadline.Add(-reserve)) {
								t.Errorf("processing deadline=%v, want %v", got, deadline.Add(-reserve))
							}
							if expired {
								t.Error("expired processing invoked external callback")
							}
							if fail {
								return errors.New("processor_failed")
							}
							return nil
						}
						store := &cancellationStore{}
						store.finish = func(ctx context.Context, action string) error {
							finishes++
							want := "complete"
							if fail || expired {
								want = "fail"
							}
							if action != want {
								t.Errorf("persistence=%s, want %s", action, want)
							}
							got, ok := ctx.Deadline()
							if !ok || !got.Equal(deadline) {
								t.Errorf("final deadline=%v, want %v", got, deadline)
							}
							if ctx.Err() != nil {
								t.Error("processing context cancellation canceled final persistence")
							}
							if processing != nil && processing.Err() == nil {
								t.Error("processing context not explicitly canceled before persistence")
							}
							if action == "fail" && (store.failedJobID != "opaque-job" || store.failedLeaseToken != "opaque-lease") {
								t.Error("failure lost original job/lease fence")
							}
							return nil
						}
						worker := ExtractionWorker{Store: store, Timeout: tc.timeout,
							Validate: func(ctx context.Context, _ Document, _ CanonicalMetadata) (LayoutResult, error) {
								return LayoutResult{}, process(ctx)
							},
							Extract: func(ctx context.Context, _ ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
								return DocumentCandidate{}, nil, process(ctx)
							},
							Convert: func(ctx context.Context, _ ConversionJob) (Document, CanonicalMetadata, error) {
								return Document{}, CanonicalMetadata{}, process(ctx)
							},
						}
						var err error
						lease := now.Add(tc.lease)
						switch kind {
						case "layout":
							err = worker.runLayout(parent, LayoutJob{ID: "opaque-job", LeaseToken: "opaque-lease", LeaseUntil: lease})
						case "conversion":
							err = worker.runConversion(parent, store, ConversionJob{ID: "opaque-job", LeaseToken: "opaque-lease", LeaseUntil: lease, ConverterVersion: SimplifiedConverterVersion})
						case "extraction":
							err = worker.runJob(parent, ExtractionJob{ID: "opaque-job", LeaseToken: "opaque-lease", LeaseUntil: lease, TemplateVersion: "v1", ExtractorVersion: ExtractorVersion})
						}
						if err != nil {
							t.Fatal(err)
						}
						wantCalls := 1
						if expired {
							wantCalls = 0
						}
						if calls != wantCalls || finishes != 1 {
							t.Fatalf("processor calls=%d/final calls=%d, want %d/1", calls, finishes, wantCalls)
						}
						if expired && kind == "extraction" && store.failureCode != "extraction_timeout" {
							t.Errorf("expired extraction code=%s", store.failureCode)
						}
					})
				})
			}
		}
	}
}

func TestExtractionDeadlineWaitsForDetachedCleanup(t *testing.T) {
	for _, earlyStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "processing_deadline", true: "parent_cancel"}[earlyStop], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				parent, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				store := &cancellationStore{kind: "extraction"}
				finished := false
				store.finish = func(ctx context.Context, action string) error {
					finished = true
					if action != "fail" {
						t.Fatal("cleanup timeout fabricated successful persistence")
					}
					if earlyStop {
						if !errors.Is(ctx.Err(), context.Canceled) {
							t.Fatal("final persistence detached from stopped parent")
						}
						return ctx.Err()
					}
					if ctx.Err() != nil {
						t.Fatal("reserved final persistence already expired")
					}
					return nil
				}
				worker := ExtractionWorker{Store: store, BatchSize: 2, MaxAttempts: 3, Timeout: 120 * time.Second,
					Extract: func(ctx context.Context, _ ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
						<-ctx.Done()
						cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
						defer stop()
						if cleanup.Err() != nil {
							t.Fatal("detached cleanup inherited cancellation")
						}
						<-cleanup.Done()
						if finished {
							t.Fatal("persistence ran before actual cleanup settled")
						}
						return DocumentCandidate{}, nil, ctx.Err()
					},
				}
				if earlyStop {
					go func() { time.Sleep(2 * time.Second); cancel() }()
				}
				job := ExtractionJob{ID: "opaque-job", LeaseToken: "opaque-lease", LeaseUntil: started.Add(120 * time.Second), TemplateVersion: "v1", ExtractorVersion: ExtractorVersion}
				err := worker.runJob(parent, job)
				wantElapsed := 50 * time.Second
				if earlyStop {
					wantElapsed = 22 * time.Second
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !finished || time.Since(started) != wantElapsed {
					t.Fatalf("actual cleanup elapsed=%v, want %v", time.Since(started), wantElapsed)
				}
			})
		})
	}
}
