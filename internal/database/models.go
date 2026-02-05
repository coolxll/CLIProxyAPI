package database

import (
	"time"
)

// RequestLog represents a single API request stored in the database.
type RequestLog struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	RequestID       string    `gorm:"index;size:64" json:"request_id"`
	Timestamp       time.Time `gorm:"index" json:"timestamp"`
	Method          string    `gorm:"size:10" json:"method"`
	Path            string    `gorm:"size:255" json:"path"`
	StatusCode      int       `gorm:"index" json:"status_code"`
	LatencyMs       int64     `json:"latency_ms"`
	ClientIP        string    `gorm:"size:45" json:"client_ip"`
	Model           string    `gorm:"size:100;index" json:"model"`
	Provider        string    `gorm:"size:50" json:"provider"` // e.g., "openai", "claude"
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
	IsError         bool      `gorm:"index" json:"is_error"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	AuthIndex       string    `gorm:"size:50" json:"auth_index"` // API Key index or identifier
}
