package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

type cliModelProbeResult struct {
	Models []domain.CLIModel `json:"models"`
	// Source identifies the configured gateway, official provider, Runner or built-in catalog.
	Source string `json:"source,omitempty"`
	// Connected means the provider answered a live model request.
	Connected bool   `json:"connected,omitempty"`
	Error     string `json:"error,omitempty"`
}

const assistantAnthropicAPIBase = "https://api.anthropic.com"

// probeCLIModels backs 「获取可用模型」 for a personal model catalog.
// The form's unsaved API Key, Base URL and protocol win over the stored ones
// so a user can probe before saving; nothing is stored. Like
// verifyGitToken it answers 200 with a structured result.
func (h *usersHandlers) probeCLIModels(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, cliModelProbeResult{Error: "只能使用当前登录用户的凭据获取模型"})
		return
	}
	var req struct {
		Engine string `json:"engine"`
		APIKey string `json:"apiKey"`
		// BaseURL is the form's value; null falls back to the stored one,
		// while "" means the official endpoint.
		BaseURL *string `json:"baseUrl"`
		// Protocol is the agent CLI's form value; null keeps the stored one.
		Protocol    *string `json:"protocol"`
		Environment *string `json:"environment"`
	}
	if !decode(c, &req) {
		return
	}
	engine := strings.TrimSpace(req.Engine)
	if engine != assistantEngineCodeBuddy && !slices.Contains(assistantCLIEngines(), engine) {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "不支持的 CLI：" + engine})
		return
	}
	if req.BaseURL != nil && !validCLIBaseURL(*req.BaseURL) {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: errInvalidCLIBaseURL})
		return
	}
	apiKey, baseURL, protocol, environment := strings.TrimSpace(req.APIKey), "", "", ""
	if h.repo != nil {
		if stored, found := h.repo.User(id); found {
			storedKey, storedURL := assistantEngineCredential(stored, engine)
			if apiKey == "" {
				apiKey = storedKey
			}
			baseURL, protocol = storedURL, assistantEngineProtocol(stored, engine)
			environment = stored.CodeBuddyInternetEnvironment
		}
	}
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	configuredBaseURL := baseURL
	if req.Protocol != nil && runtime.IsAgentCLI(engine) {
		protocol = runtime.AgentCLIProtocol(engine, *req.Protocol)
	}
	c.Header("Cache-Control", "private, no-store")
	if engine == assistantEngineCodeBuddy && req.Environment != nil {
		var ok bool
		environment, ok = domain.NormalizeCodeBuddyInternetEnvironment(*req.Environment)
		if !ok {
			c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "请选择有效的 CodeBuddy 网络环境"})
			return
		}
	}
	if engine == assistantEngineCodeBuddy && baseURL == "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		var models []domain.CLIModel
		for _, id := range discoverCLIModels(ctx, environment, apiKey) {
			if !codeBuddyIsDefaultModel(id) && assistantValidEngineModel(id) {
				models = append(models, domain.CLIModel{ID: id})
			}
		}
		if len(models) == 0 {
			if _, err := resolveAssistantEngineBin(engine); errors.Is(err, errAssistantCLIMissing) {
				c.JSON(http.StatusOK, cliModelProbeResult{Error: err.Error()})
				return
			}
			c.JSON(http.StatusOK, cliModelProbeResult{Error: "CodeBuddy CLI 没有返回模型，请检查 CLI 和个人凭据"})
			return
		}
		source := "official"
		if apiKey == "" {
			source = "builtin"
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, models, ""), Source: source})
		return
	}
	if engine == runtime.AgentCLICursor {
		h.probeCursorModels(c, apiKey)
		return
	}
	if engine == runtime.AgentCLIQoder {
		h.probeQoderModels(c, apiKey)
		return
	}
	if baseURL == "" && apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, cliModelsOf(assistantEngineBuiltinModels(engine)), ""), Source: "builtin"})
		return
	}
	if engine == runtime.AgentCLIPi && baseURL != "" {
		// Pi rides a user Base URL; the installed CLI's own provider data
		// floors the endpoint's published list when it knows the service.
		h.probePiModels(c, apiKey, baseURL, protocol)
		return
	}
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(engine) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	source := "remote"
	anthropic := assistantEngineAnthropicModels(engine, protocol)
	if baseURL == "" {
		source = "official"
		switch engine {
		case assistantEngineCodex:
			baseURL = "https://api.openai.com/v1"
		case assistantEngineClaude:
			baseURL = assistantAnthropicAPIBase
		case runtime.AgentCLIKimi:
			baseURL, anthropic = "https://api.kimi.com/coding/v1", false
		case runtime.AgentCLIDeepSeek:
			baseURL, anthropic = "https://api.deepseek.com", false
		case runtime.AgentCLIZCode:
			baseURL, anthropic = "https://open.bigmodel.cn/api/coding/paas/v4", false
		}
	}
	var models []assistantDiscoveredModel
	if engine == runtime.AgentCLIAntigravity && protocol == runtime.AgentCLIProtocolGemini {
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com"
		}
		models, err = discoverGeminiModels(ctx, apiKey, baseURL)
	} else if baseURL != "" {
		models, err = assistantEngineDiscover(ctx, anthropic, apiKey, baseURL)
	} else {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先在个人配置中填写模型服务的 Base URL"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Connected: true, Error: "服务没有返回可用模型，可以手动添加模型"})
		return
	}
	// The installed CLI's own provider catalog floors live results: DeepSeek
	// Harness ships pi-ai provider data and Pi matches the Base URL origin
	// against its bundled catalogs.
	out := cliModelsOf(models)
	switch engine {
	case runtime.AgentCLIDeepSeek:
		out = mergeCatalogModels(dshCatalogModels(), out)
	case runtime.AgentCLIPi:
		out = mergeCatalogModels(piCatalogModelsForBaseURL(configuredBaseURL), out)
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, out, configuredBaseURL), Source: source, Connected: true})
}

func discoverGeminiModels(ctx context.Context, key, baseURL string) ([]assistantDiscoveredModel, error) {
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return discoverGeminiModelsWithClient(ctx, &client, key, baseURL)
}

func discoverGeminiModelsWithClient(ctx context.Context, client *http.Client, key, baseURL string) ([]assistantDiscoveredModel, error) {
	if client == nil {
		c := *http.DefaultClient
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &c
	}
	baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1beta")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1beta/models?pageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("Gemini 模型接口返回重定向 HTTP %d，已拒绝跟随跳转", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini 模型接口返回 HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var models []assistantDiscoveredModel
	for _, item := range payload.Models {
		id := strings.TrimPrefix(item.Name, "models/")
		if assistantValidEngineModel(id) && slices.Contains(item.Methods, "generateContent") {
			models = append(models, assistantDiscoveredModel{ID: id, Name: item.DisplayName})
			if len(models) == assistantDiscoveryMaxModels {
				break
			}
		}
	}
	return models, nil
}

// probeCursorModels asks cursor-agent itself: Cursor has no Base URL, and
// the models a key may use depend on its account plan.
func (h *usersHandlers) probeCursorModels(c *gin.Context, apiKey string) {
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(runtime.AgentCLICursor) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	models, err := cursorAgentModels(ctx, apiKey)
	if errors.Is(err, errAssistantCLIMissing) {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLICursor, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLICursor)), ""), Source: "builtin"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "Cursor CLI 没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLICursor, models, ""), Source: "official"})
}

var cursorModelLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*) - (.+)$`)

// probeQoderModels asks the qoder CLI itself: Qoder has no Base URL, and the
// models behind a PAT depend on the account (official tiers plus BYOK rows).
func (h *usersHandlers) probeQoderModels(c *gin.Context, apiKey string) {
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIQoder, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIQoder)), ""), Source: "builtin"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	models, err := qoderAgentModels(ctx, apiKey)
	if errors.Is(err, errAssistantCLIMissing) {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIQoder, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIQoder)), ""), Source: "builtin"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		// The CLI only lists the account's BYOK models; none means the
		// official "auto" model is all the account offers.
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIQoder, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIQoder)), ""), Source: "builtin"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIQoder, models, ""), Source: "remote"})
}

var qoderModelLine = regexp.MustCompile(`^(.+?)\s*\(([^()]+)\)$`)

// qoderAgentModels runs `qoder --list-models` in a throwaway HOME with only
// the user's PAT, so no host Qoder login or configuration is visible to it.
func qoderAgentModels(ctx context.Context, apiKey string) ([]domain.CLIModel, error) {
	bin, err := resolveAssistantEngineBin(runtime.AgentCLIQoder)
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "agentflow-qoder-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	// A fresh settings file keeps the CLI from auto-updating mid-probe.
	if err := os.MkdirAll(filepath.Join(home, ".qoder"), 0o700); err == nil {
		settings, _ := json.Marshal(map[string]any{"autoUpdates": false})
		_ = os.WriteFile(filepath.Join(home, ".qoder", "settings.json"), settings, 0o600)
	}
	env := []string{
		"HOME=" + home, "QODER_CONFIG_DIR=" + filepath.ToSlash(filepath.Join(home, ".qoder")),
		"QODER_PERSONAL_ACCESS_TOKEN=" + apiKey, "QODERCN_PERSONAL_ACCESS_TOKEN=" + apiKey,
		"NO_COLOR=1", "CI=1",
	}
	for _, key := range []string{"PATH", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	cmd := exec.CommandContext(ctx, bin, "--list-models")
	cmd.Dir, cmd.Env = home, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Qoder CLI 响应超时")
		}
		text := strings.ToLower(string(out))
		if strings.Contains(text, "not logged in") || assistantAuthFailureText(string(out)) {
			return nil, assistantCredentialError{engine: runtime.AgentCLIQoder}
		}
		return nil, fmt.Errorf("qoder --list-models 退出：%v", err)
	}
	return parseQoderModels(string(out)), nil
}

// parseQoderModels reads `qoder --list-models`: the literal "MODEL" header,
// bare built-in tier names (e.g. Efficient — the only usable model on some
// accounts) and `name (model-id)` BYOK rows.
func parseQoderModels(out string) []domain.CLIModel {
	var models []domain.CLIModel
	seen := map[string]bool{}
	add := func(id, name string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, domain.CLIModel{ID: id, Name: strings.TrimSpace(name)})
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "MODEL" {
			continue
		}
		if match := qoderModelLine.FindStringSubmatch(line); match != nil {
			add(match[2], match[1])
			continue
		}
		add(line, line)
	}
	return models
}

// dshCatalogModels reads the official provider catalog the installed DeepSeek
// Harness ships for itself (@earendil-works/pi-ai provider data), so the probe
// lists what this dsh build can actually drive instead of a table hardcoded
// here. Anything unreadable just yields no catalog.
func dshCatalogModels() []domain.CLIModel {
	bin, err := resolveAssistantEngineBin(runtime.AgentCLIDeepSeek)
	if err != nil {
		return nil
	}
	dir := piAiDataDir(npmPackageRoot(bin, "@deepseek-ai"))
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "deepseek.json"))
	if err != nil {
		return nil
	}
	return parseDshCatalog(data)
}

// piAiDataDir locates the pi-ai provider data directory shipped inside a
// package root: the package-nested copy first, the hoisted sibling second —
// npm may place pi-ai next to the package instead of nesting it.
func piAiDataDir(root string) string {
	rel := filepath.Join("dist", "providers", "data")
	for _, dir := range []string{
		filepath.Join(root, "node_modules", "@earendil-works", "pi-ai", rel),
		filepath.Join(filepath.Dir(filepath.Dir(root)), "@earendil-works", "pi-ai", rel),
	} {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			return dir
		}
	}
	return ""
}

// npmPackageRoot resolves an npm package directory behind a launcher binary:
// npm and on-demand installs put the real entry inside the package tree, so
// walking up from it lands on the package root under scope (e.g.
// "@deepseek-ai", "@earendil-works").
func npmPackageRoot(bin, scope string) string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	for dir := filepath.Dir(real); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == scope && filepath.Base(filepath.Dir(parent)) == "node_modules" {
			return dir
		}
	}
	return ""
}

// parseDshCatalog reads pi-ai's provider data: API groups
// ("openai-completions", …) of model entries keyed "chat:<id>", flattened to
// id/name pairs in file order with duplicates folded. The entry's own id
// wins over the key suffix.
func parseDshCatalog(data []byte) []domain.CLIModel {
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil { // '{'
		return nil
	}
	var out []domain.CLIModel
	seen := map[string]bool{}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // API group name
			return out
		}
		if _, err := dec.Token(); err != nil { // '{' of the group
			return out
		}
		for dec.More() {
			key, _ := dec.Token()
			var entry struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := dec.Decode(&entry); err != nil {
				return out
			}
			id := entry.ID
			if id == "" {
				id = key.(string)
				if i := strings.LastIndexByte(id, ':'); i >= 0 {
					id = id[i+1:]
				}
			}
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, domain.CLIModel{ID: id, Name: strings.TrimSpace(entry.Name)})
		}
		if _, err := dec.Token(); err != nil { // '}' of the group
			return out
		}
	}
	return out
}

// mergeCatalogModels keeps the floor's order and names and appends the live
// list's additions, so a model the service just released shows up even though
// the deterministic floor doesn't know it yet.
func mergeCatalogModels(floor, live []domain.CLIModel) []domain.CLIModel {
	seen := map[string]bool{}
	for _, model := range floor {
		seen[model.ID] = true
	}
	out := floor
	for _, model := range live {
		if !seen[model.ID] {
			seen[model.ID] = true
			out = append(out, model)
		}
	}
	return out
}

// probePiModels answers 「获取可用模型」 for Pi behind a user Base URL: the
// endpoint's published model list fetched live, floored by the installed
// CLI's own pi-ai provider data when it knows the service.
func (h *usersHandlers) probePiModels(c *gin.Context, apiKey, baseURL, protocol string) {
	floor := piCatalogModelsForBaseURL(baseURL)
	if apiKey == "" {
		if len(floor) > 0 {
			c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIPi, floor, ""), Source: "remote"})
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(runtime.AgentCLIPi) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	live, err := assistantEngineDiscover(ctx, assistantEngineAnthropicModels(runtime.AgentCLIPi, protocol), apiKey, baseURL)
	if err != nil || len(live) == 0 {
		if len(floor) > 0 {
			// The endpoint didn't answer; the CLI's catalog is still a
			// deterministic list for the services it knows.
			c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIPi, floor, ""), Source: "remote"})
			return
		}
		if err != nil {
			c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIPi, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIPi)), ""), Source: "builtin"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLIPi, mergeCatalogModels(floor, cliModelsOf(live)), ""), Source: "remote"})
}

// piCatalogModelsForBaseURL reads the installed Pi's pi-ai provider data and
// keeps the models whose provider baseUrl shares the Base URL's origin, so
// official-style endpoints get the CLI's own catalog as a deterministic
// floor. Custom gateways match nothing and simply get no floor.
func piCatalogModelsForBaseURL(baseURL string) []domain.CLIModel {
	origin := cliBaseURLOrigin(baseURL)
	if origin == "" {
		return nil
	}
	bin, err := resolveAssistantEngineBin(runtime.AgentCLIPi)
	if err != nil {
		return nil
	}
	dir := piAiDataDir(npmPackageRoot(bin, "@earendil-works"))
	if dir == "" {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil
	}
	var out []domain.CLIModel
	seen := map[string]bool{}
	for _, file := range files { // Glob sorts, so file order is stable
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, model := range parsePiAiCatalog(data, origin) {
			if !seen[model.ID] {
				seen[model.ID] = true
				out = append(out, model)
			}
		}
	}
	return out
}

// cliBaseURLOrigin reduces a Base URL to scheme://host, so provider catalogs
// match user endpoints regardless of path prefixes like /v1.
func cliBaseURLOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// parsePiAiCatalog reads one pi-ai provider data file — API groups of model
// entries keyed "chat:<id>", each carrying the provider baseUrl it targets —
// keeping the entries whose baseUrl shares the wanted origin, in file order.
// An empty origin keeps every entry.
func parsePiAiCatalog(data []byte, origin string) []domain.CLIModel {
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil { // '{'
		return nil
	}
	var out []domain.CLIModel
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token() // API group name
		if err != nil {
			return out
		}
		if _, ok := key.(string); !ok {
			return out
		}
		if _, err := dec.Token(); err != nil { // '{' of the group
			return out
		}
		for dec.More() {
			key, err := dec.Token() // "chat:<id>"
			if err != nil {
				return out
			}
			// A fresh struct per entry: absent JSON fields must not keep
			// the previous entry's values.
			var entry struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				BaseURL string `json:"baseUrl"`
			}
			if err := dec.Decode(&entry); err != nil {
				return out
			}
			id := entry.ID
			if id == "" {
				keyStr, ok := key.(string)
				if !ok {
					continue
				}
				id = keyStr
				if i := strings.LastIndexByte(id, ':'); i >= 0 {
					id = id[i+1:]
				}
			}
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			if origin != "" && cliBaseURLOrigin(entry.BaseURL) != origin {
				continue
			}
			seen[id] = true
			out = append(out, domain.CLIModel{ID: id, Name: strings.TrimSpace(entry.Name)})
		}
		if _, err := dec.Token(); err != nil { // '}' of the group
			return out
		}
	}
	return out
}

// cursorAgentModels runs `cursor-agent models` in a throwaway HOME with only
// the user's key, so no server token or stored CLI login is visible to it.
func cursorAgentModels(ctx context.Context, apiKey string) ([]domain.CLIModel, error) {
	bin, err := resolveAssistantEngineBin(runtime.AgentCLICursor)
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "agentflow-cursor-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	env := []string{"HOME=" + home, "CURSOR_API_KEY=" + apiKey, "NO_COLOR=1", "CI=1"}
	for _, key := range []string{"PATH", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.Dir, cmd.Env = home, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Cursor CLI 响应超时")
		}
		if strings.Contains(strings.ToLower(string(out)), "unauthorized") || strings.Contains(string(out), "401") {
			return nil, assistantCredentialError{engine: runtime.AgentCLICursor}
		}
		return nil, fmt.Errorf("cursor-agent models 退出：%v", err)
	}
	return cursorModelFamilies(parseCursorModels(string(out))), nil
}

// cursorModelFamilies folds the effort variants cursor-agent lists into one
// row per family. The built-in table wins; other IDs ending in a recognized
// effort suffix fold the same way.
func cursorModelFamilies(models []domain.CLIModel) []domain.CLIModel {
	return runtime.FoldCursorCLIModels(models)
}

func parseCursorModels(output string) []domain.CLIModel {
	models := []domain.CLIModel{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.Join(strings.Fields(strings.ReplaceAll(line, "\u200b", "")), " ")
		if match := cursorModelLine.FindStringSubmatch(line); match != nil {
			models = append(models, domain.CLIModel{ID: match[1], Name: strings.TrimSuffix(match[2], " (default)")})
		}
	}
	return models
}

func cliModelsOf(models []assistantDiscoveredModel) []domain.CLIModel {
	out := make([]domain.CLIModel, 0, len(models))
	for _, model := range models {
		out = append(out, domain.CLIModel{
			ID: model.ID, Name: model.Name, Efforts: model.Efforts, DefaultEffort: model.DefaultEffort,
			ReasoningEfforts: model.ReasoningEfforts, DiscoveredReasoningEfforts: model.DiscoveredReasoningEfforts,
		})
	}
	return out
}

// Standard model listings often contain only IDs. Supply the CLI's catalog
// levels when the endpoint omits them, and constrain advertised metadata to
// levels this CLI can actually pass to its provider.
func cliModelsWithEfforts(engine string, models []domain.CLIModel, baseURL string) []domain.CLIModel {
	for i := range models {
		id := models[i].ID
		if engine != assistantEngineCodeBuddy {
			id = engine + ":" + id
		}
		levels := append([]string{}, assistantModelEffortsForBaseURL(id, models, baseURL)...)
		models[i].DiscoveredReasoningEfforts = &levels
	}
	return models
}
