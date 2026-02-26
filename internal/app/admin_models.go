package app

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ccLoad/internal/model"
	"ccLoad/internal/util"

	"github.com/gin-gonic/gin"
)

var fetchModelsHTTPStatusPattern = regexp.MustCompile(`HTTP\s+(\d{3})`)

// ============================================================
// Admin API: 获取渠道可用模型列表
// ============================================================

// FetchModelsRequest 获取模型列表请求参数
type FetchModelsRequest struct {
	ChannelType string `json:"channel_type" binding:"required"`
	URL         string `json:"url" binding:"required"`
	APIKey      string `json:"api_key" binding:"required"`
	ProxyURL    string `json:"proxy_url,omitempty"` // 代理URL（可选）
}

// FetchModelsResponse 获取模型列表响应
type FetchModelsResponse struct {
	Models      []model.ModelEntry `json:"models"`          // 模型列表（包含redirect_model便于编辑）
	ChannelType string             `json:"channel_type"`    // 渠道类型
	Source      string             `json:"source"`          // 数据来源: "api"(从API获取) 或 "predefined"(预定义)
	Debug       *FetchModelsDebug  `json:"debug,omitempty"` // 调试信息（仅开发环境）
}

// FetchModelsDebug 调试信息结构
type FetchModelsDebug struct {
	NormalizedType string `json:"normalized_type"` // 规范化后的渠道类型
	FetcherType    string `json:"fetcher_type"`    // 使用的Fetcher类型
	ChannelURL     string `json:"channel_url"`     // 渠道URL（脱敏）
}

// BatchRefreshModelsRequest 批量刷新模型请求
type BatchRefreshModelsRequest struct {
	ChannelIDs  []int64 `json:"channel_ids"`
	Mode        string  `json:"mode"`                   // merge(增量,默认) / replace(覆盖)
	ChannelType string  `json:"channel_type,omitempty"` // 可选：覆盖渠道类型
}

// BatchRefreshModelsItem 批量刷新单渠道结果
type BatchRefreshModelsItem struct {
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	Status      string `json:"status"` // updated / unchanged / failed
	Error       string `json:"error,omitempty"`
	Fetched     int    `json:"fetched"`
	Added       int    `json:"added,omitempty"`   // merge模式
	Removed     int    `json:"removed,omitempty"` // replace模式
	Total       int    `json:"total"`             // 刷新后总模型数
}

// HandleFetchModels 获取指定渠道的可用模型列表
// 路由: GET /admin/channels/:id/models/fetch
func (s *Server) HandleFetchModels(c *gin.Context) {
	channelID, err := ParseInt64Param(c, "id")
	if err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "无效的渠道ID")
		return
	}

	channel, err := s.channelCache.GetConfig(c.Request.Context(), channelID)
	if err != nil {
		RespondErrorMsg(c, http.StatusNotFound, "渠道不存在")
		return
	}

	keys, err := s.store.GetAPIKeys(c.Request.Context(), channelID)
	if err != nil || len(keys) == 0 {
		RespondErrorMsg(c, http.StatusBadRequest, "该渠道没有可用的API Key")
		return
	}
	apiKey := keys[0].APIKey

	channelType := c.Query("channel_type")
	if channelType == "" {
		channelType = channel.ChannelType
	}

	httpClient, clientErr := s.getClientForChannel(channel)
	if clientErr != nil {
		RespondErrorMsg(c, http.StatusInternalServerError, "获取代理客户端失败: "+clientErr.Error())
		return
	}

	response, err := s.fetchModelsWithURLFallback(c.Request.Context(), channel.ID, channel.GetURLs(), channelType, apiKey, httpClient)
	if err != nil {
		RespondErrorMsg(c, http.StatusOK, err.Error())
		return
	}

	RespondJSON(c, http.StatusOK, response)
}

// HandleFetchModelsPreview 支持未保存的渠道配置直接测试模型列表
// 路由: POST /admin/channels/models/fetch
func (s *Server) HandleFetchModelsPreview(c *gin.Context) {
	var req FetchModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "参数无效: "+err.Error())
		return
	}

	req.ChannelType = strings.TrimSpace(req.ChannelType)
	req.URL = strings.TrimSpace(req.URL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	if req.ChannelType == "" || req.URL == "" || req.APIKey == "" {
		RespondErrorMsg(c, http.StatusBadRequest, "channel_type、url、api_key为必填字段")
		return
	}

	normalizedURL, err := validateChannelURLs(req.URL)
	if err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "url无效: "+err.Error())
		return
	}

	// 获取代理客户端（如果指定了proxy_url）
	var httpClient *http.Client
	if req.ProxyURL != "" {
		var clientErr error
		httpClient, clientErr = s.proxyClients.GetClient(req.ProxyURL)
		if clientErr != nil {
			RespondErrorMsg(c, http.StatusBadRequest, "代理地址无效: "+clientErr.Error())
			return
		}
	} else {
		httpClient = s.client
	}

	tmpCfg := &model.Config{URL: normalizedURL}
	response, err := s.fetchModelsWithURLFallback(c.Request.Context(), 0, tmpCfg.GetURLs(), req.ChannelType, req.APIKey, httpClient)
	if err != nil {
		RespondErrorMsg(c, http.StatusOK, err.Error())
		return
	}
	RespondJSON(c, http.StatusOK, response)
}

// HandleBatchRefreshModels 批量获取并刷新渠道模型
// 路由: POST /admin/channels/models/refresh-batch
func (s *Server) HandleBatchRefreshModels(c *gin.Context) {
	var req BatchRefreshModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "参数无效: "+err.Error())
		return
	}

	channelIDs := normalizeBatchChannelIDs(req.ChannelIDs)
	if len(channelIDs) == 0 {
		RespondErrorMsg(c, http.StatusBadRequest, "channel_ids不能为空")
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "replace" {
		RespondErrorMsg(c, http.StatusBadRequest, "mode 仅支持 merge 或 replace")
		return
	}

	overrideType := strings.TrimSpace(req.ChannelType)
	ctx := c.Request.Context()

	results := make([]BatchRefreshModelsItem, 0, len(channelIDs))
	updated := 0
	unchanged := 0
	failed := 0
	changed := false

	for _, channelID := range channelIDs {
		item := BatchRefreshModelsItem{ChannelID: channelID}

		cfg, err := s.store.GetConfig(ctx, channelID)
		if err != nil {
			item.Status = "failed"
			item.Error = "渠道不存在"
			failed++
			results = append(results, item)
			continue
		}
		item.ChannelName = cfg.Name

		keys, err := s.store.GetAPIKeys(ctx, channelID)
		if err != nil || len(keys) == 0 {
			item.Status = "failed"
			item.Error = "该渠道没有可用的API Key"
			failed++
			results = append(results, item)
			continue
		}

		apiKey := strings.TrimSpace(keys[0].APIKey)
		if apiKey == "" {
			item.Status = "failed"
			item.Error = "该渠道没有可用的API Key"
			failed++
			results = append(results, item)
			continue
		}

		channelType := overrideType
		if channelType == "" {
			channelType = cfg.ChannelType
		}

		httpClient, clientErr := s.getClientForChannel(cfg)
		if clientErr != nil {
			item.Status = "failed"
			item.Error = "获取代理客户端失败: " + clientErr.Error()
			failed++
			results = append(results, item)
			continue
		}

		resp, err := s.fetchModelsWithURLFallback(ctx, cfg.ID, cfg.GetURLs(), channelType, apiKey, httpClient)
		if err != nil {
			item.Status = "failed"
			item.Error = err.Error()
			failed++
			results = append(results, item)
			continue
		}

		fetched := normalizeModelEntriesForSave(resp.Models)
		item.Fetched = len(fetched)

		switch mode {
		case "replace":
			removed, hasChange := replaceModelEntries(cfg, fetched)
			item.Removed = removed
			item.Total = len(cfg.ModelEntries)

			if !hasChange {
				item.Status = "unchanged"
				unchanged++
				results = append(results, item)
				continue
			}
		default: // merge
			added, hasChange := mergeModelEntries(cfg, fetched)
			item.Added = added
			item.Total = len(cfg.ModelEntries)

			if !hasChange {
				item.Status = "unchanged"
				unchanged++
				results = append(results, item)
				continue
			}
		}

		if _, err := s.store.UpdateConfig(ctx, channelID, cfg); err != nil {
			item.Status = "failed"
			item.Error = "保存模型失败: " + err.Error()
			failed++
			results = append(results, item)
			continue
		}

		item.Status = "updated"
		updated++
		changed = true
		results = append(results, item)
	}

	if changed {
		s.InvalidateChannelListCache()
	}

	RespondJSON(c, http.StatusOK, gin.H{
		"mode":      mode,
		"total":     len(channelIDs),
		"updated":   updated,
		"unchanged": unchanged,
		"failed":    failed,
		"results":   results,
	})
}

// fetchModelsWithURLFallback 按URL排序顺序抓取模型列表。
// 设计目标：多URL渠道下，单个URL异常不应导致整个管理操作失败。
func (s *Server) fetchModelsWithURLFallback(
	ctx context.Context,
	channelID int64,
	urls []string,
	channelType, apiKey string,
	httpClient *http.Client,
) (*FetchModelsResponse, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("渠道URL为空")
	}
	if len(urls) == 1 {
		return fetchModelsForConfig(ctx, channelType, urls[0], apiKey, httpClient)
	}

	selectorEnabled := s != nil && s.urlSelector != nil && channelID > 0
	var selector *URLSelector
	if selectorEnabled {
		selector = s.urlSelector
	}
	sortedURLs := orderURLsWithSelector(selector, channelID, urls)

	var lastErr error
	for _, entry := range sortedURLs {
		start := time.Now()
		resp, err := fetchModelsForConfig(ctx, channelType, entry.url, apiKey, httpClient)
		if err == nil {
			if selectorEnabled {
				latency := time.Since(start)
				if latency <= 0 {
					latency = time.Millisecond
				}
				s.urlSelector.RecordLatency(channelID, entry.url, latency)
			}
			return resp, nil
		}
		lastErr = err
		if selectorEnabled && shouldCooldownURLOnFetchModelsError(err) {
			s.urlSelector.CooldownURL(channelID, entry.url)
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("获取模型列表失败: 未找到可用URL")
}

func shouldCooldownURLOnFetchModelsError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	if statusCode, body, ok := parseFetchModelsStatus(errMsg); ok {
		classification := util.ClassifyHTTPResponseWithMeta(statusCode, nil, []byte(body))
		return classification.Level == util.ErrorLevelChannel
	}

	msgLower := strings.ToLower(errMsg)
	networkErrorMarkers := []string{
		"请求失败:",
		"读取响应失败:",
		"context deadline exceeded",
		"i/o timeout",
		"connection refused",
		"connection reset",
		"no route to host",
	}
	for _, marker := range networkErrorMarkers {
		if strings.Contains(msgLower, marker) {
			return true
		}
	}
	return false
}

func parseFetchModelsStatus(errMsg string) (statusCode int, body string, ok bool) {
	matches := fetchModelsHTTPStatusPattern.FindStringSubmatch(errMsg)
	if len(matches) < 2 {
		return 0, "", false
	}

	code, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, "", false
	}

	body = errMsg
	if fullMatch := matches[0]; fullMatch != "" {
		if idx := strings.Index(errMsg, fullMatch); idx >= 0 {
			body = strings.TrimLeft(errMsg[idx+len(fullMatch):], "): \t")
		}
	}
	return code, strings.TrimSpace(body), true
}

func fetchModelsForConfig(ctx context.Context, channelType, channelURL, apiKey string, httpClient *http.Client) (*FetchModelsResponse, error) {
	normalizedType := util.NormalizeChannelType(channelType)
	source := determineSource(channelType)

	var (
		modelNames []string
		fetcherStr string
		err        error
	)

	if source == "predefined" {
		modelNames = util.PredefinedModels(normalizedType)
		if len(modelNames) == 0 {
			return nil, fmt.Errorf("渠道类型:%s 暂无预设模型列表", normalizedType)
		}
		fetcherStr = "predefined"
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		fetcher := util.NewModelsFetcher(channelType)
		fetcherStr = fmt.Sprintf("%T", fetcher)

		modelNames, err = fetcher.FetchModels(ctx, channelURL, apiKey, httpClient)
		if err != nil {
			return nil, fmt.Errorf(
				"获取模型列表失败(渠道类型:%s, 规范化类型:%s, 数据来源:%s): %w",
				channelType, normalizedType, source, err,
			)
		}
	}

	models := make([]model.ModelEntry, len(modelNames))
	for i, name := range modelNames {
		models[i] = model.ModelEntry{
			Model:         name,
			RedirectModel: name,
		}
	}

	return &FetchModelsResponse{
		Models:      models,
		ChannelType: channelType,
		Source:      source,
		Debug: &FetchModelsDebug{
			NormalizedType: normalizedType,
			FetcherType:    fetcherStr,
			ChannelURL:     channelURL,
		},
	}, nil
}

// determineSource 判断模型列表来源（辅助函数）
func determineSource(channelType string) string {
	switch util.NormalizeChannelType(channelType) {
	case util.ChannelTypeOpenAI, util.ChannelTypeGemini, util.ChannelTypeAnthropic, util.ChannelTypeCodex:
		return "api"
	default:
		return "predefined"
	}
}

func normalizeModelEntriesForSave(entries []model.ModelEntry) []model.ModelEntry {
	if len(entries) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(entries))
	normalized := make([]model.ModelEntry, 0, len(entries))
	for _, entry := range entries {
		clean := entry
		if err := clean.Validate(); err != nil {
			continue
		}
		if clean.Model == "" {
			continue
		}
		if clean.RedirectModel == clean.Model {
			clean.RedirectModel = ""
		}
		key := strings.ToLower(clean.Model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, clean)
	}
	return normalized
}

func mergeModelEntries(cfg *model.Config, fetched []model.ModelEntry) (added int, changed bool) {
	existing := make(map[string]struct{}, len(cfg.ModelEntries))
	for _, entry := range cfg.ModelEntries {
		existing[strings.ToLower(entry.Model)] = struct{}{}
	}

	for _, entry := range fetched {
		key := strings.ToLower(entry.Model)
		if _, exists := existing[key]; exists {
			continue
		}
		cfg.ModelEntries = append(cfg.ModelEntries, entry)
		existing[key] = struct{}{}
		added++
	}

	return added, added > 0
}

func replaceModelEntries(cfg *model.Config, fetched []model.ModelEntry) (removed int, changed bool) {
	oldEntries := cfg.ModelEntries
	oldSet := make(map[string]struct{}, len(oldEntries))
	newSet := make(map[string]struct{}, len(fetched))

	for _, entry := range oldEntries {
		oldSet[strings.ToLower(entry.Model)] = struct{}{}
	}
	for _, entry := range fetched {
		newSet[strings.ToLower(entry.Model)] = struct{}{}
	}
	for key := range oldSet {
		if _, exists := newSet[key]; !exists {
			removed++
		}
	}

	if len(oldEntries) == len(fetched) {
		same := true
		for i := range oldEntries {
			if oldEntries[i].Model != fetched[i].Model || oldEntries[i].RedirectModel != fetched[i].RedirectModel {
				same = false
				break
			}
		}
		if same {
			return 0, false
		}
	}

	cfg.ModelEntries = fetched
	return removed, true
}
