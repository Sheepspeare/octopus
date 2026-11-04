package model

type APIKey struct {
	ID              int      `json:"id" gorm:"primaryKey"`
	Name            string   `json:"name" gorm:"not null"`
	APIKey          string   `json:"api_key" gorm:"not null"`
	Enabled         bool     `json:"enabled" gorm:"default:true"`
	ExpireAt        int64    `json:"expire_at,omitempty"`
	MaxCost         float64  `json:"max_cost,omitempty"`
	SupportedModels []string `json:"supported_models" gorm:"serializer:json"` // 允许访问的分组名称, 空表示不限制; 以 JSON 数组存储, 读写两侧都无需再拆分隔符。
}

// 用某个 API Key 从本机回环请求 /v1/models, 看这把钥匙真的能列出模型。
type APIKeyTestModelsRequest struct {
	ID int `json:"id" binding:"required"`
}

// 用某个 API Key 从本机回环发起一次补全请求, 走的是与外部客户端完全相同的入口。
type APIKeyTestChatRequest struct {
	ID    int    `json:"id" binding:"required"`
	Model string `json:"model" binding:"required"` // 分组名称, 即客户端填的 model。
}

// 外部调用测试的结果; 网关返回什么都原样带回, 由 OK 区分, 失败时 status 与 body 直接说明原因。
type APIKeyTestResult struct {
	OK      bool     `json:"ok"`                 // 是否得到 200 且内容可用。
	Status  int      `json:"status"`             // 网关返回的 HTTP 状态码; 网络层失败时为 0。
	Latency int64    `json:"latency_ms"`         // 从发出到读完响应的耗时, 单位毫秒。
	BaseURL string   `json:"base_url,omitempty"` // 本次测试实际请求的地址, 便于用户照抄到自己的客户端。
	Models  []string `json:"models,omitempty"`   // 仅模型列表测试返回。
	Content string   `json:"content,omitempty"`  // 仅补全测试返回, 模型输出的片段。
	Body    string   `json:"body,omitempty"`     // 网关响应原文的截断片段, 供失败时定位。
	Error   string   `json:"error,omitempty"`    // 网络层或解析失败的原因。
}
