package translation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSummaryUsesQuotaAuditAndStrictOutput(t *testing.T) {
	for _, output := range []map[string]string{{"summary": "教會消息摘要。"}, {"summary": ""}, {"summary": strings.Repeat("界", 161)}, {"summary": "valid", "title": "extra"}} {
		generator := &generatorStub{result: Result{Fields: output}}
		repository := &translationRepositoryStub{}
		service := NewService(nil, nil, generator, repository, testServiceConfig())
		summary, err := service.GenerateSummary(context.Background(), SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "ja", Title: "題", Body: "本文", Actor: "actor", SourceVersion: 7})
		valid := output["summary"] == "教會消息摘要。"
		if valid && (err != nil || summary != output["summary"]) || !valid && !errors.Is(err, ErrProvider) {
			t.Fatalf("summary=%q err=%v output=%v", summary, err, output)
		}
		if repository.reserveCalls != 1 || len(repository.audits) != 1 || repository.audits[0].Action != "news_summary_generate" {
			t.Fatalf("repository=%+v", repository)
		}
		if !valid && repository.releaseCalls != 1 {
			t.Fatal("invalid output retained cooldown")
		}
		if generator.request.Purpose != "summary" || generator.request.SourceLocale != "ja" || generator.request.TargetLocale != "ja" {
			t.Fatalf("request=%+v", generator.request)
		}
	}
}

func TestSummaryRejectsInvalidInputBeforeProvider(t *testing.T) {
	base := SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "zh-Hant", Title: "消息", Body: "內容", Actor: "actor", SourceVersion: 7}
	for _, mutate := range []func(*SummaryRequest){
		func(r *SummaryRequest) { r.ResourceID = "invalid" }, func(r *SummaryRequest) { r.Locale = "fr" },
		func(r *SummaryRequest) { r.Actor = " " }, func(r *SummaryRequest) { r.SourceVersion = 0 },
		func(r *SummaryRequest) { r.Title = " " }, func(r *SummaryRequest) { r.Body = " " },
		func(r *SummaryRequest) { r.Body = string([]byte{0xff}) }, func(r *SummaryRequest) { r.Body = strings.Repeat("界", 100_001) },
	} {
		request := base
		mutate(&request)
		generator := &generatorStub{}
		repository := &translationRepositoryStub{}
		_, err := NewService(nil, nil, generator, repository, testServiceConfig()).GenerateSummary(context.Background(), request)
		if !errors.Is(err, ErrInvalidRequest) || repository.reserveCalls != 0 || generator.calls != 0 {
			t.Fatalf("request=%+v err=%v reserve=%d calls=%d", request, err, repository.reserveCalls, generator.calls)
		}
	}
}

func TestSummaryQuotaProviderFailureAndAudit(t *testing.T) {
	for _, test := range []struct {
		name                                    string
		reserveErr, providerErr, auditErr, want error
		timeout                                 bool
		outcome                                 string
	}{
		{name: "quota", reserveErr: &RateLimitError{RetryAfter: time.Minute}, want: ErrRateLimited, outcome: OutcomeRateLimited},
		{name: "filtered", providerErr: ErrContentFiltered, want: ErrContentFiltered, outcome: OutcomeContentFiltered},
		{name: "provider", providerErr: ErrProvider, want: ErrProvider, outcome: OutcomeProviderFailure},
		{name: "timeout", timeout: true, want: ErrTimeout, outcome: OutcomeTimeout},
		{name: "audit", auditErr: errors.New("database unavailable"), want: ErrAudit, outcome: OutcomeSuccess},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{result: Result{Fields: map[string]string{"summary": "摘要"}}, err: test.providerErr, waitForContext: test.timeout}
			repository := &translationRepositoryStub{reserveErr: test.reserveErr, auditErr: test.auditErr}
			config := testServiceConfig()
			config.HandlerTimeout = time.Millisecond
			_, err := NewService(nil, nil, generator, repository, config).GenerateSummary(context.Background(), SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "zh-Hant", Title: "消息", Body: "內容", Actor: "actor", SourceVersion: 7})
			if !errors.Is(err, test.want) || len(repository.audits) != 1 || repository.audits[0].Outcome != test.outcome {
				t.Fatalf("err=%v audits=%+v", err, repository.audits)
			}
			if test.reserveErr != nil && generator.calls != 0 {
				t.Fatal("quota bypassed")
			}
			if (test.providerErr != nil || test.timeout) && (repository.releaseCalls != 1 || repository.releaseContextErr != nil) {
				t.Fatalf("failure cleanup=%+v", repository)
			}
			if repository.audits[0].SourceLocale != "zh-Hant" || repository.audits[0].TargetLocale != "zh-Hant" || repository.audits[0].PromptVersion != "cms-summary-v1" {
				t.Fatalf("audit=%+v", repository.audits[0])
			}
		})
	}
}

func TestSummaryAzureHTTPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/responses" || r.Header.Get("x-policy-id") != "cms-policy" {
			t.Errorf("request=%v", r)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "existing-deployment" || payload["store"] != false || payload["background"] != false {
			t.Errorf("payload=%v", payload)
		}
		instructions := payload["instructions"].(string)
		if strings.Contains(instructions, "UNIQUE-SOURCE") || !strings.Contains(instructions, "same source language") || !strings.Contains(instructions, "never add facts") {
			t.Errorf("instructions=%s", instructions)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output_text": `{"summary":"消息摘要"}`})
	}))
	defer server.Close()
	result, err := NewAzureOpenAI(server.URL, "existing-deployment", "test-key", server.Client(), time.Second, "cms-policy").Generate(context.Background(), Request{Purpose: "summary", Module: "news", SourceLocale: "zh-Hant", TargetLocale: "zh-Hant", Fields: map[string]string{"title": "UNIQUE-SOURCE", "body": "消息正文"}})
	if err != nil || result.Fields["summary"] != "消息摘要" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSummaryProviderSchemaAndDecode(t *testing.T) {
	request := Request{Purpose: "summary", Module: "news", SourceLocale: "ja", TargetLocale: "ja", Fields: map[string]string{"title": "題", "body": "本文"}}
	body, err := NewAzureOpenAI("", "", "", nil, 0, "").requestBody(request)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instructions string
		Text         struct {
			Format struct {
				Schema struct{ Properties map[string]any }
			}
		}
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Text.Format.Schema.Properties) != 1 || payload.Text.Format.Schema.Properties["summary"] == nil || !strings.Contains(payload.Instructions, "160") {
		t.Fatalf("payload=%s", body)
	}
	for _, raw := range []string{`{"summary":"短い概要"}`, `{"summary":""}`, `{"summary":null}`, `{"summary":"ok","title":"extra"}`, `{bad}`, `{"summary":"one","summary":"two"}`} {
		result, err := decodeResult(raw, request)
		if raw == `{"summary":"短い概要"}` {
			if err != nil || result.Fields["summary"] != "短い概要" {
				t.Fatalf("%+v %v", result, err)
			}
		} else if err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestSummaryPreservesSafeRetryAfter(t *testing.T) {
	repository := &translationRepositoryStub{reserveErr: &RateLimitError{RetryAfter: 73 * time.Second}}
	service := NewService(nil, nil, &generatorStub{}, repository, testServiceConfig())
	_, err := service.GenerateSummary(context.Background(), SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "ja", Title: "來源", Body: "內容", Actor: "actor-1", SourceVersion: 7})
	var limited *RateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter != 73*time.Second {
		t.Fatalf("error = %#v", err)
	}
}

func TestSummaryFailsClosedWhenCooldownReleaseFails(t *testing.T) {
	repository := &translationRepositoryStub{releaseErr: errors.New("private database failure")}
	service := NewService(nil, nil, &generatorStub{err: ErrContentFiltered}, repository, testServiceConfig())
	_, err := service.GenerateSummary(context.Background(), SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "ja", Title: "來源", Body: "內容", Actor: "actor-1", SourceVersion: 7})
	if !errors.Is(err, ErrInternal) || strings.Contains(err.Error(), "private") || repository.releaseCalls != 1 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeInternalFailure {
		t.Fatalf("error=%v release=%d audits=%#v", err, repository.releaseCalls, repository.audits)
	}
}

func TestSummaryReleasesFailedCooldownAfterCallerCancellation(t *testing.T) {
	repository := &translationRepositoryStub{}
	service := NewService(nil, nil, &generatorStub{waitForContext: true}, repository, testServiceConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = service.GenerateSummary(ctx, SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "ja", Title: "來源", Body: "內容", Actor: "actor-1", SourceVersion: 7})
	if repository.releaseCalls != 1 || repository.releaseContextErr != nil || len(repository.audits) != 1 {
		t.Fatalf("release=%d context=%v audits=%#v", repository.releaseCalls, repository.releaseContextErr, repository.audits)
	}
}

func TestSummaryReservesExactResourceTargetAndBudgets(t *testing.T) {
	repository := &translationRepositoryStub{}
	config := testServiceConfig()
	service := NewService(nil, nil, &generatorStub{result: Result{Fields: map[string]string{"summary": "摘要"}}}, repository, config)
	if _, err := service.GenerateSummary(context.Background(), SummaryRequest{ResourceID: "10000000-0000-4000-8000-000000000001", Locale: "ja", Title: "來源", Body: "內容", Actor: "actor-1", SourceVersion: 7}); err != nil {
		t.Fatal(err)
	}
	want := Reservation{
		Actor: "actor-1", ResourceType: "news", ResourceID: "10000000-0000-4000-8000-000000000001", SourceVersion: 7, TargetLocale: "ja", Now: config.Now(),
		ActorMinuteLimit: 10, DeploymentMinuteLimit: 60, ActorDailyLimit: 30, DeploymentDailyLimit: 300, Cooldown: 10 * time.Minute,
	}
	if !reflect.DeepEqual(repository.reservation, want) {
		t.Fatalf("reservation = %#v, want %#v", repository.reservation, want)
	}
}
