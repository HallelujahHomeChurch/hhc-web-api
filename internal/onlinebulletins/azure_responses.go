package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Shared bounded Azure transport; callers validate their own typed result.
func (c *AzureExtractor) respond(parent context.Context, payload []byte, result any) (RecognitionUsage, error) {
	var usage RecognitionUsage
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/openai/v1/responses", bytes.NewReader(payload))
	if err != nil {
		return usage, ErrRecognition
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("api-key", c.apiKey)
	if c.policy != "" {
		request.Header.Set("x-policy-id", c.policy)
	}
	started := time.Now()
	response, err := c.client.Do(request)
	usage.Requests, usage.Duration = 1, time.Since(started)
	if err != nil {
		if ctx.Err() != nil {
			return usage, ctx.Err()
		}
		return usage, ErrRecognition
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	usage.Duration = time.Since(started)
	if err != nil && ctx.Err() != nil {
		return usage, ctx.Err()
	}
	if err != nil || len(data) > 4<<20 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return usage, ErrRecognition
	}
	var envelope struct {
		Status     string          `json:"status"`
		Model      string          `json:"model"`
		Error      json.RawMessage `json:"error"`
		Incomplete json.RawMessage `json:"incomplete_details"`
		Output     []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Content []struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			Input   *int64 `json:"input_tokens"`
			Output  *int64 `json:"output_tokens"`
			Details struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &envelope) != nil || !recognitionModelName.MatchString(envelope.Model) || envelope.Usage.Input == nil || envelope.Usage.Output == nil {
		return usage, ErrRecognition
	}
	usage = RecognitionUsage{Model: envelope.Model, InputTokens: *envelope.Usage.Input, OutputTokens: *envelope.Usage.Output, ReasoningTokens: envelope.Usage.Details.Reasoning, Requests: 1, Duration: time.Since(started)}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 || usage.ReasoningTokens > usage.OutputTokens {
		return RecognitionUsage{}, ErrRecognition
	}
	if envelope.Status != "completed" || presentJSON(envelope.Error) || presentJSON(envelope.Incomplete) {
		return usage, ErrRecognition
	}
	var output *string
	for _, item := range envelope.Output {
		if item.Status != "" && item.Status != "completed" {
			return usage, ErrRecognition
		}
		if item.Type == "reasoning" && len(item.Content) == 0 {
			continue
		}
		if item.Type != "message" {
			return usage, ErrRecognition
		}
		for _, content := range item.Content {
			if content.Type != "output_text" || content.Text == nil || output != nil {
				return usage, ErrRecognition
			}
			output = content.Text
		}
	}
	if output == nil || !utf8.ValidString(*output) {
		return usage, ErrRecognition
	}
	decoder := json.NewDecoder(strings.NewReader(*output))
	decoder.DisallowUnknownFields()
	if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
		return usage, ErrRecognition
	}
	return usage, nil
}
