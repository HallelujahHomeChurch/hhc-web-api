package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type progressRecognitionStore struct {
	recognitionMemoryStore
	events []string
}

func (s *progressRecognitionStore) BeginRecognitionProgress(_ context.Context, _ ExtractionJob, total int, _ time.Time) error {
	s.events = append(s.events, fmt.Sprintf("total:%d", total))
	return nil
}
func (s *progressRecognitionStore) StartRecognitionPage(_ context.Context, _ ExtractionJob, input RecognitionPage, _ time.Time) error {
	s.events = append(s.events, fmt.Sprintf("start:%d", input.Page))
	return nil
}
func (s *progressRecognitionStore) CompleteRecognitionPage(_ context.Context, _ ExtractionJob, input RecognitionPage, _ RecognitionResult, reused bool, _ time.Time) error {
	s.events = append(s.events, fmt.Sprintf("verified:%d:%t", input.Page, reused))
	return nil
}

func TestRecognitionRunnerReportsOnlyRevalidatedCachedPages(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	snapshot := ConversionTemplateSettings()
	job := ExtractionJob{LeaseUntil: time.Now().Add(RecognitionJobTimeout), Canonical: candidateIssue(), SourceAssetChecksum: source.SourceSHA256, TemplateSnapshot: &snapshot, ExtractorVersion: RecognitionExtractorVersion()}
	store := &progressRecognitionStore{recognitionMemoryStore: recognitionMemoryStore{pages: map[int]RecognitionCheckpoint{}}}
	var continuation *RecognitionContinuation
	for index, input := range pages {
		input.Canonical, input.Continuation = &job.Canonical.Metadata, continuation
		result := results[index]
		var ok bool
		result.SemanticSHA256, ok = recognitionSemanticSHA256(input, result)
		if !ok || !validMaterializedRecognition(input, result) {
			t.Fatal("invalid cached fixture")
		}
		raw, _ := json.Marshal(input)
		hash := sha256.Sum256(raw)
		store.pages[input.Page] = RecognitionCheckpoint{Canonical: job.Canonical.Metadata, Page: input.Page, Phase: "verified", InputSHA256: hex.EncodeToString(hash[:]), ProtocolSHA256: RecognitionProtocolSHA256(), Deployment: RecognitionDeployment, ModelVersion: RecognitionModelVersion, Result: result, TotalUsage: RecognitionUsage{Model: RecognitionDeployment, Requests: 2}}
		var err error
		continuation, err = NextRecognitionContinuation(input, result)
		if err != nil {
			t.Fatal(err)
		}
	}
	runner := RecognitionRunner{Client: &AzureExtractor{deployment: RecognitionDeployment}, Store: store, Assets: validDocument(4).LayoutManifest.Assets}
	if _, _, err := runner.Run(t.Context(), job, source, pages, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"total:4", "start:1", "verified:1:true", "start:2", "verified:2:true", "start:3", "verified:3:true", "start:4", "verified:4:true"}
	if !reflect.DeepEqual(store.events, want) {
		t.Fatalf("revalidated progress=%v want=%v", store.events, want)
	}
	store.events = nil
	bad := store.pages[2]
	bad.Result.SemanticSHA256 = string(make([]byte, 64))
	store.pages[2] = bad
	if _, _, err := runner.Run(t.Context(), job, source, pages, nil); err == nil {
		t.Fatal("semantic mismatch accepted")
	}
	if want := []string{"total:4", "start:1", "verified:1:true", "start:2"}; !reflect.DeepEqual(store.events, want) {
		t.Fatalf("invalid cached page counted: %v", store.events)
	}
}
