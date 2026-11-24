package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
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
		if !slices.ContainsFunc(models, func(got domain.CLIModel) bool { return got.ID == model.ID && got.Name == model.Name }) {
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
	if !reflect.DeepEqual(models, want) {
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
	if !reflect.DeepEqual(merged, want) {
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
	if !reflect.DeepEqual(models, want) {
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

func TestProbeCLIModelsUsesOfficialEndpointWithoutBaseURL(t *testing.T) {
	h, owner := workbenchFixture(t)
	original := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = original })
	handlers := &usersHandlers{deps: h.deps}
	for _, check := range []struct {
		engine, url string
		anthropic   bool
	}{
		{"codex", "https://api.openai.com/v1", false},
		{"claude-code", "https://api.anthropic.com", true},
		{"kimi", "https://api.kimi.com/coding/v1", false},
		{"dsh", "https://api.deepseek.com", false},
		{"zcode", "https://open.bigmodel.cn/api/coding/paas/v4", false},
	} {
		t.Run(check.engine, func(t *testing.T) {
			called := false
			assistantEngineDiscover = func(_ context.Context, anthropic bool, key, endpoint string) ([]assistantDiscoveredModel, error) {
				called = true
				if anthropic != check.anthropic || endpoint != check.url || key != "fixture-key" {
					t.Fatalf("unexpected probe: Anthropic=%v endpoint=%s", anthropic, endpoint)
				}
				return []assistantDiscoveredModel{{ID: "official-model"}}, nil
			}
			input := bytes.NewBufferString(`{"engine":"` + check.engine + `","apiKey":"fixture-key","baseUrl":""}`)
			c, w := workbenchContext(owner, http.MethodPost, "/", input)
			c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(owner.ID, 10)}}
			handlers.probeCLIModels(c)
			if !called || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"official"`) ||
				!strings.Contains(w.Body.String(), `"id":"official-model"`) || !strings.Contains(w.Body.String(), `"connected":true`) {
				t.Fatalf("result %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProbeCodeBuddyIOAModelsReportsMissingCLI(t *testing.T) {
	h, owner := workbenchFixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", "")
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	original := assistantCLIGlobalDirs
	assistantCLIGlobalDirs = []string{bin}
	t.Cleanup(func() { assistantCLIGlobalDirs = original })

	payload := bytes.NewBufferString(`{"engine":"codebuddy","environment":"ioa"}`)
	c, w := workbenchContext(owner, http.MethodPost, "/", payload)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(owner.ID, 10)}}
	(&usersHandlers{deps: h.deps}).probeCLIModels(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "没有安装") {
		t.Fatalf("missing CodeBuddy CLI: %d %s", w.Code, w.Body.String())
	}
}

func TestDiscoverGeminiModelsKeepsGenerativeIDs(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "fixture-key" {
			http.Error(w, "unexpected model-list request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"models/embedding","supportedGenerationMethods":["embedContent"]},
			{"name":"models/gemini-fixture","displayName":"Gemini Fixture","supportedGenerationMethods":["generateContent"]}
		]}`)
	}))
	t.Cleanup(service.Close)
	models, err := discoverGeminiModels(context.Background(), "fixture-key", service.URL+"/v1beta")
	if err != nil || !reflect.DeepEqual(models, []assistantDiscoveredModel{{ID: "gemini-fixture", Name: "Gemini Fixture"}}) {
		t.Fatalf("models=%+v error=%v", models, err)
	}
}

func TestAntigravityCatalogFollowsCredentialSource(t *testing.T) {
	ctx := context.Background()
	user := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		"agy": {APIKey: "fixture-key", Protocol: domain.AgentCLIProtocolGemini},
	}}
	builtin, _ := assistantEngineModels(ctx, user, nil)
	for id, efforts := range map[string][]string{
		"gemini-4-neon-high":     {"low", "medium", "high", "xhigh"},
		"gemini-4-carbon-medium": {"low", "medium", "high"},
		"gemini-4-argon-medium":  {"low", "medium", "high"},
		"gemini-4-hydrogen-high": {"low", "medium", "high", "xhigh"},
	} {
		var found []string
		for _, model := range builtin {
			if model.ID == "agy:"+id {
				found = model.Efforts
				break
			}
		}
		if !reflect.DeepEqual(found, efforts) {
			t.Errorf("built-in %s efforts = %v, want %v", id, found, efforts)
		}
	}

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/models" && r.Header.Get("x-goog-api-key") == "fixture-key":
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-gateway","displayName":"Gateway Gemini","supportedGenerationMethods":["generateContent"]}]}`)
		case (r.URL.Path == "/models" || r.URL.Path == "/v1/models") && r.Header.Get("Authorization") == "Bearer fixture-key":
			_, _ = io.WriteString(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected Gemini catalog request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(service.Close)
	cred := user.AgentCLICredentials["agy"]
	cred.BaseURL = service.URL
	user.AgentCLICredentials["agy"] = cred
	custom, _ := assistantEngineModels(ctx, user, nil)
	if len(custom) != 1 || custom[0].ID != "agy:gemini-gateway" {
		t.Fatalf("custom endpoint models = %+v", custom)
	}
	cred.Protocol = domain.AgentCLIProtocolChat
	user.AgentCLICredentials["agy"] = cred
	if fallback, _ := assistantEngineModels(ctx, user, nil); len(fallback) != 0 {
		t.Fatalf("an unavailable gateway offered native models: %+v", fallback)
	}
}

func TestCursorModelFamiliesFoldGemini4(t *testing.T) {
	output := `
gemini-4-neon-low - Gemini 4 Neon Low
gemini-4-neon-medium - Gemini 4 Neon Medium
gemini-4-neon-high - Gemini 4 Neon High
gemini-4-neon-xhigh - Gemini 4 Neon Extra High
gemini-4-carbon-low - Gemini 4 Carbon Low
gemini-4-carbon-medium - Gemini 4 Carbon Medium
gemini-4-carbon-high - Gemini 4 Carbon High
gemini-4-hydrogen-low - Gemini 4 Hydrogen Low
gemini-4-hydrogen-medium - Gemini 4 Hydrogen Medium
gemini-4-hydrogen-high - Gemini 4 Hydrogen High
gemini-4-hydrogen-xhigh - Gemini 4 Hydrogen Extra High
gemini-4-argon-low - Gemini 4 Argon Low
gemini-4-argon-medium - Gemini 4 Argon Medium (default)
gemini-4-argon-high - Gemini 4 Argon High
`
	got := cursorModelFamilies(parseCursorModels(output))
	want := []domain.CLIModel{
		{ID: "gemini-4-neon", Name: "Gemini 4 Neon"},
		{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families:\n got %v\nwant %v", got, want)
	}
}
