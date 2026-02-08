package util

import (
	"encoding/json"
	"strings"
)

// sensitiveKeys 敏感字段名列表（用于脱敏）
var sensitiveKeys = []string{
	"api_key", "apikey", "api-key",
	"authorization", "auth",
	"x-api-key", "x-auth-token",
	"password", "secret", "token",
	"bearer", "credential",
}

// SanitizeRequestBody 脱敏并截断请求体
// 移除敏感字段（api_key, authorization等），超过maxSize则截断
func SanitizeRequestBody(body []byte, maxSize int) string {
	if len(body) == 0 {
		return ""
	}

	// 尝试解析为JSON并脱敏
	sanitized := sanitizeJSONBody(body)

	// 截断处理
	if maxSize > 0 && len(sanitized) > maxSize {
		return sanitized[:maxSize] + "\n[truncated]"
	}
	return sanitized
}

// SanitizeResponseBody 脱敏并截断响应体
// 超过maxSize则截断
func SanitizeResponseBody(body []byte, maxSize int) string {
	if len(body) == 0 {
		return ""
	}

	result := string(body)

	// 截断处理
	if maxSize > 0 && len(result) > maxSize {
		return result[:maxSize] + "\n[truncated]"
	}
	return result
}

// sanitizeJSONBody 尝试解析JSON并移除敏感字段
func sanitizeJSONBody(body []byte) string {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		// 非JSON格式，直接返回原始内容
		return string(body)
	}

	// 递归脱敏
	sanitizeMap(data)

	// 重新序列化（保持可读性）
	result, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return string(body)
	}
	return string(result)
}

// sanitizeMap 递归脱敏map中的敏感字段
func sanitizeMap(data map[string]any) {
	for key, value := range data {
		keyLower := strings.ToLower(key)

		// 检查是否为敏感字段
		if isSensitiveKey(keyLower) {
			data[key] = "[REDACTED]"
			continue
		}

		// 递归处理嵌套结构
		switch v := value.(type) {
		case map[string]any:
			sanitizeMap(v)
		case []any:
			sanitizeSlice(v)
		}
	}
}

// sanitizeSlice 递归脱敏slice中的敏感字段
func sanitizeSlice(data []any) {
	for i, item := range data {
		switch v := item.(type) {
		case map[string]any:
			sanitizeMap(v)
		case []any:
			sanitizeSlice(v)
		case string:
			// 检查是否看起来像API Key（长字符串，包含特定前缀）
			if looksLikeAPIKey(v) {
				data[i] = "[REDACTED]"
			}
		}
	}
}

// isSensitiveKey 检查key是否为敏感字段
func isSensitiveKey(key string) bool {
	for _, sensitive := range sensitiveKeys {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}

// looksLikeAPIKey 检查字符串是否看起来像API Key
func looksLikeAPIKey(s string) bool {
	// 常见API Key前缀
	prefixes := []string{"sk-", "pk-", "api-", "key-", "bearer "}
	sLower := strings.ToLower(s)
	for _, prefix := range prefixes {
		if strings.HasPrefix(sLower, prefix) && len(s) > 20 {
			return true
		}
	}
	return false
}
