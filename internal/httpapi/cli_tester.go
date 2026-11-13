package httpapi

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// A CLI test is one real, minimal turn: it proves the key, the Base URL and
// the provider actually work together, which a model listing alone cannot.
const cliTestTimeout = 120 * time.Second

// cliTestPrompt stays tiny so the test costs as little as possible.
const cliTestPrompt = "只回复两个字：正常"

type cliTestResult struct {
	OK        bool   `json:"ok"`
	Engine    string `json:"engine"`
	Model     string `json:"model,omitempty"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
	// Reply is the assistant's own text, truncated: evidence that the model
	// answered instead of only the endpoint accepting a socket.
	Reply string `json:"reply,omitempty"`
	Error string `json:"error,omitempty"`
}

// testCLI backs 「测试」 in the credential editor. It runs a one-shot turn with
// the form's unsaved values (or the stored ones) and stores nothing.
// 路由: g.POST("/api/users/:id/cli-test", h.testCLI)
func (h *usersHandlers) testCLI(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, cliTestResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, cliTestResult{Error: "只能测试当前登录用户的凭据"})
		return
	}
	var req struct {
		Engine string `json:"engine"`
		APIKey string `json:"apiKey"`
		// BaseURL is the form's value; null keeps the stored one.
		BaseURL  *string `json:"baseUrl"`
		Protocol *string `json:"protocol"`
		Model    string  `json:"model"`
	}
	if !decode(c, &req) {
		return
	}
	engine := assistantInstallEngine(req.Engine)
	if !engineTestable(engine) && engine != assistantEngineCodeBuddy {
		c.JSON(http.StatusBadRequest, cliTestResult{Error: "不支持的 CLI：" + engine})
		return
	}
	user, found := h.repo.User(id)
	if !found {
		c.JSON(http.StatusNotFound, cliTestResult{Error: "用户不存在"})
		return
	}
	// The form's values override the stored ones so a user can test before
	// saving, exactly like 获取可用模型.
	if key := strings.TrimSpace(req.APIKey); key != "" {
		applyTestKey(&user, engine, key, req.BaseURL, req.Protocol)
	} else if req.BaseURL != nil || req.Protocol != nil {
		applyTestKey(&user, engine, "", req.BaseURL, req.Protocol)
	}

	model := strings.TrimSpace(req.Model)
	testRoot, err := os.MkdirTemp("", "agentflow-cli-test-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, cliTestResult{Error: "无法创建测试目录"})
		return
	}
	defer os.RemoveAll(testRoot)
	ctx, cancel := context.WithTimeout(c.Request.Context(), cliTestTimeout)
	defer cancel()
	started := time.Now()
	reply, _, runErr := runAssistantCLI(ctx, assistantCLIRequest{
		Executor: engine,
		Model:    engine + ":" + model,
		Prompt:   cliTestPrompt,
		// ReviewOnly is the one-shot path: no MCP servers, no history, no
		// session to keep.
		ReviewOnly: true,
		// A throwaway root keeps the test out of the user's chat workspace.
		SessionRoot: testRoot,
	}, user, nil)
	elapsed := time.Since(started).Milliseconds()
	if runErr != nil {
		if ctx.Err() != nil {
			c.JSON(http.StatusOK, cliTestResult{Engine: engine, Model: model, LatencyMS: elapsed, Error: "测试超时，请检查网络或服务地址"})
			return
		}
		log.Printf("cli test %s: %v", engine, runErr)
		c.JSON(http.StatusOK, cliTestResult{Engine: engine, Model: model, LatencyMS: elapsed, Error: runErr.Error()})
		return
	}
	c.JSON(http.StatusOK, cliTestResult{OK: true, Engine: engine, Model: model, LatencyMS: elapsed, Reply: truncateRunes(strings.TrimSpace(reply), 200)})
}

// engineTestable reports whether the chat path can run this engine one-shot.
func engineTestable(engine string) bool {
	for _, known := range assistantCLIEngines() {
		if known == engine {
			return true
		}
	}
	return false
}

// applyTestKey overlays unsaved form values on the stored user. An empty key
// keeps the stored one, while nil pointers keep stored Base URL / protocol.
func applyTestKey(user *domain.User, engine, key string, baseURL, protocol *string) {
	if user.AgentCLICredentials == nil {
		user.AgentCLICredentials = map[string]domain.AgentCLICredential{}
	}
	switch engine {
	case assistantEngineCodex:
		if key != "" {
			user.CodexAPIKey = key
		}
		if baseURL != nil {
			user.CodexBaseURL = strings.TrimSpace(*baseURL)
		}
		return
	case assistantEngineClaude:
		if key != "" {
			user.ClaudeCodeAPIKey = key
		}
		if baseURL != nil {
			user.ClaudeCodeBaseURL = strings.TrimSpace(*baseURL)
		}
		return
	case assistantEngineCodeBuddy:
		if key != "" {
			user.CodeBuddyAPIKey = key
		}
		return
	}
	cred := user.AgentCLICredentials[engine]
	if key != "" {
		cred.APIKey = key
	}
	if baseURL != nil {
		cred.BaseURL = strings.TrimSpace(*baseURL)
	}
	if protocol != nil {
		cred.Protocol = strings.TrimSpace(*protocol)
	}
	user.AgentCLICredentials[engine] = cred
}
