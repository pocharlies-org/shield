package slowpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLLMOutputValid(t *testing.T) {
	resp, err := parseLLMOutput(`{"spam":true,"reason":"crypto","confidence":95}`)
	require.NoError(t, err)
	assert.True(t, resp.IsSpam)
	assert.Equal(t, "crypto", resp.Reason)
	assert.Equal(t, 95, resp.Confidence)
}

func TestParseLLMOutputHam(t *testing.T) {
	resp, err := parseLLMOutput(`{"spam":false,"reason":"clean","confidence":10}`)
	require.NoError(t, err)
	assert.False(t, resp.IsSpam)
}

func TestParseLLMOutputEmpty(t *testing.T) {
	_, err := parseLLMOutput("")
	assert.Error(t, err)
}

func TestParseLLMOutputGarbage(t *testing.T) {
	_, err := parseLLMOutput("not json at all")
	assert.Error(t, err)
}

func TestParseLLMOutputRejectsUnsafeFormats(t *testing.T) {
	cases := map[string]string{
		"trailing comma":  `{"spam":true,"reason":"spam","confidence":90,}`,
		"thought wrapper": `<think>analysis</think>{"spam":false,"reason":"ok","confidence":20}`,
		"regex prose":     `The message is spam: true, reason: "crypto ad", confidence: 85`,
		"unknown field":   `{"spam":true,"reason":"spam","confidence":90,"action":"ban"}`,
		"missing spam":    `{"reason":"spam","confidence":90}`,
		"missing reason":  `{"spam":true,"confidence":90}`,
		"missing score":   `{"spam":false,"reason":"clean"}`,
		"blank reason":    `{"spam":true,"reason":" ","confidence":90}`,
		"zero confidence": `{"spam":false,"reason":"clean","confidence":0}`,
		"out of range":    `{"spam":true,"reason":"spam","confidence":101}`,
		"low confidence":  `{"spam":true,"reason":"spam","confidence":80}`,
		"multiple values": `{"spam":false,"reason":"ok","confidence":20} {}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseLLMOutput(input)
			require.Error(t, err)
		})
	}
}
