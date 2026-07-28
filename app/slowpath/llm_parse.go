package slowpath

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type llmResponse struct {
	IsSpam     bool   `json:"spam"`
	Reason     string `json:"reason"`
	Confidence int    `json:"confidence"`
}

func parseLLMOutput(content string) (llmResponse, error) {
	clean := strings.TrimSpace(content)
	if clean == "" {
		return llmResponse{}, fmt.Errorf("empty response")
	}

	var wire struct {
		IsSpam     *bool   `json:"spam"`
		Reason     *string `json:"reason"`
		Confidence *int    `json:"confidence"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(clean))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return llmResponse{}, fmt.Errorf("decode strict LLM response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return llmResponse{}, fmt.Errorf("response must contain exactly one JSON object")
	}
	if wire.IsSpam == nil || wire.Reason == nil || wire.Confidence == nil {
		return llmResponse{}, fmt.Errorf("spam, reason, and confidence are required")
	}
	reason := strings.TrimSpace(*wire.Reason)
	if reason == "" {
		return llmResponse{}, fmt.Errorf("reason is required")
	}
	if *wire.Confidence < 1 || *wire.Confidence > 100 {
		return llmResponse{}, fmt.Errorf("confidence must be between 1 and 100")
	}
	if *wire.IsSpam && *wire.Confidence <= 80 {
		return llmResponse{}, fmt.Errorf("spam decision requires confidence above 80")
	}
	return llmResponse{IsSpam: *wire.IsSpam, Reason: reason, Confidence: *wire.Confidence}, nil
}
