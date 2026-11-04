package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

// API Key 面板的外部调用测试: 用这把钥匙从本机回环打自己的 /v1 入口。
// 走的是外部客户端完全相同的路径(APIKeyAuth → 选路 → 上游), 因此能一次性验证钥匙是否可用、
// 能用哪些模型、以及真实调用能否出字 —— 这些都不是在进程内直接查缓存能替代的。
const (
	apiKeyProbeTimeout   = 30 * time.Second
	apiKeyProbePrompt    = "hi"
	apiKeyProbeMaxTokens = 16
	apiKeyProbeBodyLimit = 1024
)

// apiKeyProbeBaseURL 返回本进程对外的调用地址; 监听在通配地址时回环取 127.0.0.1。
// 这里同时会回给界面, 用户可直接照抄到自己的客户端。
func apiKeyProbeBaseURL() string {
	host := strings.TrimSpace(conf.AppConfig.Server.Host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, conf.AppConfig.Server.Port)
}

// apiKeyProbeClient 返回自调专用客户端: 回环请求不能走用户配置的上游代理。
func apiKeyProbeClient() *http.Client {
	client := &http.Client{Timeout: apiKeyProbeTimeout}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := transport.Clone()
		cloned.Proxy = nil
		client.Transport = cloned
	}
	return client
}

// apiKeyProbeRequest 发出一次自调请求, 返回状态码与响应原文; 网络层失败时 status 为 0。
func apiKeyProbeRequest(ctx context.Context, method, url, apiKey string, payload any) (int, []byte, int64, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, 0, err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	response, err := apiKeyProbeClient().Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return 0, nil, latency, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, latency, err
	}
	return response.StatusCode, raw, latency, nil
}

// apiKeyProbeModels 解析 /v1/models 的响应; 只认 data[].id 这一种形状, 网关的两种方言都符合。
func apiKeyProbeModels(raw []byte) []string {
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	names := make([]string, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			names = append(names, id)
		}
	}
	return names
}

// apiKeyProbeContent 从补全响应里取模型输出; 兼容 chat 与 anthropic 两种形状。
func apiKeyProbeContent(raw []byte) string {
	var chat struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chat); err == nil {
		for _, choice := range chat.Choices {
			if text := strings.TrimSpace(choice.Message.Content); text != "" {
				return text
			}
			if text := strings.TrimSpace(choice.Message.ReasoningContent); text != "" {
				return text
			}
		}
	}

	var anthropic struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &anthropic); err == nil {
		for _, block := range anthropic.Content {
			if text := strings.TrimSpace(block.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

// testAPIKeyModels 用指定 API Key 请求本机 /v1/models, 验证钥匙本身可用并拿到可用模型清单。
func testAPIKeyModels(c *gin.Context) {
	var request model.APIKeyTestModelsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	apiKey, err := op.APIKeyGet(request.ID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "api key not found")
		return
	}

	baseURL := apiKeyProbeBaseURL()
	ctx, cancel := context.WithTimeout(c.Request.Context(), apiKeyProbeTimeout)
	defer cancel()

	status, raw, latency, err := apiKeyProbeRequest(ctx, http.MethodGet, baseURL+"/v1/models", apiKey.APIKey, nil)
	result := model.APIKeyTestResult{
		Status:  status,
		Latency: latency,
		BaseURL: baseURL + "/v1",
		Body:    truncateText(string(raw), apiKeyProbeBodyLimit),
	}
	if err != nil {
		result.Error = err.Error()
		resp.Success(c, result)
		return
	}

	result.Models = apiKeyProbeModels(raw)
	result.OK = status == http.StatusOK && len(result.Models) > 0
	if status != http.StatusOK && result.Error == "" {
		result.Error = fmt.Sprintf("gateway returned HTTP %d", status)
	}
	resp.Success(c, result)
}

// testAPIKeyChat 用指定 API Key 走本机 /v1/chat/completions 发一次最小补全请求。
// 请求会真实进入转发链路并计入统计, 这正是外部调用测试要暴露的东西。
func testAPIKeyChat(c *gin.Context) {
	var request model.APIKeyTestChatRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	apiKey, err := op.APIKeyGet(request.ID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "api key not found")
		return
	}

	payload := map[string]any{
		"model":      request.Model,
		"messages":   []map[string]string{{"role": "user", "content": apiKeyProbePrompt}},
		"max_tokens": apiKeyProbeMaxTokens,
		"stream":     false,
	}

	baseURL := apiKeyProbeBaseURL()
	ctx, cancel := context.WithTimeout(c.Request.Context(), apiKeyProbeTimeout)
	defer cancel()

	status, raw, latency, err := apiKeyProbeRequest(ctx, http.MethodPost, baseURL+"/v1/chat/completions", apiKey.APIKey, payload)
	result := model.APIKeyTestResult{
		Status:  status,
		Latency: latency,
		BaseURL: baseURL + "/v1",
		Body:    truncateText(string(raw), apiKeyProbeBodyLimit),
	}
	if err != nil {
		result.Error = err.Error()
		resp.Success(c, result)
		return
	}

	result.Content = truncateText(apiKeyProbeContent(raw), 200)
	result.OK = status == http.StatusOK && result.Content != ""
	if status != http.StatusOK && result.Error == "" {
		result.Error = fmt.Sprintf("gateway returned HTTP %d", status)
	}
	resp.Success(c, result)
}
