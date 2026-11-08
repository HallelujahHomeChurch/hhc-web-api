package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestExtractorAssetPreflightLoadsBundledAssetsWithoutServiceConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("BULLETIN_EXTRACTOR_ASSET_ORIGIN", "https://unavailable.invalid")
	var output bytes.Buffer
	err := run(context.Background(), []string{"-verify-assets"}, strings.NewReader(""), &output)
	// Outside the immutable image, missing bundled files must fail closed rather
	// than falling back to a mutable website manifest or loading the database.
	if !errors.Is(err, onlinebulletins.ErrTemplateAssets) || output.Len() != 0 {
		t.Fatalf("expected missing bundled assets, got %v / %q", err, output.String())
	}
}

func TestLayoutRunnerKeepsFrozenVersionRouting(t *testing.T) {
	previous := rendererArtifactSHA256
	rendererArtifactSHA256 = strings.Repeat("a", 64)
	t.Cleanup(func() { rendererArtifactSHA256 = previous })
	for _, version := range []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10", "v11"} {
		runner, err := layoutRunner(version)
		if err != nil {
			t.Fatal(err)
		}
		want := "/opt/bulletin-renderer/scripts/measure-bulletin-layout-" + version + ".mjs"
		if version == "v1" {
			want = "/opt/bulletin-renderer/scripts/measure-bulletin-layout.mjs"
		}
		if runner.ScriptPath != want {
			t.Fatal("old document routed to a different compositor", version)
		}
		if version == "v3" && runner.RendererArtifactSHA256 != "ebbdad08a61f1a2bb824e4cc99e6a091a1be38351cfd5b4f0763c5a324bd9d5e" {
			t.Fatal("frozen V3 digest lost")
		}
		if version == "v4" && runner.RendererArtifactSHA256 != "f654f3d546724d50368832a7a2999a445b60b9b9c9bd350b43411f9f522c72c3" {
			t.Fatal("frozen V4 digest lost")
		}
		if version == "v5" && runner.RendererArtifactSHA256 != "e819aeda4e7241a785ca60a6da3f5a8f2786be0b370a6e867b5025ef5f714f53" {
			t.Fatal("frozen V5 digest lost")
		}
		if version == "v6" && runner.RendererArtifactSHA256 != "142cdc601bfb33e2715b46efea7823bac8ee89b1b9620f5186599e34a4363d27" {
			t.Fatal("frozen V6 digest lost")
		}
		if version == "v7" && runner.RendererArtifactSHA256 != "cc55a5bd97412c64f90205f1cf8d9c27530d9fb2fedf2cd5b2e23a2435152c85" {
			t.Fatal("frozen V7 digest lost")
		}
		if version == "v8" && runner.RendererArtifactSHA256 != "646c849b47e87cc3607abccd3e92ad60a432bc55d8b1835f97c5b34ca2793c66" {
			t.Fatal("frozen V8 digest lost")
		}
		if version == "v9" && runner.RendererArtifactSHA256 != "e38c5bb6fea134ffdf56cbc542f5c20a9a82c8e03b7a5653ebf0eabfd7bb0997" {
			t.Fatal("frozen V9 digest lost")
		}
		if version == "v10" && runner.RendererArtifactSHA256 != rendererArtifactSHA256 {
			t.Fatal("V10 must use the linked artifact")
		}
	}
	if _, err := layoutRunner("v999"); err == nil {
		t.Fatal("unsupported renderer silently upgraded")
	}
}

func TestExtractorAcceptsExplicitSnapshotWithoutInferringCurrentSettings(t *testing.T) {
	input := `{"issueId":"b08a80e8-6388-4166-9a2a-23d336f74b7a","series":"general","contentLocale":"zh-Hant","metadata":{"title":"主題","subtitle":"副標","issueNumber":1739,"date":"2026-09-20"},"templateSnapshot":{"version":7,"visionMission":"異象","visionFellowship":"團契","visionCommitment":"理想"}}`
	if _, err := decodeCanonical(strings.NewReader(input)); err != nil {
		t.Fatal("explicit diagnostic snapshot rejected", err)
	}
	for _, invalid := range []string{strings.Replace(input, `"version":7`, `"version":0`, 1), strings.Replace(input, `"version":7`, `"version":9007199254740992`, 1), strings.Replace(input, `"異象"`, `" 不正規 "`, 1), strings.Replace(input, `"issueNumber":1739`, `"issueNumber":1738`, 1)} {
		if _, err := decodeCanonical(strings.NewReader(invalid)); err == nil {
			t.Fatal("invalid snapshot job accepted")
		}
	}
}

func TestExtractorRejectsUnboundedOrUnknownCanonicalInput(t *testing.T) {
	for _, input := range []string{strings.Repeat(" ", (64<<10)+1), `{"externalScript":"https://foreign.example"}`, `{}`, `{"issueId":"invalid"} {}`, "{\"issueId\":\"\xff\"}"} {
		if _, err := decodeCanonical(strings.NewReader(input)); err == nil {
			t.Fatal("invalid canonical job input accepted")
		}
	}
	input := `{"issueId":"b08a80e8-6388-4166-9a2a-23d336f74b7a","series":"general","contentLocale":"zh-Hant","metadata":{"title":"主題","subtitle":"副標","issueNumber":1739,"date":"2026-09-20"}}`
	canonical, err := decodeCanonical(strings.NewReader(input))
	if err != nil || canonical.Metadata.Title != "主題" || canonical.ContentLocale != "zh-Hant" {
		t.Fatal("valid canonical input lost")
	}
}

func TestExtractorVersionIsReadOnlyAndInvalidFlagsHaveNoContentOutput(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-version"}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "25.03.0") {
		t.Fatal("pinned extractor version unavailable")
	}
	output.Reset()
	if err := run(context.Background(), []string{"-unknown"}, strings.NewReader(""), &output); err == nil || output.Len() != 0 {
		t.Fatal("invalid job printed input or started extraction")
	}
}

func TestDisabledScheduledExtractorDoesNotLoadServerOrCredentials(t *testing.T) {
	t.Setenv("BULLETIN_EXTRACTION_ENABLED", "false")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("APP_API_TOKEN", "")
	var output bytes.Buffer
	if err := run(context.Background(), nil, strings.NewReader("private data must not be read"), &output); err != nil || output.Len() != 0 {
		t.Fatal("disabled scheduled job did not exit quietly", err)
	}
}

func TestScheduledExtractorWorkContext(t *testing.T) {
	for _, parentLimit := range []time.Duration{0, 30 * time.Second, 300 * time.Second} {
		t.Run(parentLimit.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				parent := context.Background()
				if parentLimit > 0 {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, parentLimit)
					defer cancel()
				}
				want := 120 * time.Second
				if parentLimit > 0 && parentLimit < want {
					want = parentLimit
				}
				cleaned := false
				var returned error
				err := runWithBatch(parent, nil, strings.NewReader(""), io.Discard, func(ctx context.Context) error {
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(started.Add(want)) {
						t.Fatalf("whole work deadline=%v, want %v", deadline, started.Add(want))
					}
					// The injected batch owns its cleanup just as runBatch owns deferred DB.Close.
					defer func() { time.Sleep(10 * time.Second); cleaned = true }()
					<-ctx.Done()
					returned = errors.Join(ctx.Err(), errors.New("work_failed"))
					return returned
				})
				if err != returned || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("original work error lost: %v", err)
				}
				if !cleaned || time.Since(started) != want+10*time.Second {
					t.Fatal("command returned before actual owned cleanup")
				}
			})
		})
	}
}

func TestScheduledExtractorWorkContextCanceledOnReturn(t *testing.T) {
	var work context.Context
	err := runWithBatch(context.Background(), nil, strings.NewReader(""), io.Discard, func(ctx context.Context) error { work = ctx; return nil })
	if err != nil || work == nil || !errors.Is(work.Err(), context.Canceled) {
		t.Fatalf("whole work context not canceled on return: %v", err)
	}
}

func TestScheduledExtractorWorkContextKeepsSignalCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runWithBatch(parent, nil, strings.NewReader(""), io.Discard, func(ctx context.Context) error { cancel(); <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("scheduled work detached from signal parent", err)
	}
}

func TestScheduledExtractorSeamDoesNotStartDiagnosticBatch(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"-verify-assets"}, {"-unknown"}, {"-source", "fixture.pdf"}} {
		called := false
		_ = runWithBatch(context.Background(), args, strings.NewReader("{}"), io.Discard, func(context.Context) error { called = true; return nil })
		if called {
			t.Fatalf("diagnostic/invalid mode entered scheduled batch: %v", args)
		}
	}
}

func TestExtractorReleaseWakeIsDisabledWithoutServiceAccess(t *testing.T) {
	t.Setenv("BULLETIN_EXTRACTION_ENABLED", "false")
	if err := run(context.Background(), []string{"--wake"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--wake", "--version"}, {"--wake", "--source", "private.pdf"}} {
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard); !errors.Is(err, errInvalidJob) {
			t.Fatal("ambiguous release wake mode accepted", err)
		}
	}
}
