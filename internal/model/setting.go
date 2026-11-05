package model

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/dlclark/regexp2"
)

type SettingKey string

const (
	SettingKeyProxyURL                SettingKey = "proxy_url"
	SettingKeyStatsSaveInterval       SettingKey = "stats_save_interval"        // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval SettingKey = "model_info_update_interval" // 模型信息更新间隔(小时)
	SettingKeyCORSAllowOrigins        SettingKey = "cors_allow_origins"         // 跨域白名单(逗号分隔, 如 "example.com,example2.com"). 为空不允许跨域, "*"允许所有
	SettingKeyModelFilter             SettingKey = "model_filter"               // 渠道获取模型时的全局过滤表达式; 留空表示不过滤
	SettingKeyLogRequestBody          SettingKey = "log_request_body"            // 是否记录请求体; 关闭后日志详情不再保存请求原文
	SettingKeyLogResponseBody         SettingKey = "log_response_body"           // 是否记录响应体; 关闭后日志详情不再保存响应原文
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},       // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},          // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"}, // 默认24小时更新一次模型信息
		{Key: SettingKeyModelFilter, Value: ""},               // 默认不过滤模型
		{Key: SettingKeyLogRequestBody, Value: "true"},        // 默认记录请求体
		{Key: SettingKeyLogResponseBody, Value: "true"},       // 默认记录响应体
	}
}

func (s *Setting) Validate() error {
	switch s.Key {
	case SettingKeyStatsSaveInterval:
		// 非数字会让启动时的 SettingGetInt 失败、定时任务根本不注册; 0 或负数会让 Register 静默跳过。
		minutes, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("stats save interval must be an integer")
		}
		if minutes <= 0 {
			return fmt.Errorf("stats save interval must be greater than 0")
		}
		if minutes > 1440 {
			return fmt.Errorf("stats save interval must not exceed 1440")
		}
		return nil
	case SettingKeyModelInfoUpdateInterval:
		_, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("model info update interval must be an integer")
		}
		return nil
	case SettingKeyModelFilter:
		if s.Value == "" {
			return nil
		}
		// 与渠道侧一致用 ECMAScript 方言校验, 避免设置能存但探测时编译失败。
		if _, err := regexp2.Compile(s.Value, regexp2.ECMAScript); err != nil {
			return fmt.Errorf("model filter regex is invalid: %w", err)
		}
		return nil
	case SettingKeyLogRequestBody:
		// 存成非法布尔会让读取方每次解析失败, 只能退回默认值, 界面上的开关将失去意义。
		if _, err := strconv.ParseBool(s.Value); err != nil {
			return fmt.Errorf("log request body must be true or false")
		}
		return nil
	case SettingKeyLogResponseBody:
		if _, err := strconv.ParseBool(s.Value); err != nil {
			return fmt.Errorf("log response body must be true or false")
		}
		return nil
	case SettingKeyProxyURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("proxy URL is invalid: %w", err)
		}
		validSchemes := map[string]bool{
			"http":    true,
			"https":   true,
			"socks5":  true,
			"socks5h": true,
		}
		if !validSchemes[parsedURL.Scheme] {
			return fmt.Errorf("proxy URL scheme must be http, https, socks5, or socks5h")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("proxy URL must have a host")
		}
		return nil
	}

	return nil
}
