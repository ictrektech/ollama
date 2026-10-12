package openai

import (
	"github.com/ollama/ollama/api"
	"testing"
)

func TestGatewayNativeOptions(t *testing.T) {
	n := 5
	legacy := 9
	chat, err := FromChatRequest(ChatCompletionRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: &legacy, MaxCompletionTokens: &n, Options: map[string]any{"num_ctx": 8192, "temperature": 0.25, "num_predict": 12}})
	if err != nil {
		t.Fatal(err)
	}
	if chat.Options["num_ctx"] != 8192 || chat.Options["num_predict"] != 12 || chat.Options["temperature"] != 0.25 {
		t.Fatalf("options=%v", chat.Options)
	}
	keep := &api.Duration{}
	gen, err := FromCompleteRequest(CompletionRequest{Model: "test", Prompt: "hello", KeepAlive: keep, Options: map[string]any{"num_ctx": 4096}})
	if err != nil || gen.Options["num_ctx"] != 4096 || gen.KeepAlive != keep {
		t.Fatalf("generate=%+v err=%v", gen, err)
	}
}

func TestGatewayReasoningAlias(t *testing.T) {
	chat, err := FromChatRequest(ChatCompletionRequest{Model: "test", Messages: []Message{{Role: "assistant", Content: "answer", ReasoningContent: "thought"}}})
	if err != nil || chat.Messages[0].Thinking != "thought" {
		t.Fatalf("chat=%v err=%v", chat, err)
	}
	out := ToChatCompletion("test", api.ChatResponse{Message: api.Message{Role: "assistant", Content: "answer", Thinking: "thought"}})
	if out.Choices[0].Message.ReasoningContent != "thought" {
		t.Fatal("gateway response alias missing")
	}
}
