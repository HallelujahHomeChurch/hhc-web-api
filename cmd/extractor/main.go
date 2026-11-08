// Bounded scheduled extraction with an explicit single-source diagnostic mode.
// This process never loads the public API server configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

// Linked from the exact shared renderer artifact during the isolated build.
var rendererArtifactSHA256 string

const rendererV1ArtifactSHA256 = "22f3015ecfdcf197690bb503e9467dc7515ddfd1bdb6af89ff19462a2211059f"

const rendererV2ArtifactSHA256 = "64b9e00032faac8c0d934ea115e9dadc2526c91ee082fa1546fc881226ae999f"
const rendererV4ArtifactSHA256 = "f654f3d546724d50368832a7a2999a445b60b9b9c9bd350b43411f9f522c72c3"
const rendererV3ArtifactSHA256 = "ebbdad08a61f1a2bb824e4cc99e6a091a1be38351cfd5b4f0763c5a324bd9d5e"
const rendererV5ArtifactSHA256 = "e819aeda4e7241a785ca60a6da3f5a8f2786be0b370a6e867b5025ef5f714f53"
const rendererV6ArtifactSHA256 = "142cdc601bfb33e2715b46efea7823bac8ee89b1b9620f5186599e34a4363d27"
const rendererV7ArtifactSHA256 = "cc55a5bd97412c64f90205f1cf8d9c27530d9fb2fedf2cd5b2e23a2435152c85"
const rendererV8ArtifactSHA256 = "646c849b47e87cc3607abccd3e92ad60a432bc55d8b1835f97c5b34ca2793c66"
const rendererV9ArtifactSHA256 = "e38c5bb6fea134ffdf56cbc542f5c20a9a82c8e03b7a5653ebf0eabfd7bb0997"
const templateV2ManifestSHA256 = "27382983b5b4e1025030048f15b81f5f22514b0d4ada267d5be5145530f095e5"

// Pin the renderer image's immutable manifest, not the independently deployed website.
const templateManifestSHA256 = "983f2545fd63ad9afe04c9cde5c3456c76934d2aa08a81050595e8f909f6120f"
const templateManifestFile = "/opt/bulletin-renderer/packages/ui/src/bulletin-reader/v7/template-assets.json"
const templateV2ManifestFile = "/opt/bulletin-renderer/packages/ui/src/bulletin-reader/v2/template-assets.json"

var errInvalidJob = errors.New("invalid_extractor_job")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		// Never log source paths, extracted text, canonical metadata or credentials.
		code := "bulletin_extraction_failed"
		if errors.Is(err, errInvalidJob) {
			code = "invalid_extractor_job"
		} else if errors.Is(err, onlinebulletins.ErrTemplateAssets) {
			code = "template_assets_unavailable"
		}
		fmt.Fprintln(os.Stderr, code)
		os.Exit(1)
	}
}

type diagnosticInput struct {
	onlinebulletins.CanonicalIssue
	TemplateSnapshot *onlinebulletins.TemplateSettings `json:"templateSnapshot,omitempty"`
}

func decodeCanonical(input io.Reader) (diagnosticInput, error) {
	var canonical diagnosticInput
	data, err := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || !utf8.Valid(data) {
		return canonical, errInvalidJob
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&canonical) != nil || decoder.Decode(new(any)) != io.EOF {
		return canonical, errInvalidJob
	}
	metadata := canonical.Metadata
	date, dateError := time.Parse("2006-01-02", metadata.Date)
	if uuid.Validate(canonical.IssueID) != nil || canonical.Series != "general" || canonical.ContentLocale != "zh-Hant" || dateError != nil || date.Format("2006-01-02") != metadata.Date || metadata.IssueNumber < 1 || strings.TrimSpace(metadata.Title) == "" || !utf8.ValidString(metadata.Title) || !utf8.ValidString(metadata.Subtitle) || utf8.RuneCountInString(metadata.Title) > 4096 || utf8.RuneCountInString(metadata.Subtitle) > 4096 {
		return canonical, errInvalidJob
	}
	if snapshot := canonical.TemplateSnapshot; snapshot != nil {
		text, valid := onlinebulletins.NormalizeTemplateText(snapshot.TemplateText)
		if metadata.IssueNumber < 1739 || snapshot.Version < 1 || snapshot.Version > 9007199254740991 || !valid || text != snapshot.TemplateText {
			return canonical, errInvalidJob
		}
	}
	return canonical, nil
}

func run(parent context.Context, args []string, input io.Reader, output io.Writer) error {
	return runWithBatch(parent, args, input, output, runBatch)
}

func runWithBatch(parent context.Context, args []string, input io.Reader, output io.Writer, batch func(context.Context) error) error {
	flags := flag.NewFlagSet("bulletin-extractor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "private temporary PDF path")
	version := flags.Bool("version", false, "print immutable runtime pins")
	verifyAssets := flags.Bool("verify-assets", false, "verify bundled template manifests without service access")
	wake := flags.Bool("wake", false, "enqueue a content-free wake for existing database work")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errInvalidJob
	}
	if *wake {
		if *source != "" || *version || *verifyAssets {
			return errInvalidJob
		}
		cfg, err := config.LoadExtractor()
		if err != nil {
			return errInvalidJob
		}
		if !cfg.Enabled {
			return nil
		}
		queue, err := onlinebulletins.NewWakeQueue(cfg.QueueURL, cfg.PoisonQueueURL, cfg.LLMEnabled)
		if err != nil {
			return errInvalidJob
		}
		return queue.Enqueue(parent, []byte(`{"version":1}`))
	}
	if *version {
		return json.NewEncoder(output).Encode(map[string]string{"poppler": onlinebulletins.PopplerVersion, "rendererArtifactSha256": rendererArtifactSHA256, "rendererV11ArtifactSha256": onlinebulletins.RendererV11SHA256, "rendererV8ArtifactSha256": rendererV8ArtifactSHA256, "templateManifestSha256": templateManifestSHA256, "rendererV2ArtifactSha256": rendererV2ArtifactSHA256, "templateV2ManifestSha256": templateV2ManifestSHA256})
	}
	if *verifyAssets {
		if _, err := onlinebulletins.LoadBundledTemplateAssets(templateManifestFile, templateManifestSHA256); err != nil {
			return err
		}
		_, err := onlinebulletins.LoadBundledTemplateAssets(templateV2ManifestFile, templateV2ManifestSHA256)
		return err
	}
	if *source == "" {
		// Cover the entire PG-owning batch, including lazy connect and claims.
		budget := 120 * time.Second
		if strings.EqualFold(strings.TrimSpace(os.Getenv("BULLETIN_LLM_EXTRACTION_ENABLED")), "true") {
			budget = onlinebulletins.RecognitionJobTimeout + 30*time.Second
		}
		ctx, cancel := context.WithTimeout(parent, budget)
		defer cancel()
		return batch(ctx)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(rendererArtifactSHA256) {
		return errInvalidJob
	}
	canonical, err := decodeCanonical(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	assets, err := onlinebulletins.LoadBundledTemplateAssets(templateManifestFile, templateManifestSHA256)
	if err != nil {
		return err
	}
	geometry, err := onlinebulletins.ParsePDFContext(ctx, *source)
	if err != nil {
		return err
	}
	candidate, issues, err := candidateForJob(geometry, canonical.CanonicalIssue, canonical.TemplateSnapshot, assets)
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Candidate onlinebulletins.DocumentCandidate `json:"candidate"`
		Issues    []onlinebulletins.ReviewIssue     `json:"issues"`
	}{candidate, issues})
	if err != nil || len(data) > 16<<20 {
		return onlinebulletins.ErrGeometryLimits
	}
	data = append(data, '\n')
	n, err := output.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
