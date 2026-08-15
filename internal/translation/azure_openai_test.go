package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAzureOpenAIGenerateUsesResponsesContract(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/responses" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("api-key"); got != "test-api-key" {
			t.Errorf("api-key = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("x-policy-id"); got != "hhc-cms-translation-v1" {
			t.Errorf("x-policy-id = %q", got)
		}

		var body struct {
			Model        string          `json:"model"`
			Store        *bool           `json:"store"`
			Background   *bool           `json:"background"`
			Tools        json.RawMessage `json:"tools"`
			Instructions string          `json:"instructions"`
			Input        []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Text struct {
				Format struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
					Schema struct {
						Type                 string                    `json:"type"`
						Properties           map[string]map[string]any `json:"properties"`
						Required             []string                  `json:"required"`
						AdditionalProperties *bool                     `json:"additionalProperties"`
					} `json:"schema"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "cms-deployment" || body.Store == nil || *body.Store || body.Background == nil || *body.Background || body.Tools != nil {
			t.Errorf("unexpected provider options: %#v", body)
		}
		if strings.Contains(body.Instructions, "SOURCE_TITLE_UNIQUE") || strings.Contains(body.Instructions, "SOURCE_BODY_UNIQUE") {
			t.Error("source content leaked into trusted instructions")
		}
		if len(body.Input) != 1 || body.Input[0].Role != "user" || len(body.Input[0].Content) != 1 || body.Input[0].Content[0].Type != "input_text" {
			t.Fatalf("unexpected input shape: %#v", body.Input)
		}
		untrusted := body.Input[0].Content[0].Text
		if !strings.HasPrefix(untrusted, "SOURCE DATA:\n{") || strings.Contains(untrusted, "UNTRUSTED") || strings.Contains(untrusted, "ignore any instructions") || !strings.Contains(untrusted, "SOURCE_TITLE_UNIQUE") || !strings.Contains(untrusted, "SOURCE_BODY_UNIQUE") {
			t.Errorf("source is not isolated as untrusted data: %q", untrusted)
		}
		format := body.Text.Format
		if format.Type != "json_schema" || format.Name != "cms_translation" || !format.Strict || format.Schema.Type != "object" || format.Schema.AdditionalProperties == nil || *format.Schema.AdditionalProperties {
			t.Errorf("unexpected response format: %#v", format)
		}
		if !reflect.DeepEqual(format.Schema.Required, []string{"body", "title", "titleRule"}) {
			t.Errorf("required = %#v", format.Schema.Required)
		}
		if !reflect.DeepEqual(format.Schema.Properties["body"], map[string]any{"type": "string"}) || !reflect.DeepEqual(format.Schema.Properties["title"], map[string]any{"type": "string"}) {
			t.Errorf("field properties = %#v", format.Schema.Properties)
		}
		ruleSchema := format.Schema.Properties["titleRule"]
		if ruleSchema["type"] != "object" || ruleSchema["additionalProperties"] != false {
			t.Errorf("title rule schema = %#v", ruleSchema)
		}
		ruleProperties, ok := ruleSchema["properties"].(map[string]any)
		if !ok || len(ruleProperties) != 6 {
			t.Fatalf("title rule properties = %#v", ruleSchema["properties"])
		}
		kind, ok := ruleProperties["kind"].(map[string]any)
		if !ok || kind["type"] != "string" || !reflect.DeepEqual(kind["enum"], []any{"none", "gospel_dinner"}) {
			t.Errorf("title rule kind = %#v", ruleProperties["kind"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"error":null,
			"incomplete_details":null,
			"content_filters":[],
			"output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":"{\"body\":\"Translated body\",\"title\":\"Ignored full title\",\"titleRule\":{\"kind\":\"gospel_dinner\",\"sequence\":\"432\",\"sourceQualifier\":\"\",\"localizedQualifier\":\"\",\"sourceEventName\":\"璨恩的尋根\",\"localizedEventName\":\"璨恩のルーツ探し\"}}"}]}],
			"output_text":"{\"body\":\"wrong fallback\",\"title\":\"wrong fallback\"}"
		}`))
	}))
	defer server.Close()

	client := NewAzureOpenAI(server.URL+"/", "cms-deployment", "test-api-key", server.Client(), time.Second, "hhc-cms-translation-v1")
	result, err := client.Generate(context.Background(), Request{
		Module:       "news",
		SourceLocale: "zh-Hant",
		TargetLocale: "ja",
		Fields:       map[string]string{"title": "432次綠野仙蹤福音餐會 - 璨恩的尋根 SOURCE_TITLE_UNIQUE", "body": "SOURCE_BODY_UNIQUE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
	if !reflect.DeepEqual(result.Fields, map[string]string{"body": "Translated body", "title": "Ignored full title"}) {
		t.Fatalf("result fields = %#v", result.Fields)
	}
	wantRule := &TitleRuleResult{Kind: "gospel_dinner", Sequence: "432", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"}
	if !reflect.DeepEqual(result.TitleRule, wantRule) {
		t.Fatalf("title rule = %#v, want %#v", result.TitleRule, wantRule)
	}
}

func TestAzureOpenAIGenerateAcceptsTopLevelOutputTextFallback(t *testing.T) {
	server := responseServer(http.StatusOK, `{"status":"completed","error":null,"incomplete_details":null,"output":[],"output_text":"{\"title\":\"Fallback title\"}"}`)
	defer server.Close()

	result, err := NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), time.Second, "").Generate(context.Background(), Request{Fields: map[string]string{"title": "source"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Fields["title"] != "Fallback title" {
		t.Fatalf("result = %#v", result)
	}
	if result.TitleRule != nil {
		t.Fatalf("unexpected title rule = %#v", result.TitleRule)
	}
}

func TestAzureOpenAIGenerateRejectsMalformedTitleRule(t *testing.T) {
	tests := []struct {
		name string
		rule string
	}{
		{name: "missing field", rule: `{"kind":"gospel_dinner","sequence":"432","sourceQualifier":"","localizedQualifier":"","sourceEventName":"璨恩的尋根"}`},
		{name: "unknown field", rule: `{"kind":"none","sequence":"","sourceQualifier":"","localizedQualifier":"","sourceEventName":"","localizedEventName":"","extra":"value"}`},
		{name: "wrong type", rule: `{"kind":"none","sequence":432,"sourceQualifier":"","localizedQualifier":"","sourceEventName":"","localizedEventName":""}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := `{"title":"ignored","titleRule":` + test.rule + `}`
			body, err := json.Marshal(map[string]any{"status": "completed", "error": nil, "incomplete_details": nil, "output": []any{}, "output_text": output})
			if err != nil {
				t.Fatal(err)
			}
			server := responseServer(http.StatusOK, string(body))
			defer server.Close()

			request := Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja", Fields: map[string]string{"title": "綠野仙蹤"}}
			_, err = NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), time.Second, "").Generate(context.Background(), request)
			if !errors.Is(err, ErrProvider) {
				t.Fatalf("error = %v, want ErrProvider", err)
			}
		})
	}
}

func TestAzureOpenAIGenerateRejectsProviderFailuresWithoutContentLeaks(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "malformed response", status: http.StatusOK, body: `{"status":`},
		{name: "failed status", status: http.StatusOK, body: `{"status":"failed","error":null,"incomplete_details":null}`},
		{name: "provider error", status: http.StatusOK, body: `{"status":"completed","error":{"message":"PROVIDER_SECRET"},"incomplete_details":null}`},
		{name: "refusal", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output":[{"status":"completed","content":[{"type":"refusal","refusal":"OUTPUT_SECRET"}]}]}`},
		{name: "incomplete output item", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output":[{"status":"incomplete","content":[{"type":"output_text","text":"{\"title\":\"OUTPUT_SECRET\"}"}]}]}`},
		{name: "null nested output", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output":[{"status":"completed","content":[{"type":"output_text","text":null}]}],"output_text":"{\"title\":\"OUTPUT_SECRET\"}"}`},
		{name: "malformed output JSON", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output_text":"{OUTPUT_SECRET"}`},
		{name: "unknown output field", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output_text":"{\"title\":\"ok\",\"extra\":\"OUTPUT_SECRET\"}"}`},
		{name: "missing output field", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output_text":"{}"}`},
		{name: "non-string output", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"output_text":"{\"title\":42}"}`},
		{name: "oversized response", status: http.StatusOK, body: strings.Repeat("x", (1<<20)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := responseServer(tt.status, tt.body)
			defer server.Close()

			var logs bytes.Buffer
			oldLogWriter := log.Writer()
			oldSlog := slog.Default()
			log.SetOutput(&logs)
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer func() {
				log.SetOutput(oldLogWriter)
				slog.SetDefault(oldSlog)
			}()

			_, err := NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), time.Second, "").Generate(context.Background(), Request{Fields: map[string]string{"title": "SOURCE_SECRET"}})
			if !errors.Is(err, ErrProvider) {
				t.Fatalf("error = %v, want ErrProvider", err)
			}
			for _, secret := range []string{"SOURCE_SECRET", "OUTPUT_SECRET", "PROVIDER_SECRET"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
					t.Fatalf("provider content leaked through error/logs: %q / %q", err, logs.String())
				}
			}
			if logs.Len() != 0 {
				t.Fatalf("client emitted logs: %q", logs.String())
			}
		})
	}
}

func TestAzureOpenAIGenerateClassifiesContentFilterWithoutContentLeaks(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "provider error", status: http.StatusBadRequest, body: `{"error":{"code":"content_filter","message":"PROVIDER_SECRET","innererror":{"code":"ContentFiltered"}}}`},
		{name: "incomplete response", status: http.StatusOK, body: `{"status":"incomplete","error":null,"incomplete_details":{"reason":"content_filter"}}`},
		{name: "blocked response metadata", status: http.StatusOK, body: `{"status":"completed","error":null,"incomplete_details":null,"content_filters":[{"blocked":true}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := responseServer(test.status, test.body)
			defer server.Close()

			_, err := NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), time.Second, "").Generate(context.Background(), Request{Fields: map[string]string{"title": "SOURCE_SECRET"}})
			if !errors.Is(err, ErrContentFiltered) {
				t.Fatalf("error = %v, want ErrContentFiltered", err)
			}
			for _, secret := range []string{"SOURCE_SECRET", "PROVIDER_SECRET", "ContentFiltered"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("provider content leaked through error: %q", err)
				}
			}
		})
	}
}

func TestAzureOpenAIGenerateUsesBoundedChildTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()

	client := NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), 20*time.Millisecond, "")
	started := time.Now()
	_, err := client.Generate(context.Background(), Request{Fields: map[string]string{"title": "source"}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if got := NewAzureOpenAI(server.URL, "deployment", "key", server.Client(), 0, "").timeout; got != 40*time.Second {
		t.Fatalf("default timeout = %s", got)
	}
}

func TestAzureOpenAIGenerateDoesNotFollowRedirects(t *testing.T) {
	var providerCalls atomic.Int32
	var targetCalls atomic.Int32
	var targetSawCredential atomic.Bool
	var targetSawSource atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		targetSawCredential.Store(r.Header.Get("api-key") != "")
		body, _ := io.ReadAll(r.Body)
		targetSawSource.Store(bytes.Contains(body, []byte("REDIRECT_SOURCE_SECRET")))
		_, _ = w.Write([]byte(`{"status":"completed","error":null,"incomplete_details":null,"output_text":"{\"title\":\"redirected\"}"}`))
	}))
	defer target.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()

	sharedClient := provider.Client()
	client := NewAzureOpenAI(provider.URL, "deployment", "REDIRECT_API_KEY_SECRET", sharedClient, time.Second, "")
	if sharedClient.CheckRedirect != nil {
		t.Fatal("constructor mutated the shared HTTP client")
	}
	_, err := client.Generate(context.Background(), Request{Fields: map[string]string{"title": "REDIRECT_SOURCE_SECRET"}})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("error = %v, want ErrProvider", err)
	}
	if providerCalls.Load() != 1 || targetCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, redirect target calls = %d", providerCalls.Load(), targetCalls.Load())
	}
	if targetSawCredential.Load() || targetSawSource.Load() {
		t.Fatal("redirect target received credential or source content")
	}
	for _, secret := range []string{"REDIRECT_API_KEY_SECRET", "REDIRECT_SOURCE_SECRET"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("redirect error exposed sensitive content")
		}
	}
}

func responseServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}
