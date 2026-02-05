package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/database"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/stretchr/testify/assert"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// setupTestDB sets up an in-memory SQLite database for testing.
// NOTE: CGO_ENABLED=0 environment might cause issues with standard SQLite driver.
// However, modern gorm.io/driver/sqlite might work if it uses a pure Go implementation or if CGO is enabled.
// If this fails due to CGO issues, we might need a pure Go sqlite driver like 'modernc.org/sqlite' or verify environment.
// But first, let's try the standard way.
func setupTestDB() *gorm.DB {
	// Use in-memory DB
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		// Fallback for environments where in-memory might be tricky or driver issues
		// Trying a temporary file if memory fails, but usually panic is appropriate for test setup failure
		panic(fmt.Sprintf("failed to connect database: %v", err))
	}
	if err := db.AutoMigrate(&database.RequestLog{}); err != nil {
		panic(fmt.Sprintf("failed to migrate database: %v", err))
	}
	return db
}

func setupRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/logs", h.GetTrafficLogs)
	r.GET("/usage/export", h.ExportUsageStatistics)
	r.POST("/usage/import", h.ImportUsageStatistics)
	return r
}

func TestGetTrafficLogs(t *testing.T) {
	// Setup DB
	db := setupTestDB()

	// Use a lock or just assign since tests run sequentially here usually,
	// but be careful with parallel tests.
	oldDB := database.DB
	database.DB = db
	defer func() { database.DB = oldDB }()

	// Seed data
	logs := []database.RequestLog{}
	for i := 1; i <= 25; i++ {
		model := "gpt-3.5-turbo"
		status := 200
		if i%5 == 0 {
			model = "gpt-4"
		}
		if i%3 == 0 {
			status = 400
		}

		logs = append(logs, database.RequestLog{
			RequestID:   fmt.Sprintf("req-%d", i),
			Timestamp:   time.Now().Add(time.Duration(-i) * time.Minute),
			Method:      "POST",
			Path:        "/v1/chat/completions",
			StatusCode:  status,
			Model:       model,
			Provider:    "openai",
			IsError:     status != 200,
		})
	}
	db.CreateInBatches(logs, 100)

	h := &Handler{usageStats: usage.GetRequestStatistics()}
	router := setupRouter(h)

	t.Run("Default Pagination", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		assert.Equal(t, float64(25), response["total"])
		assert.Equal(t, float64(1), response["page"])
		assert.Equal(t, float64(20), response["size"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 20)
	})

	t.Run("Custom Pagination", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs?page=2&size=10", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		assert.Equal(t, float64(25), response["total"])
		assert.Equal(t, float64(2), response["page"])
		assert.Equal(t, float64(10), response["size"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 10)
	})

	t.Run("Filter by Model", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs?model=gpt-4", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		// 25 items total. Items at index 5, 10, 15, 20, 25 are gpt-4 (1-based index)
		// Total 5 items
		assert.Equal(t, float64(5), response["total"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 5)

		for _, l := range logsList {
			logMap := l.(map[string]interface{})
			assert.Equal(t, "gpt-4", logMap["model"])
		}
	})

	t.Run("Filter by Status", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs?status=400", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		// 25 items total. Items at index 3, 6, 9, 12, 15, 18, 21, 24 are status 400
		// Total 8 items
		assert.Equal(t, float64(8), response["total"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 8)

		for _, l := range logsList {
			logMap := l.(map[string]interface{})
			assert.Equal(t, float64(400), logMap["status_code"])
		}
	})

	t.Run("Filter combined", func(t *testing.T) {
		w := httptest.NewRecorder()
		// Multiples of 5 are gpt-4 (5, 10, 15, 20, 25)
		// Multiples of 3 are status 400 (3, 6, 9, 12, 15, 18, 21, 24)
		// Intersection: 15 (gpt-4 and status 400)
		req, _ := http.NewRequest("GET", "/logs?model=gpt-4&status=400", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		assert.Equal(t, float64(1), response["total"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 1)
	})

	t.Run("No Results", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs?model=non-existent", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		assert.Equal(t, float64(0), response["total"])

		logsList := response["logs"].([]interface{})
		assert.Len(t, logsList, 0)
	})

	t.Run("Database Uninitialized", func(t *testing.T) {
		// Temporarily unset DB
		currentDB := database.DB
		database.DB = nil
		defer func() { database.DB = currentDB }()

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/logs", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		assert.Equal(t, "database not initialized", response["error"])
	})

	t.Run("Export then Import in DB mode restores totals", func(t *testing.T) {
		// export usage from seeded DB
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/usage/export", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var exported usageExportPayload
		err := json.Unmarshal(w.Body.Bytes(), &exported)
		assert.NoError(t, err)
		assert.Greater(t, exported.Usage.TotalRequests, int64(0))

		// clear db table
		err = db.Exec("DELETE FROM request_logs").Error
		assert.NoError(t, err)

		// import usage into db
		payload := usageImportPayload{Version: 1, Usage: exported.Usage}
		body, _ := json.Marshal(payload)
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("POST", "/usage/import", bytes.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)

		// export again, totals should match original export
		w3 := httptest.NewRecorder()
		req3, _ := http.NewRequest("GET", "/usage/export", nil)
		router.ServeHTTP(w3, req3)
		assert.Equal(t, http.StatusOK, w3.Code)

		var exported2 usageExportPayload
		err = json.Unmarshal(w3.Body.Bytes(), &exported2)
		assert.NoError(t, err)
		assert.Equal(t, exported.Usage.TotalRequests, exported2.Usage.TotalRequests)
		assert.Equal(t, exported.Usage.TotalTokens, exported2.Usage.TotalTokens)
		assert.Equal(t, exported.Usage.FailureCount, exported2.Usage.FailureCount)
	})
}

