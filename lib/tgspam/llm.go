package tgspam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/redstone-md/shield/lib/spamcheck"
)

type llmResponse struct {
	IsSpam     bool   `json:"spam"`
	Reason     string `json:"reason"`
	Confidence int    `json:"confidence"`
}

type llmContext struct {
	RequestContext     string
	RecentChatMessages []spamcheck.Request
}

const maxLLMProviderRetries = 20

type llmCheckParams struct {
	Name        string
	ErrorPrefix string
	RetryCount  int
	Msg         string
	History     llmContext
	Send        func(context.Context, string) (llmResponse, error)
}

func runLLMProviderCheck(ctx context.Context, p llmCheckParams) (spam bool, cr spamcheck.Response) {
	retryCount := max(p.RetryCount, 1)
	retryCount = min(retryCount, maxLLMProviderRetries)

	msg := appendHistoryToLLMMessage(p.Msg, p.History)

	var resp llmResponse
	var err error
	for i := 0; i < retryCount; i++ {
		if resp, err = p.Send(ctx, msg); err == nil {
			break
		}
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
	}
	if err != nil {
		return false, spamcheck.Response{
			Spam: false, Name: p.Name, Details: fmt.Sprintf("%s error: %v", p.ErrorPrefix, err), Error: err,
		}
	}

	return resp.IsSpam, spamcheck.Response{
		Spam: resp.IsSpam, Name: p.Name,
		Details: strings.TrimSuffix(resp.Reason, ".") + ", confidence: " + fmt.Sprintf("%d%%", resp.Confidence),
	}
}

func appendHistoryToLLMMessage(msg string, history llmContext) string {
	if history.RequestContext == "" && len(history.RecentChatMessages) == 0 {
		return msg
	}

	var sb strings.Builder
	if history.RequestContext != "" {
		sb.WriteString("Moderation context:\n")
		sb.WriteString(history.RequestContext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Current checked user message:\n")
	sb.WriteString(msg)

	if len(history.RecentChatMessages) > 0 {
		sb.WriteString("\n\nRecent chat messages:\n")
		for i, h := range history.RecentChatMessages {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(fmt.Sprintf("%q: %q", h.UserName, llmHistoryMessage(h)))
		}
	}

	sb.WriteByte('\n')
	return sb.String()
}

func llmHistoryMessage(req spamcheck.Request) string {
	if req.HistoryMsg != "" {
		return req.HistoryMsg
	}
	return req.Msg
}

func parseLLMResponse(content string) (llmResponse, error) {
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
