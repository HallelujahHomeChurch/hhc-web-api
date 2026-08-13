package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

var (
	ErrProvider        = errors.New("translation provider error")
	ErrTimeout         = errors.New("translation provider timeout")
	ErrContentFiltered = errors.New("translation content filtered")
)

type AzureOpenAI struct {
	endpoint   string
	deployment string
	apiKey     string
	policyName string
	httpClient *http.Client
	timeout    time.Duration
}

func NewAzureOpenAI(endpoint, deployment, apiKey string, httpClient *http.Client, timeout time.Duration, policyName string) *AzureOpenAI {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	privateClient := *httpClient
	privateClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if timeout <= 0 {
		timeout = 40 * time.Second
	}
	return &AzureOpenAI{endpoint: strings.TrimRight(endpoint, "/"), deployment: deployment, apiKey: apiKey, policyName: strings.TrimSpace(policyName), httpClient: &privateClient, timeout: timeout}
}

func (c *AzureOpenAI) Generate(ctx context.Context, request Request) (Result, error) {
	body, err := c.requestBody(request)
	if err != nil {
		return Result{}, ErrProvider
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.endpoint+"/openai/v1/responses", bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrProvider
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("api-key", c.apiKey)
	if c.policyName != "" {
		httpRequest.Header.Set("x-policy-id", c.policyName)
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		var netError net.Error
		if requestContext.Err() != nil || errors.As(err, &netError) && netError.Timeout() {
			return Result{}, ErrTimeout
		}
		return Result{}, ErrProvider
	}
	defer response.Body.Close()
	responseBody, readErr := readBounded(response.Body)
	if readErr != nil {
		return Result{}, ErrProvider
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if isContentFiltered(responseBody) {
			return Result{}, ErrContentFiltered
		}
		return Result{}, ErrProvider
	}
	return parseResponse(responseBody, request.Fields)
}

func (c *AzureOpenAI) requestBody(request Request) ([]byte, error) {
	keys := make([]string, 0, len(request.Fields))
	properties := make(map[string]map[string]string, len(request.Fields))
	for key := range request.Fields {
		keys = append(keys, key)
		properties[key] = map[string]string{"type": "string"}
	}
	sort.Strings(keys)
	source, err := json.Marshal(struct {
		Module       string            `json:"module"`
		SourceLocale string            `json:"sourceLocale"`
		TargetLocale string            `json:"targetLocale"`
		Fields       map[string]string `json:"fields"`
	}{request.Module, request.SourceLocale, request.TargetLocale, request.Fields})
	if err != nil {
		return nil, err
	}
	payload := struct {
		Model        string `json:"model"`
		Store        bool   `json:"store"`
		Background   bool   `json:"background"`
		Instructions string `json:"instructions"`
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
					Type                 string                       `json:"type"`
					Properties           map[string]map[string]string `json:"properties"`
					Required             []string                     `json:"required"`
					AdditionalProperties bool                         `json:"additionalProperties"`
				} `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}{Model: c.deployment, Store: false, Background: false, Instructions: translationInstructions(request.Module, request.TargetLocale)}
	payload.Input = make([]struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}, 1)
	payload.Input[0].Role = "user"
	payload.Input[0].Content = make([]struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}, 1)
	payload.Input[0].Content[0].Type = "input_text"
	payload.Input[0].Content[0].Text = "SOURCE DATA:\n" + string(source)
	payload.Text.Format.Type = "json_schema"
	payload.Text.Format.Name = "cms_translation"
	payload.Text.Format.Strict = true
	payload.Text.Format.Schema.Type = "object"
	payload.Text.Format.Schema.Properties = properties
	payload.Text.Format.Schema.Required = keys
	payload.Text.Format.Schema.AdditionalProperties = false
	return json.Marshal(payload)
}

func readBounded(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, ErrProvider
	}
	return body, nil
}

func parseResponse(body []byte, requested map[string]string) (Result, error) {
	var response struct {
		Status            string          `json:"status"`
		Error             json.RawMessage `json:"error"`
		IncompleteDetails json.RawMessage `json:"incomplete_details"`
		ContentFilters    []struct {
			Blocked bool `json:"blocked"`
		} `json:"content_filters"`
		Output []struct {
			Status  string `json:"status"`
			Content []struct {
				Type    string  `json:"type"`
				Text    *string `json:"text"`
				Refusal string  `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
		OutputText *string `json:"output_text"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Result{}, ErrProvider
	}
	if isContentFiltered(response.Error) || isContentFiltered(response.IncompleteDetails) {
		return Result{}, ErrContentFiltered
	}
	if response.Status != "completed" || hasJSONValue(response.Error) || hasJSONValue(response.IncompleteDetails) {
		return Result{}, ErrProvider
	}
	for _, filter := range response.ContentFilters {
		if filter.Blocked {
			return Result{}, ErrContentFiltered
		}
	}
	var output strings.Builder
	foundOutput := false
	for _, item := range response.Output {
		if item.Status != "" && item.Status != "completed" {
			return Result{}, ErrProvider
		}
		for _, content := range item.Content {
			switch content.Type {
			case "output_text":
				if content.Text == nil {
					return Result{}, ErrProvider
				}
				foundOutput = true
				output.WriteString(*content.Text)
			case "refusal":
				return Result{}, ErrProvider
			default:
				return Result{}, ErrProvider
			}
		}
	}
	outputText := output.String()
	if !foundOutput && response.OutputText != nil {
		outputText = *response.OutputText
	}
	fields, err := decodeFields(outputText, requested)
	if err != nil {
		return Result{}, ErrProvider
	}
	return Result{Fields: fields}, nil
}

func isContentFiltered(body []byte) bool {
	var value struct {
		Code       string `json:"code"`
		Reason     string `json:"reason"`
		InnerError struct {
			Code string `json:"code"`
		} `json:"innererror"`
		Error *struct {
			Code       string `json:"code"`
			InnerError struct {
				Code string `json:"code"`
			} `json:"innererror"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	return value.Code == "content_filter" || value.InnerError.Code == "ContentFiltered" || value.Reason == "content_filter" || value.Error != nil && (value.Error.Code == "content_filter" || value.Error.InnerError.Code == "ContentFiltered")
}

func hasJSONValue(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && !bytes.Equal(value, []byte("null"))
}

func decodeFields(output string, requested map[string]string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &raw); err != nil || len(raw) != len(requested) {
		return nil, ErrProvider
	}
	fields := make(map[string]string, len(raw))
	for key := range requested {
		value, ok := raw[key]
		if !ok {
			return nil, ErrProvider
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, ErrProvider
		}
		fields[key] = text
	}
	return fields, nil
}
