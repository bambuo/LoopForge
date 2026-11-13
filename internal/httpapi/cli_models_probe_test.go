package httpapi

import (
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestParseQoderModels(t *testing.T) {
	out := `MODEL
Efficient
deepseek-flash (qoder-custom-659a7d42-8726-4a49-bb85-751111111111)
gpt-6-luna (qoder-custom-66164e2b-0649-4a0c-90a2-5e2222222222)
deepseek-flash (qoder-custom-659a7d42-8726-4a49-bb85-751111111111)
auto (qoder-auto)
`
	models := parseQoderModels(out)
	want := []domain.CLIModel{
		{ID: "Efficient", Name: "Efficient"},
		{ID: "qoder-custom-659a7d42-8726-4a49-bb85-751111111111", Name: "deepseek-flash"},
		{ID: "qoder-custom-66164e2b-0649-4a0c-90a2-5e2222222222", Name: "gpt-6-luna"},
		{ID: "qoder-auto", Name: "auto"},
	}
	if len(models) != len(want) {
		t.Fatalf("models: %v", models)
	}
	for _, model := range want {
		if !slices.Contains(models, model) {
			t.Fatalf("missing %v in %v", model, models)
		}
	}
	// The MODEL header is dropped; bare lines are built-in tier models (e.g.
	// Efficient, the only usable model on some accounts).
	if models := parseQoderModels("MODEL\nEfficient\n"); len(models) != 1 || models[0].ID != "Efficient" {
		t.Fatalf("bare tier model: %v", models)
	}
}

func TestAssistantAuthFailureTextRecognizesQoderRejection(t *testing.T) {
	for _, text := range []string{
		"Failed to authenticate: personal access token was rejected",
		"Authentication failed",
		"HTTP 401 Unauthorized",
	} {
		if !assistantAuthFailureText(text) {
			t.Fatalf("auth failure not recognized: %q", text)
		}
	}
	if assistantAuthFailureText("connection reset by peer") {
		t.Fatal("transport error misread as an auth failure")
	}
}
