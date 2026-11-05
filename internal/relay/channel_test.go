package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/httpclient"
)

// applyOverride 用给定的渠道参数处理一份请求体, 返回处理后的 JSON 对象。
func applyOverride(t *testing.T, paramOverride, body string) map[string]any {
	t.Helper()

	request := &httpclient.Request{Body: []byte(body), JSONBody: []byte(body)}
	channel := model.Channel{ChannelConfig: model.ChannelConfig{ParamOverride: paramOverride}}
	if err := applyChannelConfig(channel, request); err != nil {
		t.Fatalf("applyChannelConfig(%s): %v", paramOverride, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(request.Body, &parsed); err != nil {
		t.Fatalf("result is not a JSON object: %v (%s)", err, request.Body)
	}
	return parsed
}

// TestChannelOverrideFillsMissingTopLevelKey 验证客户端没传的顶层键被补上。
func TestChannelOverrideFillsMissingTopLevelKey(t *testing.T) {
	got := applyOverride(t, `{"temperature":0.3}`, `{"messages":[]}`)
	if got["temperature"] != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", got["temperature"])
	}
}

// TestChannelOverrideKeepsClientTopLevelKey 验证客户端显式给出的顶层键不被改写。
func TestChannelOverrideKeepsClientTopLevelKey(t *testing.T) {
	got := applyOverride(t, `{"temperature":0.3}`, `{"temperature":0.9}`)
	if got["temperature"] != 0.9 {
		t.Fatalf("temperature = %v, want 0.9", got["temperature"])
	}
}

// TestChannelOverrideMergesNestedDefaults 验证客户端只给部分子键时, 其余子键仍按渠道默认值补齐。
// 这是本次修正的核心: 调用方传 thinking.budget_tokens 不应连带丢掉渠道配的 thinking.type=disabled。
func TestChannelOverrideMergesNestedDefaults(t *testing.T) {
	got := applyOverride(t, `{"thinking":{"type":"disabled"}}`, `{"thinking":{"budget_tokens":1024}}`)
	thinking, ok := got["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want object", got["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking.type = %v, want disabled", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking.budget_tokens = %v, want 1024", thinking["budget_tokens"])
	}
}

// TestChannelOverrideKeepsClientNestedKey 验证客户端写过的子键不被渠道默认值覆盖。
func TestChannelOverrideKeepsClientNestedKey(t *testing.T) {
	got := applyOverride(t, `{"thinking":{"type":"disabled"}}`, `{"thinking":{"type":"enabled"}}`)
	thinking, ok := got["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want object", got["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
	}
}

// TestChannelOverrideKeepsExplicitNull 验证显式 null 被视为客户端的选择, 不被默认值替换。
func TestChannelOverrideKeepsExplicitNull(t *testing.T) {
	got := applyOverride(t, `{"thinking":{"type":"disabled"}}`, `{"thinking":null}`)
	if value, exists := got["thinking"]; !exists || value != nil {
		t.Fatalf("thinking = %#v, want explicit null", value)
	}
}

// TestChannelOverrideKeepsScalar 验证客户端给出的非对象值同样保留, 不与对象默认值合并。
func TestChannelOverrideKeepsScalar(t *testing.T) {
	got := applyOverride(t, `{"thinking":{"type":"disabled"}}`, `{"thinking":false}`)
	if got["thinking"] != false {
		t.Fatalf("thinking = %#v, want false", got["thinking"])
	}
}

// TestChannelOverrideIgnoresModelAndStream 验证 model 与 stream 始终由转发流程决定。
func TestChannelOverrideIgnoresModelAndStream(t *testing.T) {
	got := applyOverride(t, `{"model":"hijacked","stream":true}`, `{"model":"client-model"}`)
	if got["model"] != "client-model" {
		t.Fatalf("model = %v, want client-model", got["model"])
	}
	if _, exists := got["stream"]; exists {
		t.Fatalf("stream = %v, want absent", got["stream"])
	}
}

// TestChannelOverrideKeepsDottedKeyLiteral 验证键名里的点号按字面量处理, 不被当成嵌套路径。
func TestChannelOverrideKeepsDottedKeyLiteral(t *testing.T) {
	got := applyOverride(t, `{"a.b":1}`, `{}`)
	if got["a.b"] != float64(1) {
		t.Fatalf("a.b = %v, want 1 (nested path leaked)", got["a.b"])
	}
	if _, exists := got["a"]; exists {
		t.Fatalf("a = %#v, want absent", got["a"])
	}
}

// TestChannelOverrideKeepsHTMLEscaping 验证合并不会把正文里的尖括号改写成 Unicode 转义。
func TestChannelOverrideKeepsHTMLEscaping(t *testing.T) {
	request := &httpclient.Request{
		Body:     []byte(`{"thinking":{"note":"a<b>c"},"messages":[]}`),
		JSONBody: []byte(`{"thinking":{"note":"a<b>c"},"messages":[]}`),
	}
	channel := model.Channel{ChannelConfig: model.ChannelConfig{ParamOverride: `{"thinking":{"type":"disabled"}}`}}
	if err := applyChannelConfig(channel, request); err != nil {
		t.Fatalf("applyChannelConfig: %v", err)
	}
	if body := string(request.Body); !strings.Contains(body, "a<b>c") {
		t.Fatalf("body = %s, want original < > kept", body)
	}
}

// TestChannelOverrideInvalidJSON 验证渠道配置本身不是 JSON 对象时报错, 而不是静默忽略。
func TestChannelOverrideInvalidJSON(t *testing.T) {
	request := &httpclient.Request{Body: []byte(`{}`)}
	if err := applyChannelConfig(model.Channel{ChannelConfig: model.ChannelConfig{ParamOverride: `[1,2]`}}, request); err == nil {
		t.Fatal("want error for non-object override, got nil")
	}
}
