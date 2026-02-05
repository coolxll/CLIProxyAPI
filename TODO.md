# SQLite Request Logging - TODO List

## 🔴 [P1] Critical Issues (Must Fix Before Merge)
- [ ] **Git Tracking**: Add `internal/database/` to git. Current `git status` shows it is untracked, which will break CI/CD builds.
- [ ] **Gin Context Race Condition**: `HandleUsage` spawns a goroutine `logToDatabase(ctx, record)` which reads from `*gin.Context`. If the request completes before the goroutine runs, it will cause a race condition or panic.
    - *Fix*: Copy required metadata (Method, Path, ClientIP, Status Code, etc.) into a struct *before* starting the goroutine.
- [ ] **Logger Gate Logic**: DB logging is currently gated by `UsageStatisticsEnabled`. It should only depend on `EnableRequestLog` so that users can enable persistent logs without necessarily enabling in-memory aggregates.
- [ ] **Snapshot/Import Conflict**: `Snapshot()` currently ignores in-memory data when DB is enabled. This breaks the `ImportUsageStatistics` flow (which imports to memory then calls Snapshot).
    - *Fix*: Support merging or selecting between In-Memory and DB data in `Snapshot()`.
- [ ] **DB Driver Mismatch**: `database.go` only matches `"sqlite"`, but config docs mention `"sqlite3"`. Also, default case ignores `cfg.DSN` and hardcodes `cliproxy.db`.

## 🟡 [P2] Functional & Reliability Issues
- [ ] **RequestID Size/Security**: `RequestID` embeds the full API key and is limited to 64 chars. Long keys will cause DB insert failures, and this leaks full API keys in the management API.
    - *Fix*: Use a hash of the API key or a UUID, and ensure it fits in the column.
- [ ] **Metric Accuracy**:
    - [ ] **Latency Extraction**: Implement real latency tracking instead of hardcoded `0`.
    - [ ] **Real Error Messages**: Capture actual error messages instead of generic `"Request failed"`.
    - [ ] **Status Code Mapping**: Ensure `status_code` is consistently captured across all provider types (ensure it's set in context before plugin runs).

## 🟢 [P3] Performance & Optimization
- [ ] **Time Series Aggregation**: Implement DB-backed daily/hourly trend logic for `snapshotFromDB`.
    - [ ] Must be compatible with both SQLite and MySQL date/time functions.
- [ ] **Batch DB Writing**: Implement buffered channel/batch-insert (e.g., every 100 entries or every 5 seconds) to reduce disk I/O pressure.
- [ ] **Log Retention/Pruning**: Add a background worker or configuration setting to automatically delete logs older than X days (e.g., 30 days).

## 🔵 [P4] Refactoring & Testing
- [ ] **Database Migrations**: Switch from GORM's `AutoMigrate` to a formal migration tool (e.g. `golang-migrate`, `atlas`).
- [ ] **Service/Repository Layer**: Refactor SQL queries out of handlers and plugins into a dedicated repository package.
- [ ] **Enhanced Testing**: Add integration tests that verify database persistence with a real (temporary) SQLite file, rather than just in-memory mocks.
