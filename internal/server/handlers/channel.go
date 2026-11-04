package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/rhttp"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/dlclark/regexp2"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/detail/:id", http.MethodGet).
				Handle(getChannelDetail),
		).
		AddRoute(
			router.NewRoute("/stats", http.MethodGet).
				Handle(listChannelStats),
		).
		AddRoute(
			router.NewRoute("/grants", http.MethodGet).
				Handle(listChannelGrant),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateChannel),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableChannel),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannel),
		).
		AddRoute(
			router.NewRoute("/fetch-model", http.MethodPost).
				Handle(fetchModel),
		).
		AddRoute(
			router.NewRoute("/test-model", http.MethodPost).
				Handle(testModel),
		)
}

// getChannelDetail 返回单个渠道的完整配置, 供编辑表单打开时读取。
// 与列表分开: 整份配置带着路径, 代理与凭据明文, 只有正在编辑的那一个渠道用得上。
func getChannelDetail(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	detail, err := op.ChannelDetailGet(id)
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	resp.Success(c, detail)
}

// listChannelStats 返回全部渠道及其模型的累计统计, 也是渠道列表页的数据来源。
// 不带整份配置: 统计每次转发都在变, 界面按更短的间隔刷新它, 而路径, 代理与凭据明文只在编辑时用得上。
func listChannelStats(c *gin.Context) {
	resp.Success(c, op.ChannelStatsList())
}

// listChannelGrant 返回全部渠道授权候选, 供分组页选取成员。
func listChannelGrant(c *gin.Context) {
	resp.Success(c, op.ChannelGrantCandidates())
}

func createChannel(c *gin.Context) {
	var req model.ChannelDetail
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel, err := op.ChannelCreate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := addChannelModelPrices(channel.Models, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, channel)
}

func updateChannel(c *gin.Context) {
	var req model.ChannelDetail
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if req.ID == 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := addChannelModelPrices(channel.Models, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := op.LLMCleanupGhosts(c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, channel)
}

func enableChannel(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.ChannelDel(id, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := op.LLMCleanupGhosts(c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

// addChannelModelPrices 为渠道模型匹配校准价格，并批量写入尚不存在的价格记录。
func addChannelModelPrices(modelNames []string, ctx context.Context) error {
	seen := make(map[string]struct{}, len(modelNames))
	llmInfos := make([]model.LLMInfo, 0, len(modelNames))
	for _, modelName := range modelNames {
		modelName = strings.ToLower(modelName)
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		llmInfo := model.LLMInfo{Name: modelName}
		if modelPrice := price.GetLLMPrice(modelName); modelPrice != nil {
			llmInfo.LLMPrice = *modelPrice
		}
		llmInfos = append(llmInfos, llmInfo)
	}
	return op.LLMBatchCreate(llmInfos, ctx)
}

// fetchModel 按提交的渠道配置与凭据拉取上游模型列表, 并按过滤表达式筛选后返回。
// 同时探测 OpenAI 与 Anthropic 两侧, 谁返回了哪些模型, 就给对应协议位打勾: 协议支持由探测结果决定, 无需用户声明。
// OpenAI 侧记为 Responses 而不是 Chat: Chat Completions 已被官方标记弃用, 新渠道应默认走 Responses,
// 仍需 Chat 的渠道由用户在界面上手动勾选。两侧的 /models 地址与认证形态不同, 故必须分别探测:
// 单协议上游只有一侧会成功, "哪侧成功" 本身就是协议支持的证据。
// 只有两侧都失败才算失败; 一侧失败属正常情况, 单协议上游本就只有一侧讲得通, 按成功那侧的结果返回。
func fetchModel(c *gin.Context) {
	var request model.ChannelFetchModelRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	ctx := c.Request.Context()
	// 探测收的是尚未落库的提交配置, 不经 normalizeChannelConfig, 故在此自行去空白;
	// 其中只有地址是硬需求: 渠道尚未命名时也可试拉, 故名称不在此校验。
	target := request.Channel
	target.BaseURL = strings.TrimSpace(target.BaseURL)
	target.ChannelProxy = strings.TrimSpace(target.ChannelProxy)
	target.MatchRegex = strings.TrimSpace(target.MatchRegex)
	if target.BaseURL == "" {
		resp.Error(c, http.StatusBadRequest, "channel base url is required")
		return
	}

	httpClient, exclusive, err := probeClient(target)
	if err != nil {
		resp.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	// 渠道专用代理的客户端不再共享, 探测完就得关掉空闲连接; 探测收的是未落库的输入, 留着也无从复用。
	if exclusive && httpClient != nil {
		defer httpClient.CloseIdleConnections()
	}

	var openaiModels, anthropicModels []string
	var openaiErr, anthropicErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		openaiModels, openaiErr = fetchOpenAIModels(httpClient, ctx, target, request.Key, modelsURL(target.BaseURL, target.OpenAIResponsePath))
	}()
	go func() {
		defer wg.Done()
		anthropicModels, anthropicErr = fetchAnthropicModels(httpClient, ctx, target, request.Key, modelsURL(target.BaseURL, target.AnthropicMessagePath))
	}()
	wg.Wait()

	if openaiErr != nil && anthropicErr != nil {
		// 上游鉴权失败或地址不通属于调用方配置问题, 按 502 返回并带上上游原文, 便于在界面上直接看到原因。
		resp.Error(c, http.StatusBadGateway, fmt.Sprintf("openai: %v; anthropic: %v", openaiErr, anthropicErr))
		return
	}

	var re, reGlobal *regexp2.Regexp
	if target.MatchRegex != "" {
		if re, err = regexp2.Compile(target.MatchRegex, regexp2.ECMAScript); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	// 全局过滤由设置页维护, 与渠道过滤同取 AND: 模型须同时通过两枚正则才保留, 留空的一侧不生效。
	// 设置缺失按不过滤处理: 启动初始化会补齐默认值, 缺行只可能出现在旧库尚未刷新的瞬间。
	globalFilter, _ := op.SettingGetString(model.SettingKeyModelFilter)
	if globalFilter != "" {
		if reGlobal, err = regexp2.Compile(globalFilter, regexp2.ECMAScript); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}

	// 模型名须同时通过渠道与全局两枚过滤正则, 编译与匹配错误统一按请求错误返回。
	matches := func(name string) (bool, error) {
		if re != nil {
			matched, err := re.MatchString(name)
			if err != nil {
				return false, err
			}
			if !matched {
				return false, nil
			}
		}
		if reGlobal != nil {
			matched, err := reGlobal.MatchString(name)
			if err != nil {
				return false, err
			}
			if !matched {
				return false, nil
			}
		}
		return true, nil
	}

	// 两侧结果按名称合并成一份有序集合: 同名模型在两侧都出现时, 协议位取并集。
	// 保持首次出现的顺序, 界面上模型的排列才与上游返回的一致;
	// 先并入 OpenAI 再并入 Anthropic, 顺序写死而不用 map 遍历, 否则界面上的模型排列会随每次刷新变化。
	protocolsByModel := make(map[string]model.Protocol, len(openaiModels)+len(anthropicModels))
	order := make([]string, 0, len(openaiModels)+len(anthropicModels))
	for _, name := range openaiModels {
		matched, err := matches(name)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if !matched {
			continue
		}
		if _, ok := protocolsByModel[name]; !ok {
			order = append(order, name)
		}
		protocolsByModel[name] |= model.ProtocolOpenAIResponse
	}
	for _, name := range anthropicModels {
		matched, err := matches(name)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if !matched {
			continue
		}
		if _, ok := protocolsByModel[name]; !ok {
			order = append(order, name)
		}
		protocolsByModel[name] |= model.ProtocolAnthropicMessage
	}

	models := make([]model.ChannelFetchModel, 0, len(order))
	for _, name := range order {
		models = append(models, model.ChannelFetchModel{Name: name, Protocols: protocolsByModel[name]})
	}
	resp.Success(c, models)
}

// 单模型测试的固定参数: 提示词取最短的可读文本, 输出上限压到最小, 测试只为确认能出字而不消耗额度。
const (
	testPrompt        = "hi"
	testMaxTokens     = 16
	anthropicVersion  = "2023-06-01"
	modelTestTimeout  = 30 * time.Second
	testBodyLimit     = 4096 // 上游响应体的读取上限, 足够覆盖正常补全与常见报错。
	testBodyShowLimit = 1024 // 回给界面的响应原文长度上限, 超出部分截断。
)

// probeClient 按渠道配置构造探测用的 HTTP 客户端, 与保存后转发时用的形态保持一致。
// 渠道专用代理的客户端不再共享, 故第二个返回值表示调用方是否需要在用完后关闭空闲连接。
func probeClient(target model.ChannelConfig) (*http.Client, bool, error) {
	if !target.Proxy {
		client, err := rhttp.Direct()
		return client, false, err
	}
	if target.ChannelProxy == "" {
		client, err := rhttp.Proxy()
		return client, false, err
	}
	client, err := rhttp.New(target.ChannelProxy)
	return client, true, err
}

// testModel 对指定模型发起一次最小补全请求, 用于确认渠道配置真的能出字。
// 拉取模型列表只能证明地址与凭据可用: 上游可能列出并未开通的模型, 该模型也可能在某个协议端点上并不响应。
// 故此处按真实转发的方式构造请求, 把状态码, 耗时与上游原文一并回给界面。
func testModel(c *gin.Context) {
	var request model.ChannelTestModelRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	// 与拉取模型列表同理: 测试收的是尚未落库的配置, 不经 normalizeChannelConfig, 故在此自行去空白。
	target := request.Channel
	target.BaseURL = strings.TrimSpace(target.BaseURL)
	target.ChannelProxy = strings.TrimSpace(target.ChannelProxy)
	target.OpenAIChatCompletionPath = strings.TrimSpace(target.OpenAIChatCompletionPath)
	target.OpenAIResponsePath = strings.TrimSpace(target.OpenAIResponsePath)
	target.AnthropicMessagePath = strings.TrimSpace(target.AnthropicMessagePath)
	if target.BaseURL == "" {
		resp.Error(c, http.StatusBadRequest, "channel base url is required")
		return
	}
	channelKey := strings.TrimSpace(request.Key)
	if channelKey == "" {
		resp.Error(c, http.StatusBadRequest, "channel key is required")
		return
	}
	modelName := strings.TrimSpace(request.Model)
	if modelName == "" {
		resp.Error(c, http.StatusBadRequest, "model name is required")
		return
	}

	httpClient, exclusive, err := probeClient(target)
	if err != nil {
		resp.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	if exclusive && httpClient != nil {
		defer httpClient.CloseIdleConnections()
	}

	// 测试自带超时: 上游不响应时不能让界面一直转圈, 也不必等到客户端断开才释放连接。
	ctx, cancel := context.WithTimeout(c.Request.Context(), modelTestTimeout)
	defer cancel()

	var last model.ChannelTestModelResult
	for _, protocol := range modelTestProtocols(request.Protocol) {
		result := runModelTest(ctx, httpClient, target, channelKey, modelName, protocol)
		if result.OK {
			resp.Success(c, result)
			return
		}
		last = result
		// 指定的协议只试一次; 未指定时按顺序试到成功为止, 但超时与取消已无重试意义。
		if ctx.Err() != nil {
			break
		}
	}
	// 上游拒绝属正常测试结果而非接口错误: 失败原因要原样带给界面, 故仍按 200 返回并由 OK 区分。
	resp.Success(c, last)
}

// modelTestProtocols 给出本次测试要尝试的协议顺序; 指定协议时只试那一个。
// 默认顺序与拉取模型列表一致: Chat Completions 最通用, 其次 Responses, 最后 Anthropic。
func modelTestProtocols(requested model.Protocol) []model.Protocol {
	if requested != 0 {
		return []model.Protocol{requested}
	}
	return []model.Protocol{
		model.ProtocolOpenAIChatCompletion,
		model.ProtocolOpenAIResponse,
		model.ProtocolAnthropicMessage,
	}
}

// runModelTest 用指定协议发一次真实请求并解析结果; 网络层与上游层的失败都收进结果, 不向上抛。
func runModelTest(ctx context.Context, httpClient *http.Client, target model.ChannelConfig, key, modelName string, protocol model.Protocol) model.ChannelTestModelResult {
	result := model.ChannelTestModelResult{Protocol: protocol}
	request, err := buildTestRequest(ctx, target, key, modelName, protocol)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	started := time.Now()
	response, err := httpClient.Do(request)
	result.Latency = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()

	result.Status = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, testBodyLimit))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Body = truncateText(string(body), testBodyShowLimit)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Error = fmt.Sprintf("upstream %s: %s", response.Status, truncateText(string(body), 512))
		return result
	}

	content, err := testResponseContent(protocol, body)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.OK = true
	result.Content = truncateText(content, 256)
	return result
}

// buildTestRequest 按协议构造最小的真实请求; 地址, 路径与 Header 与转发路径共用同一份配置。
func buildTestRequest(ctx context.Context, target model.ChannelConfig, key, modelName string, protocol model.Protocol) (*http.Request, error) {
	var payload any
	switch protocol {
	case model.ProtocolOpenAIResponse:
		payload = map[string]any{
			"model":             modelName,
			"input":             testPrompt,
			"max_output_tokens": testMaxTokens,
			"stream":            false,
		}
	case model.ProtocolAnthropicMessage:
		payload = map[string]any{
			"model":      modelName,
			"max_tokens": testMaxTokens,
			"messages":   []map[string]string{{"role": "user", "content": testPrompt}},
		}
	default:
		payload = map[string]any{
			"model":      modelName,
			"messages":   []map[string]string{{"role": "user", "content": testPrompt}},
			"max_tokens": testMaxTokens,
			"stream":     false,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(target.BaseURL, "/") + "/" + strings.TrimLeft(testPath(target, protocol), "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	switch protocol {
	case model.ProtocolAnthropicMessage:
		request.Header.Set("X-Api-Key", key)
		request.Header.Set("Anthropic-Version", anthropicVersion)
	default:
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for _, header := range target.CustomHeader {
		if header.HeaderKey != "" {
			request.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
	return request, nil
}

// testPath 取指定协议的请求路径; 表单未填时用保存时会补上的默认值, 保证测试与实际转发打在同一处。
func testPath(target model.ChannelConfig, protocol model.Protocol) string {
	switch protocol {
	case model.ProtocolOpenAIResponse:
		if target.OpenAIResponsePath != "" {
			return target.OpenAIResponsePath
		}
		return "/v1/responses"
	case model.ProtocolAnthropicMessage:
		if target.AnthropicMessagePath != "" {
			return target.AnthropicMessagePath
		}
		return "/v1/messages"
	default:
		if target.OpenAIChatCompletionPath != "" {
			return target.OpenAIChatCompletionPath
		}
		return "/v1/chat/completions"
	}
}

// testResponseContent 从三种协议的响应里取出模型输出的文本; 取不到即视为测试失败。
// 只回用量或空壳的响应同样说明该端点讲不通, 故不把 HTTP 200 本身当作成功。
// 推理模型的思考内容也算输出: 测试的目的是确认模型确实在算, 而非考核回答质量。
func testResponseContent(protocol model.Protocol, body []byte) (string, error) {
	switch protocol {
	case model.ProtocolOpenAIResponse:
		var parsed struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		if text := strings.TrimSpace(parsed.OutputText); text != "" {
			return text, nil
		}
		var builder strings.Builder
		for _, item := range parsed.Output {
			for _, part := range item.Content {
				builder.WriteString(part.Text)
			}
		}
		if text := strings.TrimSpace(builder.String()); text != "" {
			return text, nil
		}
		return "", fmt.Errorf("upstream returned no output text")
	case model.ProtocolAnthropicMessage:
		var parsed struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		var builder strings.Builder
		for _, part := range parsed.Content {
			builder.WriteString(part.Text)
			builder.WriteString(part.Thinking)
		}
		if text := strings.TrimSpace(builder.String()); text != "" {
			return text, nil
		}
		return "", fmt.Errorf("upstream returned no output text")
	default:
		var parsed struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		for _, choice := range parsed.Choices {
			if text := strings.TrimSpace(choice.Message.Content); text != "" {
				return text, nil
			}
			if text := strings.TrimSpace(choice.Message.ReasoningContent); text != "" {
				return text, nil
			}
		}
		return "", fmt.Errorf("upstream returned no output text")
	}
}

// truncateText 按字符截断, 避免把多字节字符切成半个; 未超限时原样返回。
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// modelsURL 取协议请求路径的父级目录, 与地址拼成同级的 /models 地址。
// 例如 /v1/chat/completions 与 /v1/messages 都得到 /v1/models, /chat/completions 得到 /models。
func modelsURL(baseURL, protocolPath string) string {
	parent := path.Dir(strings.TrimRight(protocolPath, "/"))
	// Anthropic 的 /v1/messages 只有一层, 父级即 /v1; Chat 的 /v1/chat/completions 需要再上一层。
	if strings.HasSuffix(parent, "/chat") {
		parent = path.Dir(parent)
	}
	if parent == "." || parent == "/" {
		parent = ""
	}
	return strings.TrimRight(baseURL, "/") + parent + "/models"
}

// refer: https://platform.openai.com/docs/api-reference/models/list
func fetchOpenAIModels(httpClient *http.Client, ctx context.Context, target model.ChannelConfig, key, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for _, header := range target.CustomHeader {
		if header.HeaderKey != "" {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	result, err := decodeModelList[model.OpenAIModelList](response)
	if err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(httpClient *http.Client, ctx context.Context, target model.ChannelConfig, key, url string) ([]string, error) {
	var allModels []string
	var afterID string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Anthropic-Version", anthropicVersion)
		for _, header := range target.CustomHeader {
			if header.HeaderKey != "" {
				req.Header.Set(header.HeaderKey, header.HeaderValue)
			}
		}
		if afterID != "" {
			q := req.URL.Query()
			q.Set("after_id", afterID)
			req.URL.RawQuery = q.Encode()
		}

		response, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		// 分页时每轮都会新建响应, 必须当轮读完即关; 用 defer 会攒到整个函数返回才释放。
		result, err := decodeModelList[model.AnthropicModelList](response)
		if err != nil {
			return nil, err
		}
		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}
		if !result.HasMore {
			break
		}
		afterID = result.LastID
	}
	return allModels, nil
}

// decodeModelList 关闭响应并把响应体解成模型列表; 非 2xx 时按上游错误返回。
// 两侧解析流程一致, 只有目标结构不同, 故用类型参数收敛; 分页调用要求当轮读完即关, 关闭点放在此处最稳。
func decodeModelList[T any](response *http.Response) (T, error) {
	defer response.Body.Close()
	var result T
	// 上游报错时响应体常是能被正常解码的 JSON, 若不先拦下, 模型列表会解成空列表并当作成功;
	// 响应体截断到 512 字节: 部分上游在鉴权失败时返回整页 HTML, 全文带到界面上无用。
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, err := io.ReadAll(io.LimitReader(response.Body, 512))
		if err != nil {
			return result, fmt.Errorf("upstream %s", response.Status)
		}
		return result, fmt.Errorf("upstream %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}
