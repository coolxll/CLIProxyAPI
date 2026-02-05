package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
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

		db, openErr := gorm.Open(dialector, &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if openErr != nil {
			err = openErr
			return
		}

		DB = db

		// AutoMigrate the schema
		if migrateErr := DB.AutoMigrate(&RequestLog{}); migrateErr != nil {
			log.Printf("Failed to auto-migrate database: %v", migrateErr)
			// Decide if migration failure should be fatal or not.
			// Usually strict persistence implies we should probably return error, 
			// but keeping DB valid allows partial function. 
			// However, for safety let's return error if migration fails too?
			// For now, allow it but log error, DB is valid.
		}
	})

	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	return nil
}
