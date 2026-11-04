package model

import "time"

// RelayLog 是一次已结束请求的持久化记录, 也是日志页翻看历史的数据来源。
// 元数据留在库里, 便于按时间与条数裁剪、按状态筛选;
// 请求与响应正文体积可达数 MB, 按日期写成文件, 只在打开详情时按路径读取。
// ID 沿用进程内的请求序号, 启动时由库里最大的 ID 续上, 故不自增。
type RelayLog struct {
	ID                 uint64        `json:"id" gorm:"primaryKey;autoIncrement:false"` // 请求 ID, 与进程内序号一致。
	Status             string        `json:"status" gorm:"not null"`                   // 结束时的状态: success, failed, canceled。
	StartedAt          time.Time     `json:"started_at"`                               // 请求到达时间。
	Duration           time.Duration `json:"duration"`                                 // 请求从到达到结束的总耗时。
	FirstTokenDuration time.Duration `json:"first_token_duration"`                     // 流式响应首字节耗时。
	StreamDuration     time.Duration `json:"stream_duration"`                          // 流式响应从首字节到结束的耗时。
	ResponseDuration   time.Duration `json:"response_duration"`                        // 非流式响应从开始到完整提交的耗时。
	Model              string        `json:"model"`                                    // 客户端请求的模型名称, 即分组名称。
	ReasoningEffort    string        `json:"reasoning_effort"`                         // 客户端指定的思考等级。
	Protocol           uint8         `json:"protocol"`                                 // 客户端使用的协议位。
	GroupID            int           `json:"group_id"`                                 // 承载本请求的分组 ID。
	APIKeyName         string        `json:"api_key_name"`                             // 发起请求的 API Key 名称快照。
	PromptTokens       int64         `json:"prompt_tokens"`                            // 上游确认的输入词元数。
	CompletionTokens   int64         `json:"completion_tokens"`                        // 上游确认的输出词元数。
	TotalTokens        int64         `json:"total_tokens"`                             // 上游确认的总词元数。
	Cost               float64       `json:"cost"`                                     // 按模型单价折算的费用。
	Round              int           `json:"round"`                                    // 最终一轮的轮次序号。
	TargetChannelKey   string        `json:"target_channel_key"`                       // 最终选中渠道与凭据的名称。
	TargetModel        string        `json:"target_model"`                             // 实际请求上游的模型名称。
	TargetProtocol     uint8         `json:"target_protocol"`                          // 实际请求上游的协议位。
	Error              string        `json:"error"`                                    // 最终错误, 成功时为空。
	RequestBodyPath    string        `json:"-"`                                        // 请求体文件相对日志根目录的路径, 为空表示未留存。
	ResponseBodyPath   string        `json:"-"`                                        // 响应体文件相对日志根目录的路径, 为空表示未留存。
	RequestBodyBytes   int64         `json:"request_bytes"`                            // 请求体字节数, 便于界面标注体积。
	ResponseBodyBytes  int64         `json:"response_bytes"`                           // 响应体字节数。
	CreatedAt          time.Time     `json:"created_at" gorm:"index"`                  // 落库时间, 清理按它计算保留期。
}
