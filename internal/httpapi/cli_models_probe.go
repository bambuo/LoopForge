package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// Source is "remote" for the Base URL's model list and "builtin" for the
	// executor catalog used when no Base URL is set.
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
	if protocol == runtime.AgentCLIProtocolGemini {
		baseURL = ""
	}
	if baseURL == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(engine)), Source: "builtin"})
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
