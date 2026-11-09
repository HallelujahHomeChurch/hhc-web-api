package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type RecognitionStore interface {
	LoadRecognitionCheckpoint(context.Context, ExtractionJob, RecognitionCheckpoint, time.Time) (*RecognitionCheckpoint, error)
	SaveRecognitionCheckpoint(context.Context, ExtractionJob, RecognitionCheckpoint, time.Time) error
}

type RecognitionRunner struct {
	Client *AzureExtractor
	Store  RecognitionStore
	Assets []Asset
}

func (r RecognitionRunner) Run(ctx context.Context, job ExtractionJob, source GeometryDocument, inputs []RecognitionPage, images map[string]SourceImage) (DocumentCandidate, []ReviewIssue, error) {
	if r.Client == nil || r.Store == nil || r.Client.deployment != RecognitionDeployment || job.TemplateSnapshot == nil || job.ExtractorVersion != RecognitionExtractorVersion() || source.SourceSHA256 != job.SourceAssetChecksum || len(inputs) != len(source.Pages) || len(inputs) < 4 || len(inputs) > 40 {
		return DocumentCandidate{}, nil, ErrRecognition
	}
	var results []RecognitionResult
	inputs = append([]RecognitionPage(nil), inputs...)
	receipt := &RecognitionEvidence{ProtocolSHA256: RecognitionProtocolSHA256(), Deployment: r.Client.deployment, ModelVersion: RecognitionModelVersion, SourceSHA256: source.SourceSHA256}
	var continuation *RecognitionContinuation
	for index, input := range inputs {
		input.Continuation = continuation
		if err := ctx.Err(); err != nil {
			return DocumentCandidate{}, nil, err
		}
		input.Canonical = &job.Canonical.Metadata
		if input.Page != index+1 || input.SourceSHA256 != source.SourceSHA256 || !validRecognitionPage(input) {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		sum := sha256.Sum256(raw)
		key := RecognitionCheckpoint{Canonical: job.Canonical.Metadata, Page: input.Page, Phase: "verified", InputSHA256: hex.EncodeToString(sum[:]), ProtocolSHA256: receipt.ProtocolSHA256, Deployment: receipt.Deployment, ModelVersion: RecognitionModelVersion}
		saved, err := r.Store.LoadRecognitionCheckpoint(ctx, job, key, time.Now().UTC())
		if err != nil {
			return DocumentCandidate{}, nil, err
		}
		var result RecognitionResult
		var total RecognitionUsage
		if saved != nil {
			result, total = saved.Result, saved.TotalUsage
			signature, ok := recognitionSemanticSHA256(input, result)
			if !ok || result.SemanticSHA256 != signature || !validMaterializedRecognition(input, result) {
				return DocumentCandidate{}, nil, ErrRecognition
			}
		} else {
			result, total, err = r.Client.RecognizeVerifiedPage(ctx, input, func(phase string, result RecognitionResult, usage, aggregate RecognitionUsage, code string) error {
				checkpoint := key
				checkpoint.Phase, checkpoint.Result, checkpoint.Usage, checkpoint.TotalUsage, checkpoint.ErrorCode = phase, result, usage, aggregate, code
				// Reserve lease time for recording even when inference was canceled.
				deadline := time.Now().Add(5 * time.Second)
				if job.LeaseUntil.Before(deadline) {
					deadline = job.LeaseUntil
				}
				persistence, stop := context.WithDeadline(context.WithoutCancel(ctx), deadline)
				defer stop()
				return r.Store.SaveRecognitionCheckpoint(persistence, job, checkpoint, time.Now().UTC())
			})
			if err != nil {
				return DocumentCandidate{}, nil, err
			}
		}
		continuation, err = NextRecognitionContinuation(input, result)
		if err != nil {
			return DocumentCandidate{}, nil, err
		}
		inputs[index] = input
		results = append(results, result)
		receipt.Pages = append(receipt.Pages, RecognitionPageEvidence{Page: input.Page, InputSHA256: key.InputSHA256, SemanticSHA256: result.SemanticSHA256, Usage: total})
	}
	candidate, issues, err := AssembleRecognition(source, inputs, results, job.Canonical, *job.TemplateSnapshot, r.Assets)
	if err != nil || candidate.Document == nil {
		return candidate, issues, err
	}
	if err := attachRecognitionImages(&candidate, results, images); err != nil {
		return DocumentCandidate{}, nil, err
	}
	candidate.Recognition = receipt
	candidate.SourceCoverPNG = append([]byte{}, inputs[0].PNG...)
	if !ValidRecognitionEvidence(receipt, *candidate.Document) {
		return DocumentCandidate{}, nil, ErrRecognition
	}
	retained := []ReviewIssue{}
	for _, issue := range issues {
		if issue.Code != "recognition_visual_verification_required" {
			retained = append(retained, issue)
		}
	}
	return candidate, retained, nil
}
