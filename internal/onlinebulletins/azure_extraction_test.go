package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recognitionFixture(t *testing.T) RecognitionPage {
	t.Helper()
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return RecognitionPage{SourceSHA256: strings.Repeat("a", 64), Page: 1, TotalPages: 1, Width: 612, Height: 792, PNG: pixels.Bytes(), Native: []RecognitionText{
		{ID: "p1-t1", Text: "經文原文。", Box: Box{X: .2, Y: .8, Width: .5, Height: .02}},
		{ID: "p1-t2", Text: "(羅八/33-35、/37)", Box: Box{X: .2, Y: .82, Width: .5, Height: .02}},
	}}
}

func recognitionResponse(result any) map[string]any {
	text, _ := json.Marshal(result)
	return map[string]any{"status": "completed", "model": "gpt-6-luna-2026-09-22", "usage": map[string]any{
		"input_tokens": 1200, "output_tokens": 250, "output_tokens_details": map[string]any{"reasoning_tokens": 30},
	}, "output": []any{
		map[string]any{"type": "reasoning", "summary": []any{}},
		map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}},
	}}
}

func recognitionResultFixture() RecognitionResult {
	return RecognitionResult{Page: 1, Blocks: []RecognitionBlock{{Mode: "native", Role: "cover_verse", Group: "verse", Text: "", SourceIDs: []string{"p1-t1", "p1-t2"}, Box: Box{}}}, UnassignedIDs: []string{}}
}

func TestAzureRecognitionReceivesWholePermittedPageAndAccountsForEverySource(t *testing.T) {
	input := recognitionFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/responses" || r.Header.Get("api-key") != "test-key" || r.Header.Get("x-policy-id") != "existing-policy" {
			t.Error("unexpected Azure request contract")
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Fatal("request JSON")
		}
		encoded, _ := json.Marshal(payload)
		for _, required := range []string{"input_image", "data:image/png;base64,", "p1-t1", "p1-t2", "33-35、/37", "json_schema"} {
			if !strings.Contains(string(encoded), required) {
				t.Errorf("whole-page request lost %q", required)
			}
		}
		if payload["model"] != "candidate" || payload["store"] != false || payload["background"] != false || payload["max_output_tokens"] != float64(16384) {
			t.Error("model/persistence/output budget changed")
		}
		if payload["reasoning"].(map[string]any)["effort"] != "low" {
			t.Error("unbounded reasoning")
		}
		json.NewEncoder(w).Encode(recognitionResponse(recognitionResultFixture()))
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "existing-policy", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, usage, err := client.RecognizePage(context.Background(), input)
	if err != nil || len(result.Blocks) != 1 || result.Blocks[0].Text != "經文原文。(羅八/33-35、/37)" || result.Blocks[0].Box.X != .2 || result.Blocks[0].Box.Y != .8 {
		t.Fatalf("recognition failed: %v", err)
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 250 || usage.ReasoningTokens != 30 || usage.Model != "gpt-6-luna-2026-09-22" {
		t.Fatal("actual model/usage was not retained", usage)
	}
}

func TestAzureRecognitionRejectsIncompleteOrUntrustworthyOutput(t *testing.T) {
	for _, name := range []string{"incomplete", "refusal", "reasoning_only", "unknown_source", "missing_source", "duplicate_source", "wrong_page", "rewritten_native", "unknown_role", "invalid_box", "raw_error", "oversize"} {
		t.Run(name, func(t *testing.T) {
			result := recognitionResultFixture()
			switch name {
			case "unknown_source":
				result.Blocks[0].SourceIDs[1] = "invented"
			case "missing_source":
				result.Blocks[0].SourceIDs = result.Blocks[0].SourceIDs[:1]
				result.Blocks[0].Text = ""
			case "duplicate_source":
				result.UnassignedIDs = []string{"p1-t1"}
			case "wrong_page":
				result.Page = 2
			case "rewritten_native":
				result.Blocks[0].Text = "模型改寫的內容。"
			case "unknown_role":
				result.Blocks[0].Role = "publish"
			case "invalid_box":
				result.Blocks[0].Mode = "visual"
				result.Blocks[0].Text = "圖片中的文字"
				result.Blocks[0].Box.X = -1
			}
			body := recognitionResponse(result)
			switch name {
			case "incomplete":
				body["status"] = "incomplete"
			case "refusal":
				body["output"] = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "refusal", "refusal": "PRIVATE_SOURCE"}}}}
			case "reasoning_only":
				body["output"] = []any{map[string]any{"type": "reasoning"}}
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "raw_error" {
					w.WriteHeader(429)
					w.Write([]byte(`{"error":{"message":"PRIVATE_SOURCE"}}`))
					return
				}
				if name == "oversize" {
					w.Write(bytes.Repeat([]byte("x"), (4<<20)+1))
					return
				}
				json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = client.RecognizePage(context.Background(), recognitionFixture(t))
			if err == nil || strings.Contains(err.Error(), "PRIVATE_SOURCE") || strings.Contains(err.Error(), "test-key") {
				t.Fatal("untrusted result accepted or leaked", err)
			}
		})
	}
}

func TestAzureRecognitionRejectsUntrustedInputBeforeCallingProvider(t *testing.T) {
	for _, name := range []string{"invalid_hash", "duplicate_ids", "invalid_png", "invalid_page", "invalid_geometry"} {
		t.Run(name, func(t *testing.T) {
			input := recognitionFixture(t)
			switch name {
			case "invalid_hash":
				input.SourceSHA256 = "invalid"
			case "duplicate_ids":
				input.Native[1].ID = input.Native[0].ID
			case "invalid_png":
				input.PNG = []byte("not an image")
			case "invalid_page":
				input.Page = 0
			case "invalid_geometry":
				input.Native[0].Box.Width = 2
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid source reached provider") }))
			defer server.Close()
			client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = client.RecognizePage(context.Background(), input); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestAzureRecognitionRetainsBillableUsageWhenSourceValidationFails(t *testing.T) {
	result := recognitionResultFixture()
	result.Blocks[0].Text = "模型改寫了來源。"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(recognitionResponse(result))
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	rejected, usage, err := client.RecognizePage(context.Background(), recognitionFixture(t))
	if err == nil || usage.InputTokens != 1200 || usage.OutputTokens != 250 || usage.Duration <= 0 {
		t.Fatal("failed recognition concealed billable work", usage, err)
	}
	if len(rejected.Blocks) != 1 || rejected.Blocks[0].Text != result.Blocks[0].Text {
		t.Fatal("rejected proposal lost private evaluation evidence")
	}
}

func TestAzureRecognitionSchemaIdentityIsStable(t *testing.T) {
	identity := RecognitionProtocolSHA256()
	if !hashPattern.MatchString(identity) {
		t.Fatal("missing immutable protocol identity")
	}
	first, err := json.Marshal(recognitionSchema())
	if err != nil {
		t.Fatal(err)
	}
	for range 64 {
		next, err := json.Marshal(recognitionSchema())
		if err != nil || !bytes.Equal(first, next) || RecognitionProtocolSHA256() != identity {
			t.Fatal("schema identity changed between requests")
		}
	}
}

func TestAzureRecognitionRejectsReorderedNativeReferences(t *testing.T) {
	proposal := recognitionResultFixture()
	proposal.Blocks[0].SourceIDs = []string{"p1-t2", "p1-t1"}
	if validRecognitionResult(recognitionFixture(t), proposal) {
		t.Fatal("native source was reordered")
	}
}

func TestAzureRecognitionCountsIncompleteBillableUsage(t *testing.T) {
	body := recognitionResponse(recognitionResultFixture())
	body["status"] = "incomplete"
	body["incomplete_details"] = map[string]string{"reason": "max_output_tokens"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := client.RecognizePage(context.Background(), recognitionFixture(t))
	if err == nil || usage.InputTokens != 1200 || usage.OutputTokens != 250 {
		t.Fatal("incomplete response concealed billable work", usage, err)
	}
}

func TestAzureRecognitionAcceptsVersionedModelName(t *testing.T) {
	body := recognitionResponse(recognitionResultFixture())
	body["model"] = "gpt-6.1-terra-2026-10-26"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := client.RecognizePage(context.Background(), recognitionFixture(t))
	if err != nil || usage.Model != body["model"] {
		t.Fatal("versioned provider model name rejected", err)
	}
}

func TestAzureRecognitionRejectsImageTranscriptionOverNativeText(t *testing.T) {
	proposal := recognitionResultFixture()
	proposal.Blocks = append(proposal.Blocks, RecognitionBlock{Mode: "visual", Role: "cover_verse", Group: "duplicate", Text: "經文原文。", SourceIDs: []string{}, Box: recognitionFixture(t).Native[0].Box})
	if validRecognitionResult(recognitionFixture(t), proposal) {
		t.Fatal("image transcription duplicated native source")
	}
}

func TestRecognitionVisualTextAccountsForPartialNativeAndRequiresReview(t *testing.T) {
	input := recognitionFixture(t)
	input.Native = input.Native[:1]
	input.Native[0].Text = "～"
	proposal := recognitionResultFixture()
	proposal.Blocks[0].Mode = "visual"
	proposal.Blocks[0].Text = "圖片中的完整主題～副題"
	proposal.Blocks[0].SourceIDs = []string{"p1-t1"}
	proposal.Blocks[0].Box = input.Native[0].Box
	if !validRecognitionResult(input, proposal) {
		t.Fatal("partially outlined source cannot be reviewed")
	}
	proposal.Blocks[0].Mode = "native"
	if validRecognitionResult(input, proposal) {
		t.Fatal("visual transcription accepted as exact native text")
	}
}

func TestRecognitionVisualReferencesMustOverlapAndNotDuplicate(t *testing.T) {
	input := recognitionFixture(t)
	outside := recognitionResultFixture()
	outside.Blocks[0].Mode, outside.Blocks[0].Text = "visual", "影像文字"
	outside.Blocks[0].Box = Box{X: .01, Y: .01, Width: .05, Height: .05}
	if validRecognitionResult(input, outside) {
		t.Fatal("visual block consumed unrelated native source")
	}
	duplicate := recognitionResultFixture()
	visual := RecognitionBlock{Mode: "visual", Role: "cover_verse", Group: "art", Text: "圖中文字", SourceIDs: []string{}, Box: Box{X: .01, Y: .01, Width: .05, Height: .05}}
	duplicate.Blocks = append(duplicate.Blocks, visual, visual)
	if validRecognitionResult(input, duplicate) {
		t.Fatal("duplicate visual text accepted")
	}
}

func TestAzurePageVerificationUsesOriginalSourceAndPriorProposal(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid request")
		}
		if !strings.Contains(body["instructions"].(string), "independent source verification") {
			t.Error("verification was not distinguished from recognition")
		}
		encoded, _ := json.Marshal(body)
		if !bytes.Contains(encoded, []byte("previousProposal")) || !bytes.Contains(encoded, []byte("data:image/png;base64,")) {
			t.Error("verification lost source evidence")
		}
		json.NewEncoder(w).Encode(recognitionResponse(recognitionResultFixture()))
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	previous := recognitionResultFixture()
	previous.Blocks[0].Text = "不可信的先前辨識"
	result, _, err := client.VerifyPage(context.Background(), recognitionFixture(t), previous)
	if err != nil || calls != 1 || result.Blocks[0].Text != "經文原文。(羅八/33-35、/37)" {
		t.Fatal("source verification failed", err)
	}
}

func TestRecognitionDoesNotRequireTranscribingCodeOwnedArtwork(t *testing.T) {
	input := recognitionFixture(t)
	input.Native = []RecognitionText{}
	result := RecognitionResult{Page: 1, Blocks: []RecognitionBlock{{Mode: "visual", Role: "fixed_label", Group: "masthead", Text: "", SourceIDs: []string{}, Box: Box{X: .1, Y: .1, Width: .3, Height: .05}}}, UnassignedIDs: []string{}}
	if !validRecognitionResult(input, result) {
		t.Fatal("omitted code-owned artwork requires unnecessary OCR")
	}
	result.Blocks[0].Role = "cover_verse"
	if validRecognitionResult(input, result) {
		t.Fatal("empty dynamic visual text accepted")
	}
}

func TestVerifiedRecognitionIsBoundedAndStopsAtLostLease(t *testing.T) {
	for _, scenario := range []string{"repair", "verification_limit", "lost_lease", "changed_model"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				result := recognitionResultFixture()
				if calls == 1 || scenario == "verification_limit" {
					result.Blocks[0].SourceIDs = result.Blocks[0].SourceIDs[:1]
				}
				response := recognitionResponse(result)
				if scenario == "changed_model" && calls > 1 {
					response["model"] = "other-model"
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client, err := NewAzureExtractor(server.URL, "candidate", "key", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			phases := []string{}
			result, usage, err := client.RecognizeVerifiedPage(context.Background(), recognitionFixture(t), func(phase string, result RecognitionResult, one, total RecognitionUsage, code string) error {
				phases = append(phases, phase)
				if scenario == "lost_lease" {
					return ErrExtractionLease
				}
				return nil
			})
			switch scenario {
			case "repair":
				if err != nil || calls != 3 || usage.InputTokens != 3600 || usage.Requests != 3 || result.Blocks[0].Text != "經文原文。(羅八/33-35、/37)" || phases[2] != "verified" {
					t.Fatal("bounded repair failed", calls, usage, phases, err)
				}
			case "verification_limit":
				if err == nil || calls != 3 || usage.Requests != 3 {
					t.Fatal("verification did not stop at budget", calls, usage, err)
				}
			case "changed_model":
				if err == nil || calls != 2 || usage.Requests != 2 || len(phases) != 2 || phases[1] != "verification" {
					t.Fatal("changed model attempt lost", calls, usage, phases, err)
				}
			case "lost_lease":
				if !errors.Is(err, ErrExtractionLease) || calls != 1 {
					t.Fatal("inference continued after lease loss", calls, err)
				}
			}
		})
	}
}

func TestIgnoredArtworkRegionCannotConsumeOrDuplicateRetainedText(t *testing.T) {
	input := recognitionFixture(t)
	result := recognitionResultFixture()
	result.Blocks = append(result.Blocks, RecognitionBlock{Mode: "visual", Role: "fixed_label", Group: "omitted-art", Text: "", SourceIDs: []string{}, Box: input.Native[0].Box})
	if !validRecognitionResult(input, result) {
		t.Fatal("non-rendered code-owned art box claimed retained native content")
	}
	result.Blocks[1].Text = "不應重抄的文字"
	if validRecognitionResult(input, result) {
		t.Fatal("nonempty overlapping visual transcription accepted")
	}
}

func TestRecognitionClassifiesBodyKindInTheSameResult(t *testing.T) {
	input := recognitionFixture(t)
	result := recognitionResultFixture()
	result.Blocks[0].Role = "body_title"
	result.Blocks[0].SectionKind = "sermon"
	if !validRecognitionResult(input, result) {
		t.Fatal("body kind was not part of recognition")
	}
	result.Blocks[0].SectionKind = "publish"
	if validRecognitionResult(input, result) {
		t.Fatal("untrusted body kind accepted")
	}
	result.Blocks[0].Role = "cover_verse"
	result.Blocks[0].SectionKind = "sermon"
	if validRecognitionResult(input, result) {
		t.Fatal("body kind attached to unrelated content")
	}
}

func TestRecognitionAccountsForEverySourceImage(t *testing.T) {
	input := recognitionFixture(t)
	id := strings.Repeat("e", 64)
	input.Images = []RecognitionImage{{ID: id, Box: Box{X: .2, Y: .2, Width: .3, Height: .2}}}
	result := recognitionResultFixture()
	if validRecognitionResult(input, result) {
		t.Fatal("unaccounted source image was silently discarded")
	}
	result.Blocks = append(result.Blocks, RecognitionBlock{Mode: "visual", Role: "illustration", Group: "picture", SourceIDs: []string{}, ImageIDs: []string{id}})
	if !validRecognitionResult(input, result) {
		t.Fatal("source image reference rejected")
	}
	result.Blocks[0].ImageIDs = []string{id}
	if validRecognitionResult(input, result) {
		t.Fatal("source image was duplicated")
	}
}

type canceledBodyTransport struct{ cancel context.CancelFunc }

func (t canceledBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: canceledBody{t.cancel}, Header: make(http.Header)}, nil
}

type canceledBody struct{ cancel context.CancelFunc }

func (b canceledBody) Read([]byte) (int, error) { b.cancel(); return 0, context.Canceled }
func (b canceledBody) Close() error             { return nil }
func TestCanceledResponseBodyKeepsTimeoutClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := NewAzureExtractor("https://example.invalid", "candidate", "key", "", &http.Client{Transport: canceledBodyTransport{cancel}})
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := client.RecognizePage(ctx, recognitionFixture(t))
	if !errors.Is(err, context.Canceled) || usage.Requests != 1 {
		t.Fatal("canceled body lost resumable classification", err, usage.Requests)
	}
}
