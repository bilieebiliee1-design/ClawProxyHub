package requestutil_test

import (
	"testing"

	"io.nexport.gateway/core/sdk/anthropicup"
	"io.nexport.gateway/core/sdk/openaiup"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
	"io.nexport.gateway/core/sdk/responsesup"
)

func TestDisabledThinkingAcrossBuilders(t *testing.T) {
	req := &pb.ChatRequest{Model: "test", Extra: map[string]string{
		"thinking":                `{"type":"disabled"}`,
		"anthropic_output_config": `{"effort":"high"}`,
	}}
	if got := openaiup.ChatBody(req)["reasoning_effort"]; got != "none" {
		t.Fatalf("Chat effort = %v", got)
	}
	if got := responsesup.ChatBody(req)["reasoning"].(map[string]interface{})["effort"]; got != "none" {
		t.Fatalf("Responses effort = %v", got)
	}
	if got := anthropicup.ChatBody(req)["thinking"].(map[string]interface{})["type"]; got != "disabled" {
		t.Fatalf("Anthropic thinking = %v", got)
	}
}
