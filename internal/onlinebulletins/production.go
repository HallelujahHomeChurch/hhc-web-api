package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// AdminProduction describes extraction progress only. Later layout/publication
// remain the separate Admin projections; progress never grants publication.
type AdminProduction struct {
	SourceIdentity     string     `json:"sourceIdentity"`
	JobID              string     `json:"jobId"`
	AttemptID          string     `json:"attemptId"`
	ProgressVersion    int64      `json:"progressVersion"`
	Phase              string     `json:"phase"`
	Status             string     `json:"status"`
	TotalPages         *int       `json:"totalPages"`
	CurrentPage        *int       `json:"currentPage"`
	VerifiedPages      int        `json:"verifiedPages"`
	Subphase           string     `json:"subphase"`
	StartedAt          *time.Time `json:"startedAt"`
	LastProgressAt     *time.Time `json:"lastProgressAt"`
	HeartbeatAt        *time.Time `json:"heartbeatAt"`
	ObservedAt         time.Time  `json:"observedAt"`
	VerifiedReuseCount int        `json:"verifiedReuseCount"`
}

// Optional for compatible stores. Calls remain sequential with recognition.
type RecognitionProgressStore interface {
	BeginRecognitionProgress(context.Context, ExtractionJob, int, time.Time) error
	StartRecognitionPage(context.Context, ExtractionJob, RecognitionPage, time.Time) error
	CompleteRecognitionPage(context.Context, ExtractionJob, RecognitionPage, RecognitionResult, bool, time.Time) error
}

type RecognitionProgressProof struct {
	InputSHA256, SemanticSHA256, PreviousSHA256, NextSHA256 string
}

// Validate the same private source/materialization/continuation boundaries as
// recognition, returning hashes only for the durable progress receipt.
func VerifiedRecognitionProgress(input RecognitionPage, result RecognitionResult) (RecognitionProgressProof, error) {
	if !validRecognitionPage(input) || input.Canonical == nil || !validMaterializedRecognition(input, result) {
		return RecognitionProgressProof{}, ErrRecognition
	}
	next, err := NextRecognitionContinuation(input, result)
	if err != nil {
		return RecognitionProgressProof{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return RecognitionProgressProof{}, ErrRecognition
	}
	sum := sha256.Sum256(raw)
	proof := RecognitionProgressProof{InputSHA256: hex.EncodeToString(sum[:]), SemanticSHA256: result.SemanticSHA256, NextSHA256: next.PreviousSHA256}
	if input.Continuation != nil {
		proof.PreviousSHA256 = input.Continuation.PreviousSHA256
	}
	if input.Page == 1 && input.Continuation != nil || input.Page > 1 && !hashPattern.MatchString(proof.PreviousSHA256) {
		return RecognitionProgressProof{}, ErrRecognition
	}
	return proof, nil
}
