// bulletin-eval prepares excluded-area-filtered inputs and records private,
// bounded recognition proposals. It never connects to the product database.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

var errEvaluation = errors.New("bulletin_evaluation_failed")

type evaluationBundle struct {
	Version int                               `json:"version"`
	Pages   []onlinebulletins.RecognitionPage `json:"pages"`
}

type evaluationReport struct {
	Version         int    `json:"version"`
	ProtocolSHA256  string `json:"protocolSha256"`
	Deployment      string `json:"deployment"`
	Requests        int    `json:"requests"`
	Pages           int    `json:"pages"`
	Passed          int    `json:"passed"`
	Failed          int    `json:"failed"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	ReasoningTokens int64  `json:"reasoningTokens"`
	VisualBlocks    int    `json:"visualBlocksRequiringReview"`
	UnassignedIDs   int    `json:"unassignedNativeIdsRequiringReview"`
}

type evaluationResult struct {
	Page         int                               `json:"page"`
	Repeat       int                               `json:"repeat"`
	SourceSHA256 string                            `json:"sourceSha256"`
	InputSHA256  string                            `json:"inputSha256"`
	Valid        bool                              `json:"valid"`
	DurationMS   int64                             `json:"durationMs"`
	Usage        onlinebulletins.RecognitionUsage  `json:"usage"`
	Proposal     onlinebulletins.RecognitionResult `json:"proposal"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv, nil, onlinebulletins.PrepareRecognitionSource); err != nil {
		fmt.Fprintln(os.Stderr, "bulletin_evaluation_failed")
		os.Exit(1)
	}
}

func run(parent context.Context, args []string, output io.Writer, getenv func(string) string, client *http.Client, prepare func(context.Context, string) ([]onlinebulletins.RecognitionPage, error)) error {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "run") {
		return errEvaluation
	}
	flags := flag.NewFlagSet("bulletin-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "private source PDF")
	bundlePath := flags.String("bundle", "", "private sanitized bundle")
	resultDir := flags.String("output", "", "new private result directory")
	repeat := flags.Int("repeat", 1, "repeat each page once or twice")
	verify := flags.Bool("verify", false, "independent source verification with bounded repair")
	canonicalPath := flags.String("canonical", "", "private canonical metadata JSON")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *bundlePath == "" || *repeat < 1 || *repeat > 2 {
		return errEvaluation
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	if args[0] == "prepare" {
		if *source == "" || prepare == nil || *resultDir != "" || *repeat != 1 || *verify || *canonicalPath != "" {
			return errEvaluation
		}
		pages, err := prepare(ctx, *source)
		if err != nil || len(pages) < 1 || len(pages) > 40 {
			return errEvaluation
		}
		if err := writePrivateJSON(*bundlePath, evaluationBundle{1, pages}); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]int{"preparedPages": len(pages)})
	}
	if *source != "" || *resultDir == "" {
		return errEvaluation
	}
	file, err := os.Open(*bundlePath)
	if err != nil {
		return errEvaluation
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (96<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 96<<20 {
		return errEvaluation
	}
	var bundle evaluationBundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bundle) != nil || decoder.Decode(new(any)) != io.EOF || bundle.Version != 1 || len(bundle.Pages) < 1 || len(bundle.Pages) > 40 {
		return errEvaluation
	}
	checksum := bundle.Pages[0].SourceSHA256
	for i, page := range bundle.Pages {
		if page.Page != i+1 || page.SourceSHA256 != checksum {
			return errEvaluation
		}
		if page.TotalPages == 0 {
			bundle.Pages[i].TotalPages = len(bundle.Pages)
		} else if page.TotalPages != len(bundle.Pages) {
			return errEvaluation
		}
	}
	if *canonicalPath != "" {
		file, err := os.Open(*canonicalPath)
		if err != nil {
			return errEvaluation
		}
		data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		var canonical onlinebulletins.CanonicalMetadata
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err != nil || closeErr != nil || len(data) > 64<<10 || decoder.Decode(&canonical) != nil || decoder.Decode(new(any)) != io.EOF {
			return errEvaluation
		}
		for i := range bundle.Pages {
			bundle.Pages[i].Canonical = &canonical
		}
	}
	deployment := getenv("AZURE_OPENAI_DEPLOYMENT")
	extractor, err := onlinebulletins.NewAzureExtractor(getenv("AZURE_OPENAI_ENDPOINT"), deployment, getenv("AZURE_OPENAI_API_KEY"), getenv("AZURE_OPENAI_RAI_POLICY"), client)
	if err != nil {
		return errEvaluation
	}
	// A new directory prevents mixing model versions or overwriting an earlier run.
	if err := os.Mkdir(*resultDir, 0700); err != nil {
		return errEvaluation
	}
	report := evaluationReport{Version: 2, ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: deployment}
	failed := false
	for n := 1; n <= *repeat; n++ {
		var continuation *onlinebulletins.RecognitionContinuation
		for _, page := range bundle.Pages {
			page.Continuation = continuation
			if ctx.Err() != nil {
				failed = true
				break
			}
			input, _ := json.Marshal(page)
			digest := sha256.Sum256(input)
			started := time.Now()
			var proposal onlinebulletins.RecognitionResult
			var usage onlinebulletins.RecognitionUsage
			var requestErr error
			if *verify {
				attempt := 0
				proposal, usage, requestErr = extractor.RecognizeVerifiedPage(ctx, page, func(phase string, result onlinebulletins.RecognitionResult, one, total onlinebulletins.RecognitionUsage, code string) error {
					attempt++
					checkpoint := onlinebulletins.RecognitionCheckpoint{Page: page.Page, Phase: phase, Result: result, Usage: one, TotalUsage: total, ErrorCode: code}
					return writePrivateJSON(filepath.Join(*resultDir, fmt.Sprintf("page-%02d-run-%d-attempt-%d.json", page.Page, n, attempt)), checkpoint)
				})
			} else {
				proposal, usage, requestErr = extractor.RecognizePage(ctx, page)
			}
			if *verify && requestErr == nil {
				continuation, requestErr = onlinebulletins.NextRecognitionContinuation(page, proposal)
			}
			valid := requestErr == nil
			report.Pages++
			report.Requests += usage.Requests
			if valid {
				report.Passed++
			} else {
				report.Failed++
				failed = true
			}
			report.InputTokens += usage.InputTokens
			report.OutputTokens += usage.OutputTokens
			report.ReasoningTokens += usage.ReasoningTokens
			for _, block := range proposal.Blocks {
				if block.Mode == "visual" {
					report.VisualBlocks++
				}
			}
			report.UnassignedIDs += len(proposal.UnassignedIDs)
			row := evaluationResult{page.Page, n, page.SourceSHA256, hex.EncodeToString(digest[:]), valid, time.Since(started).Milliseconds(), usage, proposal}
			if err := writePrivateJSON(filepath.Join(*resultDir, fmt.Sprintf("page-%02d-run-%d.json", page.Page, n)), row); err != nil {
				return err
			}
			if err := writeReport(filepath.Join(*resultDir, "metrics.json"), report); err != nil {
				return err
			}
			// Stop credential/transport failures instead of spending the whole budget.
			if requestErr != nil && (*verify || usage.Model == "") {
				_ = json.NewEncoder(output).Encode(report)
				return errEvaluation
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return errEvaluation
	}
	if failed {
		return errEvaluation
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errEvaluation
	}
	encodeErr := json.NewEncoder(file).Encode(value)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		return errEvaluation
	}
	return nil
}

func writeReport(path string, report evaluationReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil || os.WriteFile(path, append(data, '\n'), 0600) != nil {
		return errEvaluation
	}
	return nil
}
