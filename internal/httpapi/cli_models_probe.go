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
	// Source is "remote" for the Base URL's model list and for what a CLI
	// reports about itself, "official" for the official endpoint's model
	// list, and "builtin" for the executor catalog used when neither answers.
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// probeCLIModels backs 「获取可用模型」 for the model catalog of a CLI other
// than CodeBuddy. The form's unsaved API Key, Base URL and protocol win over the stored ones
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
		Protocol *string `json:"protocol"`
	}
	if !decode(c, &req) {
		return
	}
	engine := strings.TrimSpace(req.Engine)
	if engine == assistantEngineCodeBuddy || !slices.Contains(assistantCLIEngines(), engine) {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "不支持的 CLI：" + engine})
		return
	}
	if req.BaseURL != nil && !validCLIBaseURL(*req.BaseURL) {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: errInvalidCLIBaseURL})
		return
	}
	apiKey, baseURL, protocol := strings.TrimSpace(req.APIKey), "", ""
	if h.repo != nil {
		if stored, found := h.repo.User(id); found {
			storedKey, storedURL := assistantEngineCredential(stored, engine)
			if apiKey == "" {
				apiKey = storedKey
			}
			baseURL, protocol = storedURL, assistantEngineProtocol(stored, engine)
		}
	}
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Protocol != nil && runtime.IsAgentCLI(engine) {
		protocol = runtime.AgentCLIProtocol(engine, *req.Protocol)
	}
	if engine == runtime.AgentCLICursor {
		h.probeCursorModels(c, apiKey)
		return
	}
	if engine == runtime.AgentCLIQoder {
		h.probeQoderModels(c, apiKey)
		return
	}
	if engine == runtime.AgentCLIDeepSeek && baseURL == "" {
		// DeepSeek Harness on the official endpoint: fetch the model list
		// instead of quoting the built-in table.
		h.probeDeepSeekModels(c, apiKey)
		return
	}
	if protocol == runtime.AgentCLIProtocolGemini {
		baseURL = ""
	}
	if baseURL == "" {
		// Official endpoint: engines with their own service fetch its
		// published model list; the rest quote the built-in table.
		if official, ok := assistantEngineOfficialEndpoints[engine]; ok {
			base, anthropic := official.baseURL, official.anthropic
			h.probeOfficialModels(c, engine, apiKey, func(ctx context.Context, key string) ([]assistantDiscoveredModel, error) {
				return assistantEngineDiscover(ctx, anthropic, key, base)
			})
			return
		}
		if engine == runtime.AgentCLIAntigravity {
			h.probeOfficialModels(c, engine, apiKey, assistantGeminiModels)
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(engine)), Source: "builtin"})
		return
	}
	if engine == runtime.AgentCLIPi {
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
	models, err := assistantEngineDiscover(ctx, assistantEngineAnthropicModels(engine, protocol), apiKey, baseURL)
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "服务没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(models), Source: "remote"})
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
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLICursor)), Source: "builtin"})
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
	c.JSON(http.StatusOK, cliModelProbeResult{Models: models, Source: "remote"})
}

var cursorModelLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*) - (.+)$`)

// probeQoderModels asks the qoder CLI itself: Qoder has no Base URL, and the
// models behind a PAT depend on the account (official tiers plus BYOK rows).
func (h *usersHandlers) probeQoderModels(c *gin.Context, apiKey string) {
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(runtime.AgentCLIQoder) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	models, err := qoderAgentModels(ctx, apiKey)
	if errors.Is(err, errAssistantCLIMissing) {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIQoder)), Source: "builtin"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		// The CLI only lists the account's BYOK models; none means the
		// official "auto" model is all the account offers.
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIQoder)), Source: "builtin"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: models, Source: "remote"})
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

// deepSeekOfficialBaseURL is the endpoint DeepSeek Harness rides when no
// Base URL is configured; its published model list feeds the probe.
const deepSeekOfficialBaseURL = "https://api.deepseek.com"

// probeDeepSeekModels answers 「获取可用模型」 for DeepSeek Harness on the
// official endpoint. The installed CLI's own provider catalog is the
// deterministic floor — what this dsh build can drive, key or no key — and
// the official /models endpoint adds whatever the service currently lists,
// so new releases show up before the CLI bundles them. When the official
// endpoint doesn't answer, the catalog (or the built-in table) still stands
// in. A Base URL probe never reaches here; it lists that endpoint instead.
func (h *usersHandlers) probeDeepSeekModels(c *gin.Context, apiKey string) {
	catalog := dshCatalogModels()
	if apiKey == "" {
		// Nothing to fetch the official list with; the CLI's catalog or the
		// built-in table still answers.
		if len(catalog) == 0 {
			c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIDeepSeek)), Source: "builtin"})
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: catalog, Source: "remote"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	official, err := assistantEngineDiscover(ctx, false, apiKey, deepSeekOfficialBaseURL)
	if err != nil || len(official) == 0 {
		if len(catalog) > 0 {
			// The official endpoint didn't answer (bad key, offline,
			// nothing published); the CLI's catalog is still a
			// deterministic list.
			c.JSON(http.StatusOK, cliModelProbeResult{Models: catalog, Source: "remote"})
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIDeepSeek)), Source: "builtin"})
		return
	}
	models := cliModelsOf(official)
	if len(catalog) > 0 {
		models = mergeCatalogModels(catalog, models)
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: models, Source: "official"})
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

// officialEndpoint is an engine's own model service and the wire shape its
// model list speaks.
type officialEndpoint struct {
	baseURL   string
	anthropic bool
}

// assistantEngineOfficialEndpoints lists the engines that ride a service of
// their own when no Base URL is configured, with that service's endpoint.
var assistantEngineOfficialEndpoints = map[string]officialEndpoint{
	assistantEngineCodex:  {baseURL: "https://api.openai.com/v1"},
	assistantEngineClaude: {baseURL: "https://api.anthropic.com", anthropic: true},
	runtime.AgentCLIKimi:  {baseURL: "https://api.moonshot.cn/v1"},
	runtime.AgentCLIZCode: {baseURL: "https://open.bigmodel.cn/api/anthropic", anthropic: true},
}

// probeOfficialModels answers 「获取可用模型」 for an engine on its official
// endpoint: the service's published model list fetched with the user's key,
// with the built-in table kept as the deterministic floor. A failed fetch
// leaves the floor to answer on its own.
func (h *usersHandlers) probeOfficialModels(c *gin.Context, engine, apiKey string, discover func(ctx context.Context, apiKey string) ([]assistantDiscoveredModel, error)) {
	builtin := cliModelsOf(assistantEngineBuiltinModels(engine))
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: builtin, Source: "builtin"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := discover(ctx, apiKey)
	if err != nil || len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: builtin, Source: "builtin"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: mergeCatalogModels(builtin, cliModelsOf(models)), Source: "official"})
}

// assistantGeminiModels is a variable so tests can stub the network.
var assistantGeminiModels = discoverGeminiModels

// geminiModelsEndpoint is the official Gemini ListModels API.
const geminiModelsEndpoint = "https://generativelanguage.googleapis.com/v1beta/models"

// discoverGeminiModels lists the Gemini models a Google AI Studio key may
// use, following ListModels pages until they run out.
func discoverGeminiModels(ctx context.Context, apiKey string) ([]assistantDiscoveredModel, error) {
	var out []assistantDiscoveredModel
	page := ""
	for {
		endpoint := geminiModelsEndpoint
		if page != "" {
			endpoint += "?pageToken=" + url.QueryEscape(page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		models, next := parseGeminiModels(body)
		out = append(out, models...)
		if next == "" || len(out) >= assistantDiscoveryMaxModels {
			return out, nil
		}
		page = next
	}
}

// parseGeminiModels reads one ListModels response page: the models capable of
// generateContent, with bare IDs ("models/gemini-x" → "gemini-x").
func parseGeminiModels(data []byte) ([]assistantDiscoveredModel, string) {
	var page struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, ""
	}
	var out []assistantDiscoveredModel
	for _, model := range page.Models {
		if !slices.Contains(model.Methods, "generateContent") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(model.Name, "models/"))
		if id == "" {
			continue
		}
		name := strings.TrimSpace(model.DisplayName)
		if name == "" {
			name = id
		}
		out = append(out, assistantDiscoveredModel{ID: id, Name: name})
	}
	return out, page.NextPageToken
}

// probePiModels answers 「获取可用模型」 for Pi behind a user Base URL: the
// endpoint's published model list fetched live, floored by the installed
// CLI's own pi-ai provider data when it knows the service.
func (h *usersHandlers) probePiModels(c *gin.Context, apiKey, baseURL, protocol string) {
	floor := piCatalogModelsForBaseURL(baseURL)
	if apiKey == "" {
		if len(floor) > 0 {
			c.JSON(http.StatusOK, cliModelProbeResult{Models: floor, Source: "remote"})
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
			c.JSON(http.StatusOK, cliModelProbeResult{Models: floor, Source: "remote"})
			return
		}
		if err != nil {
			c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
			return
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLIPi)), Source: "builtin"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: mergeCatalogModels(floor, cliModelsOf(live)), Source: "remote"})
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

// cursorModelFamilies folds the effort variants cursor-agent lists into the
// families of the built-in table, in table order; IDs outside it follow as
// they are.
func cursorModelFamilies(models []domain.CLIModel) []domain.CLIModel {
	present, seen := map[string]bool{}, map[string]bool{}
	var unknown []domain.CLIModel
	for _, model := range models {
		if family, ok := runtime.CursorModelFamily(model.ID); ok {
			present[family.ID] = true
		} else if !seen[model.ID] {
			seen[model.ID] = true
			unknown = append(unknown, model)
		}
	}
	out := []domain.CLIModel{}
	for _, family := range runtime.CursorModels() {
		if present[family.ID] {
			out = append(out, domain.CLIModel{ID: family.ID, Name: family.Name})
		}
	}
	return append(out, unknown...)
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
		out = append(out, domain.CLIModel{ID: model.ID, Name: model.Name})
	}
	return out
}
