package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	DB   *gorm.DB
	once sync.Once
)

// Config holds the database configuration
type Config struct {
	Driver string // "sqlite" or "mysql"
	DSN    string // Data Source Name (connection string)
	LogDir string // Directory for SQLite file (if using sqlite)
}

// Init initializes the database connection
func Init(cfg Config) error {
	var err error
	once.Do(func() {
		var dialector gorm.Dialector

		switch strings.ToLower(cfg.Driver) {
		case "mysql":
			dialector = mysql.Open(cfg.DSN)
		case "sqlite", "sqlite3":
			dbPath := cfg.DSN
			if dbPath == "" {
				dbPath = "cliproxy.db"
				if cfg.LogDir != "" {
					if err := os.MkdirAll(cfg.LogDir, 0755); err != nil {
						log.Printf("Failed to create log directory: %v", err)
					}
					dbPath = filepath.Join(cfg.LogDir, "cliproxy.db")
				}
			}
			dialector = sqlite.Open(dbPath)
		default:
			// Default to SQLite with path logic
			dbPath := cfg.DSN
			if dbPath == "" {
				dbPath = "cliproxy.db"
				if cfg.LogDir != "" {
					_ = os.MkdirAll(cfg.LogDir, 0755)
					dbPath = filepath.Join(cfg.LogDir, "cliproxy.db")
				}
			}
			dialector = sqlite.Open(dbPath)
		}

		DB, err = gorm.Open(dialector, &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			return
		}

		// AutoMigrate the schema
		err = DB.AutoMigrate(&RequestLog{})
	})

	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	return nil
}
