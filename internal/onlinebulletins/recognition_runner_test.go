package onlinebulletins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recognitionMemoryStore struct {
	pages               map[int]RecognitionCheckpoint
	attempts            int
	canceledPersistence bool
}

func (s *recognitionMemoryStore) LoadRecognitionCheckpoint(_ context.Context, _ ExtractionJob, key RecognitionCheckpoint, _ time.Time) (*RecognitionCheckpoint, error) {
	saved, ok := s.pages[key.Page]
	if !ok {
		return nil, nil
	}
	if saved.InputSHA256 != key.InputSHA256 || saved.ProtocolSHA256 != key.ProtocolSHA256 {
		return nil, ErrExtractionJob
	}
	return &saved, nil
}
func (s *recognitionMemoryStore) SaveRecognitionCheckpoint(ctx context.Context, _ ExtractionJob, value RecognitionCheckpoint, _ time.Time) error {
	s.canceledPersistence = s.canceledPersistence || ctx.Err() != nil
	s.attempts++
	if value.Phase == "verified" {
		s.pages[value.Page] = value
	}
	return nil
}

func TestRecognitionRunnerResumesWithoutRepeatingPaidPages(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload struct {
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Fatal("request")
		}
		var input struct {
			Page int `json:"page"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(payload.Input[0].Content[0].Text, "SOURCE DATA:\n")), &input) != nil || input.Page < 1 || input.Page > 4 {
			t.Fatal("page binding")
		}
		proposal := results[input.Page-1]
		proposal.Blocks = append([]RecognitionBlock(nil), proposal.Blocks...)
		for i := range proposal.Blocks {
			proposal.Blocks[i].Text = ""
			proposal.Blocks[i].Box = Box{}
		}
		response := recognitionResponse(proposal)
		response["model"] = RecognitionDeployment
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, RecognitionDeployment, "key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := &recognitionMemoryStore{pages: map[int]RecognitionCheckpoint{}}
	runner := RecognitionRunner{Client: client, Store: store, Assets: validDocument(4).LayoutManifest.Assets}
	snapshot := ConversionTemplateSettings()
	job := ExtractionJob{LeaseUntil: time.Now().Add(RecognitionJobTimeout), Canonical: candidateIssue(), SourceAssetChecksum: source.SourceSHA256, TemplateSnapshot: &snapshot, ExtractorVersion: RecognitionExtractorVersion()}
	for range 2 {
		candidate, issues, err := runner.Run(context.Background(), job, source, pages, nil)
		if err != nil || candidate.Document == nil || !ValidRecognitionEvidence(candidate.Recognition, *candidate.Document) || len(issues) != 0 {
			t.Fatal("whole-source run failed", issues, err)
		}
		if string(candidate.SourceCoverPNG) != string(pages[0].PNG) {
			t.Fatal("source identity lost original cover evidence")
		}
	}
	if calls != 8 || store.attempts != 8 || len(store.pages) != 4 || store.canceledPersistence {
		t.Fatal("completed pages were billed again", calls, store.attempts, len(store.pages))
	}
}

type canceledCheckpointStore struct{ records []RecognitionCheckpoint }

func (s *canceledCheckpointStore) LoadRecognitionCheckpoint(context.Context, ExtractionJob, RecognitionCheckpoint, time.Time) (*RecognitionCheckpoint, error) {
	return nil, nil
}
func (s *canceledCheckpointStore) SaveRecognitionCheckpoint(ctx context.Context, _ ExtractionJob, c RecognitionCheckpoint, _ time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.records = append(s.records, c)
	return nil
}
func TestCanceledInferenceStillPersistsAttemptCost(t *testing.T) {
	source, pages, _ := recognitionDocumentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); w.WriteHeader(503) }))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, RecognitionDeployment, "test-key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := &canceledCheckpointStore{}
	snapshot := ConversionTemplateSettings()
	runner := RecognitionRunner{Client: client, Store: store, Assets: validDocument(4).LayoutManifest.Assets}
	job := ExtractionJob{Canonical: candidateIssue(), SourceAssetChecksum: source.SourceSHA256, TemplateSnapshot: &snapshot, ExtractorVersion: RecognitionExtractorVersion(), LeaseUntil: time.Now().Add(time.Minute)}
	_, _, err = runner.Run(ctx, job, source, pages, nil)
	if err == nil {
		t.Fatal("expected canceled inference")
	}
	if len(store.records) != 1 || store.records[0].Usage.Requests != 1 {
		t.Fatalf("sent attempt lost its durable cost record: records=%d", len(store.records))
	}
}
