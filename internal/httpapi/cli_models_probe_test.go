package httpapi

import (
	"os"
	"path/filepath"
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

func TestParseDshCatalog(t *testing.T) {
	data := []byte(`{
  "openai-completions": {
    "chat:deepseek-flash": {"id": "deepseek-flash", "name": "DeepSeek V4.1 Flash"},
    "chat:deepseek-v4-pro": {"id": "deepseek-v4-pro", "name": "DeepSeek V4 Pro"}
  },
  "anthropic-messages": {
    "chat:deepseek-flash": {"id": "deepseek-flash", "name": "重复条目按出现顺序折叠"},
    "chat:legacy-alias": {"name": "Legacy Alias"}
  }
}`)
	models := parseDshCatalog(data)
	want := []domain.CLIModel{
		{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash"},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
		{ID: "legacy-alias", Name: "Legacy Alias"},
	}
	if !slices.Equal(models, want) {
		t.Fatalf("catalog: got %v want %v", models, want)
	}
	if models := parseDshCatalog([]byte("not json")); len(models) != 0 {
		t.Fatalf("garbage input: %v", models)
	}
}

func TestMergeCatalogModels(t *testing.T) {
	floor := []domain.CLIModel{{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash"}}
	live := []domain.CLIModel{
		{ID: "deepseek-flash", Name: "线上同名条目不覆盖底座的名字"},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
	}
	merged := mergeCatalogModels(floor, live)
	want := []domain.CLIModel{
		{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash"},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
	}
	if !slices.Equal(merged, want) {
		t.Fatalf("merged: got %v want %v", merged, want)
	}
}

func TestNpmPackageRoot(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh", "lib")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(pkg, "bin.js")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env node\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "node_modules", "@deepseek-ai", "dsh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := npmPackageRoot(bin, "@deepseek-ai"); got != want {
		t.Fatalf("package root: got %q want %q", got, want)
	}
	if got := npmPackageRoot(bin, "@earendil-works"); got != "" {
		t.Fatalf("wrong scope must not match: got %q", got)
	}
	if got := npmPackageRoot(filepath.Join(root, "elsewhere", "dsh"), "@deepseek-ai"); got != "" {
		t.Fatalf("binary outside the scope: got %q", got)
	}
}

func TestPiAiDataDir(t *testing.T) {
	payload := []byte(`{"openai-completions":{"chat:m":{"id":"m","name":"M"}}}`)
	rel := filepath.Join("dist", "providers", "data")
	root := t.TempDir()
	nested := filepath.Join(root, "node_modules", "@earendil-works", "pi-ai", rel)
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deepseek.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := piAiDataDir(root); got != nested {
		t.Fatalf("nested copy: got %q", got)
	}
	// npm may hoist pi-ai into the node_modules next to the package instead.
	if err := os.RemoveAll(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	hoisted := filepath.Join(filepath.Dir(filepath.Dir(root)), "@earendil-works", "pi-ai", rel)
	if err := os.MkdirAll(hoisted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hoisted, "deepseek.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := piAiDataDir(root); got != hoisted {
		t.Fatalf("hoisted copy: got %q", got)
	}
	if got := piAiDataDir(filepath.Join(root, "no-such-package")); got != "" {
		t.Fatalf("missing package must read as no data dir: got %q", got)
	}
}

func TestParsePiAiCatalog(t *testing.T) {
	data := []byte(`{
  "openai-completions": {
    "chat:deepseek-flash": {"id": "deepseek-flash", "name": "DeepSeek V4.1 Flash", "baseUrl": "https://api.deepseek.com"},
    "chat:gw-model": {"id": "gw-model", "name": "Gateway Model", "baseUrl": "https://gw.example.com/v1"},
    "chat:no-base": {"id": "no-base", "name": "No Base URL"}
  },
  "anthropic-messages": {
    "chat:deepseek-flash": {"id": "deepseek-flash", "name": "重复条目按出现顺序折叠", "baseUrl": "https://api.deepseek.com"}
  }
}`)
	models := parsePiAiCatalog(data, "https://api.deepseek.com")
	want := []domain.CLIModel{{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash"}}
	if !slices.Equal(models, want) {
		t.Fatalf("origin match: got %v want %v", models, want)
	}
	// The origin ignores path prefixes like /v1.
	if models := parsePiAiCatalog(data, "https://gw.example.com"); len(models) != 1 || models[0].ID != "gw-model" {
		t.Fatalf("pathless origin match: %v", models)
	}
	if models := parsePiAiCatalog(data, ""); len(models) != 3 {
		t.Fatalf("empty origin keeps every entry: %v", models)
	}
	if models := parsePiAiCatalog([]byte("not json"), ""); len(models) != 0 {
		t.Fatalf("garbage input: %v", models)
	}
}

func TestParseGeminiModels(t *testing.T) {
	data := []byte(`{
  "models": [
    {"name": "models/gemini-5-flash", "displayName": "Gemini 5 Flash", "supportedGenerationMethods": ["generateContent"]},
    {"name": "models/text-embedding-9", "displayName": "Embeddings", "supportedGenerationMethods": ["embedContent"]},
    {"name": "models/gemini-5-pro", "supportedGenerationMethods": ["generateContent"]}
  ],
  "nextPageToken": "page-2"
}`)
	models, next := parseGeminiModels(data)
	want := []assistantDiscoveredModel{
		{ID: "gemini-5-flash", Name: "Gemini 5 Flash"},
		{ID: "gemini-5-pro", Name: "gemini-5-pro"},
	}
	if !slices.Equal(models, want) {
		t.Fatalf("models: got %v want %v", models, want)
	}
	if next != "page-2" {
		t.Fatalf("next page token: %q", next)
	}
	if models, next := parseGeminiModels([]byte("not json")); models != nil || next != "" {
		t.Fatalf("garbage input: %v %q", models, next)
	}
}
