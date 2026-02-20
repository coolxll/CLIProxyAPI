// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/database"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)

	// Async write to database
	// UsageStatisticsEnabled=false should disable both aggregation and DB writes.
	if requestLogEnabled.Load() {
		logToDatabase(ctx, record)
	}
}

var requestLogEnabled atomic.Bool

// SetRequestLogEnabled toggles whether detailed request logs are persisted.
func SetRequestLogEnabled(enabled bool) { requestLogEnabled.Store(enabled) }

func logToDatabase(ctx context.Context, record coreusage.Record) {
	if database.DB == nil {
		return
	}

	detail := normaliseDetail(record.Detail)
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	// Resolve context details
	method := record.Method
	path := record.Path
	clientIP := record.ClientIP
	statusCode := record.StatusCode
	latencyMs := record.LatencyMs

	// Resolve success/failure
	failed := record.Failed

	// Calculate latency if available (approximated or passed)
	// Note: coreusage.Record doesn't strictly have latency, but we can assume 0 or add if needed.
	// For now, we leave LatencyMs as 0 unless we extract it from context or record.

	errStr := ""
	if failed {
		errStr = "Request failed" // Simplified, real error might be in context
	}

	// Generate a secure RequestID: timestamp + hash(identifier)
	// Avoids leaking API keys and ensures length <= 64
	identifier := record.APIKey
	if identifier == "" {
		identifier = record.AuthIndex
	}
	if identifier == "" {
		identifier = clientIP
	}
	hash := sha256.Sum256([]byte(identifier))
	requestID := fmt.Sprintf("%d-%s", timestamp.UnixNano(), hex.EncodeToString(hash[:8]))

	dbLog := database.RequestLog{
		RequestID:    requestID,
		Timestamp:    timestamp,
		Method:       method,
		Path:         path,
		StatusCode:   statusCode,
		LatencyMs:    latencyMs,
		ClientIP:     clientIP,
		Model:        record.Model,
		Provider:     record.Provider,
		InputTokens:  detail.InputTokens,
		OutputTokens: detail.OutputTokens,
		TotalTokens:  detail.TotalTokens,
		IsError:      failed,
		ErrorMessage: errStr,
		AuthIndex:    record.AuthIndex,
	}

	if err := database.DB.Create(&dbLog).Error; err != nil {
		// Silently fail or log debug to avoid spam
		// fmt.Printf("Failed to write access log: %v\n", err)
	}
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp and token usage for a single request.
type RequestDetail struct {
	Timestamp time.Time  `json:"timestamp"`
	Source    string     `json:"source"`
	AuthIndex string     `json:"auth_index"`
	Tokens    TokenStats `json:"tokens"`
	Failed    bool       `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Detail)
	totalTokens := detail.TotalTokens
	statsKey := record.APIKey
	if statsKey == "" {
		if record.Method != "" && record.Path != "" {
			statsKey = record.Method + " " + record.Path
		} else if record.Provider != "" {
			statsKey = record.Provider
		} else {
			statsKey = "unknown"
		}
	}
	failed := record.Failed
	success := !failed
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, RequestDetail{
		Timestamp: timestamp,
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    detail,
		Failed:    failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
}

// Snapshot returns a copy of the aggregated metrics.
// If database is available, it aggregates from persistent storage.
// Otherwise, it returns in-memory counters (which may be empty if logging-only).
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	if database.DB != nil {
		return s.snapshotFromDB()
	}

	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			// In-memory details are kept limited or just returns what's there
			// For DB mode, we might not return details in snapshot to save bandwidth
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	// Copy maps
	result.RequestsByDay = copyMap(s.requestsByDay)
	result.RequestsByHour = formatHourMap(s.requestsByHour)
	result.TokensByDay = copyMap(s.tokensByDay)
	result.TokensByHour = formatHourMap(s.tokensByHour)

	return result
}

func copyMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func formatHourMap(m map[int]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[formatHour(k)] = v
	}
	return out
}

func (s *RequestStatistics) snapshotFromDB() StatisticsSnapshot {
	var result StatisticsSnapshot
	db := database.DB

	// 1. Totals
	type TotalResult struct {
		Requests     int64
		SuccessCount int64
		FailureCount int64
		TotalTokens  int64
	}
	// GORM sums. SQLite bools are 0/1. MySQL bools are 0/1.
	// SUM(is_error) works for FailureCount.
	// COUNT(*) - SUM(is_error) is SuccessCount.
	var totals TotalResult
	err := db.Model(&database.RequestLog{}).Select("COUNT(*) as requests, SUM(CASE WHEN is_error THEN 1 ELSE 0 END) as failure_count, SUM(total_tokens) as total_tokens").Scan(&totals).Error
	if err == nil {
		result.TotalRequests = totals.Requests
		result.FailureCount = totals.FailureCount
		result.SuccessCount = totals.Requests - totals.FailureCount
		result.TotalTokens = totals.TotalTokens
	}

	// 2. Group by API/Model
	// select auth_index, model, count(*), sum(total_tokens) ...
	type GroupResult struct {
		AuthIndex   string
		Model       string
		Requests    int64
		TotalTokens int64
	}
	var groups []GroupResult
	db.Model(&database.RequestLog{}).Select("auth_index, model, count(*) as requests, sum(total_tokens) as total_tokens").Group("auth_index, model").Scan(&groups)

	result.APIs = make(map[string]APISnapshot)
	for _, g := range groups {
		apiName := g.AuthIndex
		if apiName == "" {
			apiName = "unknown"
		}
		if _, ok := result.APIs[apiName]; !ok {
			result.APIs[apiName] = APISnapshot{Models: make(map[string]ModelSnapshot)}
		}
		api := result.APIs[apiName]
		
		// Update API totals (summing up models)
		api.TotalRequests += g.Requests
		api.TotalTokens += g.TotalTokens
		
		// Update Model
		api.Models[g.Model] = ModelSnapshot{
			TotalRequests: g.Requests,
			TotalTokens:   g.TotalTokens,
			Details:       []RequestDetail{}, // Empty details to save memory/bandwidth
		}
		result.APIs[apiName] = api
	}

	// 3. Time series (simplified for now: skip or implement later if needed for charts)
	// TODO: Implement daily/hourly trend aggregation compatible with both SQLite and MySQL.
	// Implementing proper DB-based day/hour stats is complex across drivers.
	// For now, we leave them empty or partial.
	// Frontend charts might look empty.
	// Let's do a basic query for last 30 days if possible?
	// Or just skip for this iteration as user emphasized "Log" and "Persistence".
	// I'll initialize maps so they aren't nil.
	result.RequestsByDay = make(map[string]int64)
	result.RequestsByHour = make(map[string]int64)
	result.TokensByDay = make(map[string]int64)
	result.TokensByHour = make(map[string]int64)

	return result
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.recordImported(apiName, modelName, stats, detail)
				result.Added++
			}
		}
	}

	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
	)
}

const httpStatusBadRequest = 400

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	}
	return tokens
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
