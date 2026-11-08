package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type evaluationObserver struct {
	base            http.RoundTripper
	Model           string `json:"model"`
	HTTPStatus      int    `json:"httpStatus"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	ReasoningTokens int64  `json:"reasoningTokens"`
}

func (o *evaluationObserver) reset() { *o = evaluationObserver{base: o.base} }

func (o *evaluationObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := o.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	o.HTTPStatus = response.StatusCode
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(data))
	if readErr != nil {
		return response, readErr
	}
	var envelope struct {
		Model string `json:"model"`
		Usage struct {
			Input   int64 `json:"input_tokens"`
			Output  int64 `json:"output_tokens"`
			Details struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if len(data) <= maxResponseBytes && json.Unmarshal(data, &envelope) == nil {
		o.Model = envelope.Model
		o.InputTokens = envelope.Usage.Input
		o.OutputTokens = envelope.Usage.Output
		o.ReasoningTokens = envelope.Usage.Details.Reasoning
	}
	return response, nil
}

func TestEvaluationObserverPreservesProviderBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"completed","model":"candidate-version","usage":{"input_tokens":123,"output_tokens":45,"output_tokens_details":{"reasoning_tokens":9}},"output":[{"content":[{"type":"output_text","text":"{\"summary\":\"完整消息摘要。\"}"}]}]}`))
	}))
	defer server.Close()
	observer := &evaluationObserver{base: server.Client().Transport}
	client := &http.Client{Transport: observer}
	result, err := NewAzureOpenAI(server.URL, "candidate", "test-key", client, time.Second, "").Generate(context.Background(), Request{Purpose: "summary", Module: "news", Fields: map[string]string{"title": "測試消息", "body": "完整消息正文。"}})
	if err != nil || result.Fields["summary"] != "完整消息摘要。" || observer.InputTokens != 123 || observer.OutputTokens != 45 || observer.ReasoningTokens != 9 || observer.Model != "candidate-version" || observer.HTTPStatus != 200 {
		t.Fatal("observer changed provider semantics or accounting", err)
	}
}

// Opt-in synthetic evaluation; no database, production content or publication.
func TestAzureCMSModelEvaluation(t *testing.T) {
	if os.Getenv("HHC_AZURE_CMS_EVALUATE") != "1" {
		t.Skip("explicit live evaluation not enabled")
	}
	path := os.Getenv("HHC_AZURE_CMS_EVALUATION_OUTPUT")
	deployments := strings.Split(os.Getenv("HHC_AZURE_CMS_EVALUATION_DEPLOYMENTS"), ",")
	if path == "" || len(deployments) < 1 || len(deployments) > 2 {
		t.Fatal("explicit bounded evaluation configuration required")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("evaluation output unavailable")
	}
	defer file.Close()
	var cases []struct {
		ID       string
		Request  Request
		Required string
	}
	for _, locale := range []string{"zh-Hans", "en", "ja", "ko"} {
		for _, module := range []string{"news", "pages"} {
			fields := map[string]string{"title": "主日聚會通知", "body": "聚會於2026年11月8日舉行，上午9點開始，下午3點結束。報名人數上限為120人。", "imageAlt": "聚會海報"}
			if module == "pages" {
				fields = map[string]string{"heroTitle": "隱私權政策", "sections.0.body.0": "使用者可以撤回同意並申請刪除個人資料。我們於收到申請後30天內回覆，依法必須保存的資料除外。"}
			}
			cases = append(cases, struct {
				ID       string
				Request  Request
				Required string
			}{module + "-" + locale, Request{Module: module, SourceLocale: "zh-Hant", TargetLocale: locale, Fields: fields}, map[bool]string{true: "30", false: "120"}[module == "pages"]})
		}
	}
	for _, locale := range []string{"zh-Hans", "en"} {
		cases = append(cases, struct {
			ID       string
			Request  Request
			Required string
		}{"bulletin-" + locale, Request{Module: "bulletins", SourceLocale: "zh-Hant", TargetLocale: locale, Fields: map[string]string{"title": "信心與盼望", "subtitle": "彼此相愛，一同成長"}}, ""})
	}
	for _, summary := range []struct{ locale, title, body string }{
		{"zh-Hant", "主日聚會通知", "聚會於2026年11月8日上午9點開始，下午3點結束，報名上限120人，請於活動前報名。"},
		{"zh-Hans", "主日聚会通知", "聚会于2026年11月8日上午9点开始，下午3点结束，报名上限120人，请于活动前报名。"},
		{"en", "Sunday Gathering", "The gathering runs from 9 a.m. to 3 p.m. on November 8, 2026. Registration is limited to 120 people. Please register before the event."},
		{"ja", "主日礼拝のお知らせ", "集会は2026年11月8日午前9時から午後3時まで開催します。定員は120人です。事前にお申し込みください。"},
		{"ko", "주일 모임 안내", "모임은 2026년 11월 8일 오전 9시부터 오후 3시까지 열려요. 정원은 120명이에요. 행사 전에 신청해 주세요."},
	} {
		cases = append(cases, struct {
			ID       string
			Request  Request
			Required string
		}{
			"summary-" + summary.locale, Request{Purpose: "summary", Module: "news", SourceLocale: summary.locale, TargetLocale: summary.locale, Fields: map[string]string{"title": summary.title, "body": summary.body}}, "120",
		})
	}
	cases = append(cases, struct {
		ID       string
		Request  Request
		Required string
	}{"japanese-title-rule", Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja", Fields: map[string]string{"title": "432次綠野仙蹤福音餐會 - 春日音樂夜", "body": "第432次福音餐會將舉辦音樂與生命故事分享。", "imageAlt": "活動海報"}}, "432"})
	for _, module := range []string{"history", "videos", "campaigns", "campaign-schedules"} {
		fields := map[string]string{"title": "一起分享信心與盼望"}
		if module == "history" {
			fields["body"] = "我們一同回顧教會的成長歷程，並感謝每一位參與服事的同工。"
		}
		if module == "campaigns" || module == "campaign-schedules" {
			fields = map[string]string{"subject": "一起分享信心與盼望", "body": "聚會於2026年11月8日上午9點開始，歡迎參加。https://www.alive.org.tw/"}
		}
		cases = append(cases, struct {
			ID       string
			Request  Request
			Required string
		}{module + "-en", Request{Module: module, SourceLocale: "zh-Hant", TargetLocale: "en", Fields: fields}, ""})
	}
	for _, deployment := range deployments {
		if strings.TrimSpace(deployment) == "" {
			t.Fatal("empty deployment")
		}
		observer := &evaluationObserver{base: http.DefaultTransport}
		provider := NewAzureOpenAI(os.Getenv("AZURE_OPENAI_ENDPOINT"), deployment, os.Getenv("AZURE_OPENAI_API_KEY"), &http.Client{Transport: observer}, 40*time.Second, os.Getenv("AZURE_OPENAI_RAI_POLICY"))
		for _, tc := range cases {
			tc.Request.ReasoningEffort = os.Getenv("AZURE_OPENAI_REASONING_EFFORT")
			observer.reset()
			started := time.Now()
			result, requestErr := provider.Generate(context.Background(), tc.Request)
			duration := time.Since(started)
			valid := requestErr == nil
			if valid {
				result, valid = checkCMSEvaluationResult(tc.Request, result)
			}
			if tc.Required != "" && !strings.Contains(joinEvaluationFields(result.Fields), tc.Required) {
				valid = false
			}
			row := map[string]any{"deployment": deployment, "reasoningEffort": tc.Request.ReasoningEffort, "case": tc.ID, "protocolAndBoundedChecksPassed": valid, "durationMs": duration.Milliseconds(), "usage": observer, "result": result}
			if json.NewEncoder(file).Encode(row) != nil {
				t.Fatal("evaluation output failed")
			}
			file.Sync()
			if !valid {
				t.Errorf("%s/%s: protocol/bounded check failed, status=%d", deployment, tc.ID, observer.HTTPStatus)
			}
			// Respect the current model's lower request/token budget.
			time.Sleep(3 * time.Second)
		}
	}
}

func checkCMSEvaluationResult(request Request, result Result) (Result, bool) {
	checked, err := applyTitleRule(request, request.Fields, result)
	if err != nil {
		return result, false
	}
	if request.Purpose == "summary" {
		return checked, validSummary(checked.Fields)
	}
	return checked, validResult(request.Module, request.Fields, checked.Fields)
}

func TestCMSEvaluationChecksApplicationTitleRules(t *testing.T) {
	request := Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja", Fields: map[string]string{"title": "432次綠野仙蹤福音餐會 - 春日音樂夜"}}
	result := Result{Fields: map[string]string{"title": "第432回"}, TitleRule: &TitleRuleResult{Kind: "gospel_dinner", Sequence: "432"}}
	if _, valid := checkCMSEvaluationResult(request, result); valid {
		t.Fatal("evaluation accepted a title that the application would reject")
	}
	result.TitleRule.SourceEventName, result.TitleRule.LocalizedEventName = "春日音樂夜", "春の音楽の夕べ"
	if _, valid := checkCMSEvaluationResult(request, result); !valid {
		t.Fatal("valid title rule rejected")
	}
}

func joinEvaluationFields(fields map[string]string) string {
	var out strings.Builder
	for _, text := range fields {
		out.WriteString(text)
	}
	return out.String()
}
