package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/sjson"
)

// buildOutbound 在渠道授权支持的协议内选出本轮上游协议, 构造对应的出站转换器, 并返回选中的协议和能否同协议透传。
// 地址由渠道的协议路径字段与地址拼接, 凭据取自目标绑定的渠道凭据。
// want 是客户端请求使用的协议, 由调用方按入站格式定出; 选中的协议随请求状态推给界面, 故一并返回。
func buildOutbound(channel model.Channel, grant model.ChannelGrant, channelKey model.ChannelKey, want model.Protocol) (transformer.Outbound, model.Protocol, bool, error) {
	protocol, passthrough := want, grant.Protocols&want != 0
	if !passthrough {
		protocol = 0
		switch {
		case grant.Protocols&model.ProtocolAnthropicMessage != 0:
			protocol = model.ProtocolAnthropicMessage
		case grant.Protocols&model.ProtocolOpenAIResponse != 0:
			protocol = model.ProtocolOpenAIResponse
		case grant.Protocols&model.ProtocolOpenAIChatCompletion != 0:
			protocol = model.ProtocolOpenAIChatCompletion
		}
	}

	key := auth.NewStaticKeyProvider(channelKey.Key)
	switch protocol {
	case model.ProtocolOpenAIChatCompletion:
		outbound, err := openai.NewOutboundTransformerWithConfig(&openai.Config{PlatformType: openai.PlatformOpenAI, BaseURL: channel.BaseURL, EndpointPath: channel.OpenAIChatCompletionPath, APIKeyProvider: key})
		return outbound, protocol, passthrough, err
	case model.ProtocolOpenAIResponse:
		outbound, err := responses.NewOutboundTransformerWithConfig(&responses.Config{BaseURL: channel.BaseURL, EndpointPath: channel.OpenAIResponsePath, APIKeyProvider: key})
		return outbound, protocol, passthrough, err
	case model.ProtocolAnthropicMessage:
		outbound, err := anthropic.NewOutboundTransformerWithConfig(&anthropic.Config{Type: anthropic.PlatformDirect, BaseURL: channel.BaseURL, EndpointPath: channel.AnthropicMessagePath, APIKeyProvider: key})
		return outbound, protocol, passthrough, err
	default:
		return nil, 0, false, fmt.Errorf("channel grant %d supports no known protocol: %d", grant.ID, grant.Protocols)
	}
}

// clientHeaderPlaceholder 匹配自定义 Header 值中引用的客户端请求头
var clientHeaderPlaceholder = regexp.MustCompile(`\{client_header:[^}]+\}`)

// applyChannelConfig 为上游请求补充渠道配置的默认参数并追加自定义 Header; model 与 stream 由转发流程决定, 不允许覆盖。
// 参数按缺省值语义处理: 客户端已经显式提供的键保留客户端值, 只有客户端没给的键才填入渠道配置,
// 由此渠道配置只决定"没指定时用什么", 不会悄悄改掉调用方明确要求的参数。
func applyChannelConfig(channel model.Channel, request *httpclient.Request) error {
	if channel.ParamOverride != "" {
		var overrides map[string]json.RawMessage
		if err := json.Unmarshal([]byte(channel.ParamOverride), &overrides); err != nil {
			return fmt.Errorf("invalid channel parameter override: %w", err)
		}
		body := request.Body
		// 覆盖键可能自带点号或冒号, 转义后再作为 sjson 路径使用, 避免被解析成嵌套路径。
		escape := strings.NewReplacer("\\", "\\\\", ".", "\\.", ":", "\\:")
		// 是否已存在按顶层键名判断: 键名可能含点号等字符, 用路径探测容易把 "a.b" 误判成嵌套路径。
		// 请求体不是 JSON 对象时 present 为空, 与原先一样按覆盖值写入。
		var present map[string]json.RawMessage
		_ = json.Unmarshal(body, &present)
		for key, value := range overrides {
			if key == "model" || key == "stream" {
				continue
			}
			// 客户端已提供该键时按子键补齐缺省值, 客户端自己写过的子键一律保留。
			// 只判顶层键是否存在的旧做法会让 {"thinking":{"budget_tokens":1024}} 把整个 thinking 默认值跳过,
			// 结果是调用方只调了一个子键, 却把渠道配的 type=disabled 一并丢掉。
			if existing, ok := present[key]; ok {
				merged, changed := mergeJSONDefaults(existing, value)
				if !changed {
					continue
				}
				value = merged
			}
			next, err := sjson.SetRawBytes(body, ":"+escape.Replace(key), value)
			if err != nil {
				return fmt.Errorf("apply channel parameter %q: %w", key, err)
			}
			body = next
		}
		request.Body = body
		if len(request.JSONBody) > 0 {
			request.JSONBody = slices.Clone(body)
		}
	}

	// 转换器已经写入的认证等敏感 Header 不允许被自定义配置覆盖。
	for _, header := range channel.CustomHeader {
		if request.Headers.Get(header.HeaderKey) != "" && httpclient.IsSensitiveHeader(header.HeaderKey) {
			continue
		}
		// 值中的 {client_header:xxx} 片段替换为客户端请求头 xxx 的实际值。
		value := clientHeaderPlaceholder.ReplaceAllStringFunc(header.HeaderValue, func(placeholder string) string {
			return request.Headers.Get(placeholder[len("{client_header:") : len(placeholder)-1])
		})
		request.Headers.Set(header.HeaderKey, value)
	}
	return nil
}

// mergeJSONDefaults 把 defaults 里 client 缺失的键补进去, 两边同为 JSON 对象时递归合并,
// 使 thinking.type 这类嵌套默认值在客户端只给出部分子键时依然生效。
// 返回合并结果与是否发生变化; 客户端值不是对象(含显式 null)时原样保留, 由调用方继续沿用客户端值。
func mergeJSONDefaults(client, defaults json.RawMessage) (json.RawMessage, bool) {
	var clientObject, defaultObject map[string]json.RawMessage
	if json.Unmarshal(client, &clientObject) != nil || json.Unmarshal(defaults, &defaultObject) != nil {
		return client, false
	}
	// 显式 null 与标量都会解成 nil, 都不是可合并的对象。
	if clientObject == nil || defaultObject == nil {
		return client, false
	}
	changed := false
	for key, value := range defaultObject {
		existing, ok := clientObject[key]
		if !ok {
			clientObject[key] = value
			changed = true
			continue
		}
		merged, subChanged := mergeJSONDefaults(existing, value)
		if subChanged {
			clientObject[key] = merged
			changed = true
		}
	}
	if !changed {
		return client, false
	}
	// 关闭 HTML 转义, 否则正文里的 <, >, & 会被改写成 \u003c 这类形式, 徒增困惑。
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(clientObject); err != nil {
		return client, false
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), true
}
