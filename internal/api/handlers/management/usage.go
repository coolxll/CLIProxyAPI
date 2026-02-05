package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/database"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"gorm.io/gorm"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=usage_export_"+time.Now().Format("20060102_150405")+".json")
	c.Status(http.StatusOK)

	if database.DB != nil {
		exportUsageStreamFromDB(c.Request.Context(), c.Writer)
		return
	}

	enc := json.NewEncoder(c.Writer)
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	_ = enc.Encode(usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	var payload usageImportPayload
	if err := json.NewDecoder(c.Request.Body).Decode(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json or unsupported version"})
		return
	}

	if database.DB != nil {
		added, err := importUsageSnapshotToDB(c.Request.Context(), payload.Usage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to import usage"})
			return
		}
		snapshot := h.usageStats.Snapshot()
		c.JSON(http.StatusOK, gin.H{
			"added":           added,
			"skipped":         int64(0),
			"total_requests":  snapshot.TotalRequests,
			"failed_requests": snapshot.FailureCount,
		})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

// exportUsageStreamFromDB streams the entire usage database to a JSON encoder.
func exportUsageStreamFromDB(ctx context.Context, w io.Writer) {
	if database.DB == nil {
		return
	}

	enc := json.NewEncoder(w)

	// Calculate totals first
	totalRequests := int64(0)
	successCount := int64(0)
	failureCount := int64(0)
	totalTokens := int64(0)

	err := database.DB.WithContext(ctx).Model(&database.RequestLog{}).
		Select("COUNT(*) as requests, SUM(CASE WHEN is_error THEN 1 ELSE 0 END) as failure_count, SUM(total_tokens) as total_tokens").
		Row().Scan(&totalRequests, &failureCount, &totalTokens)
	if err != nil {
		_, _ = w.Write([]byte(`{"version":1,"exported_at":"`))
		_, _ = w.Write([]byte(time.Now().UTC().Format(time.RFC3339)))
		_, _ = w.Write([]byte(`","usage":{"total_requests":0,"success_count":0,"failure_count":0,"total_tokens":0,"apis":{`))
		_, _ = w.Write([]byte(`}}}`))
		return
	}
	successCount = totalRequests - failureCount

	// Start writing header
	_, _ = w.Write([]byte(`{"version":1,"exported_at":"`))
	_, _ = w.Write([]byte(time.Now().UTC().Format(time.RFC3339)))
	_, _ = w.Write([]byte(`","usage":{"total_requests":`))
	if err := enc.Encode(totalRequests); err != nil {
		return // Early return on write error
	}
	_, _ = w.Write([]byte(`,"success_count":`))
	if err := enc.Encode(successCount); err != nil {
		return // Early return on write error
	}
	_, _ = w.Write([]byte(`,"failure_count":`))
	if err := enc.Encode(failureCount); err != nil {
		return // Early return on write error
	}
	_, _ = w.Write([]byte(`,"total_tokens":`))
	if err := enc.Encode(totalTokens); err != nil {
		return // Early return on write error
	}
	_, _ = w.Write([]byte(`,"apis":{`))

	rows, err := database.DB.WithContext(ctx).Model(&database.RequestLog{}).
		Order("auth_index ASC, model ASC, timestamp ASC").
		Rows()
	if err != nil {
		_, _ = w.Write([]byte(`}}}`)) // close early
		return
	}
	defer rows.Close()

	var (
		currentAPI   string
		currentModel string
		firstAPI     = true
		firstModel   = true
		firstDetail  = true
		apiTotalReq  int64
		apiTotalTok  int64
		modTotalReq  int64
		modTotalTok  int64
	)

	type row struct {
		Timestamp    time.Time
		Provider     string
		Model        string
		AuthIndex    string
		InputTokens  int64
		OutputTokens int64
		TotalTokens  int64
		IsError      bool
	}

	writeAPIModelTotals := func() {
		if !firstModel {
			_, _ = w.Write([]byte(`],"total_requests":`))
			if err := enc.Encode(modTotalReq); err != nil {
				return // Early return on write error
			}
			_, _ = w.Write([]byte(`,"total_tokens":`))
			if err := enc.Encode(modTotalTok); err != nil {
				return // Early return on write error
			}
			_, _ = w.Write([]byte(`}`))
		}
	}

	writeAPITotals := func() {
		writeAPIModelTotals()
		if !firstAPI {
			_, _ = w.Write([]byte(`},"total_requests":`))
			if err := enc.Encode(apiTotalReq); err != nil {
				return // Early return on write error
			}
			_, _ = w.Write([]byte(`,"total_tokens":`))
			if err := enc.Encode(apiTotalTok); err != nil {
				return // Early return on write error
			}
			_, _ = w.Write([]byte(`}`))
		}
	}

	for rows.Next() {
		var r row
		if err := database.DB.ScanRows(rows, &r); err != nil {
			continue
		}

		apiName := r.AuthIndex
		if apiName == "" {
			apiName = "unknown"
		}
		modelName := r.Model
		if modelName == "" {
			modelName = "unknown"
		}

		if apiName != currentAPI {
			writeAPITotals()
			if !firstAPI {
				_, _ = w.Write([]byte(`,`))
			}
			firstAPI = false
			currentAPI = apiName
			currentModel = "" // reset model
			apiTotalReq = 0
			apiTotalTok = 0
			firstModel = true
			_, _ = w.Write([]byte(`"` + apiName + `":{"models":{`))
		}

		if modelName != currentModel {
			writeAPIModelTotals()
			if !firstModel {
				_, _ = w.Write([]byte(`,`))
			}
			firstModel = false
			currentModel = modelName
			modTotalReq = 0
			modTotalTok = 0
			firstDetail = true
			_, _ = w.Write([]byte(`"` + modelName + `":{"details":[`))
		}

		if !firstDetail {
			_, _ = w.Write([]byte(`,`))
		}
		firstDetail = false

		if err := enc.Encode(usage.RequestDetail{
			Timestamp: r.Timestamp,
			Source:    r.Provider,
			AuthIndex: r.AuthIndex,
			Tokens: usage.TokenStats{
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				TotalTokens:  r.TotalTokens,
			},
			Failed: r.IsError,
		}); err != nil {
			break // Stop processing on write error
		}

		modTotalReq++
		modTotalTok += r.TotalTokens
		apiTotalReq++
		apiTotalTok += r.TotalTokens
	}

	writeAPITotals()
	_, _ = w.Write([]byte(`}}}`))
}

// GetTrafficLogs returns paginated request logs from the database.

func importUsageSnapshotToDB(ctx context.Context, snapshot usage.StatisticsSnapshot) (int64, error) {
	if database.DB == nil {
		return 0, nil
	}
	var logs []database.RequestLog
	for apiName, api := range snapshot.APIs {
		for modelName, model := range api.Models {
			for _, detail := range model.Details {
				ts := detail.Timestamp
				if ts.IsZero() {
					ts = time.Now().UTC()
				}
				logs = append(logs, database.RequestLog{
					RequestID:    "", // allow DB to accept empty; not used for aggregation
					Timestamp:    ts,
					Provider:     detail.Source,
					Model:        modelName,
					AuthIndex:    firstNonEmpty(detail.AuthIndex, apiName),
					InputTokens:  detail.Tokens.InputTokens,
					OutputTokens: detail.Tokens.OutputTokens,
					TotalTokens:  detail.Tokens.TotalTokens,
					IsError:      detail.Failed,
				})
			}
		}
	}
	if len(logs) == 0 {
		return 0, nil
	}

	err := database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(logs, 500).Error
	})
	if err != nil {
		return 0, err
	}
	return int64(len(logs)), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (h *Handler) GetTrafficLogs(c *gin.Context) {
	if database.DB == nil {
		c.JSON(http.StatusOK, gin.H{
			"logs":  []database.RequestLog{},
			"total": 0,
			"page":  1,
			"size":  20,
			"error": "database not initialized",
		})
		return
	}

	page := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 0 {
		page = p
	}
	size := 20
	if s, err := strconv.Atoi(c.Query("size")); err == nil && s > 0 && s <= 100 {
		size = s
	}

	var logs []database.RequestLog
	var total int64

	// Base query
	query := database.DB.Model(&database.RequestLog{})

	// Filter by model (optional)
	if model := c.Query("model"); model != "" {
		query = query.Where("model = ?", model)
	}

	// Filter by status code (optional) -- exact match
	if status := c.Query("status"); status != "" {
		if code, err := strconv.Atoi(status); err == nil {
			query = query.Where("status_code = ?", code)
		}
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count logs"})
		return
	}

	// Fetch page
	offset := (page - 1) * size
	if err := query.Order("timestamp DESC, id DESC").Limit(size).Offset(offset).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query logs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  logs,
		"total": total,
		"page":  page,
		"size":  size,
	})
}
