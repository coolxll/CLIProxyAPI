BINARY_NAME=CLIProxyAPI.exe
DEBUG_BINARY=CLIProxyAPI-debug.exe
MAIN_PATH=./cmd/server/main.go

# Release 编译参数：去掉调试符号以减小体积
LDFLAGS_RELEASE=-s -w

.PHONY: build build-debug tidy run clean langfuse-up langfuse-down

# 默认编译为 Release 版本
build: tidy
	go build -ldflags "$(LDFLAGS_RELEASE)" -o $(BINARY_NAME) $(MAIN_PATH)

# 编译为 Debug 版本（包含完整调试信息，文件名带 -debug）
build-debug: tidy
	go build -o $(DEBUG_BINARY) $(MAIN_PATH)

# 整理依赖
tidy:
	go mod tidy

# 运行（默认使用 Debug 版本方便调试）
run: build-debug
	OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4318 ./$(DEBUG_BINARY)

# 清理所有产物
clean:
	rm -f $(BINARY_NAME) $(DEBUG_BINARY)

# 启动监控全家桶
langfuse-up:
	docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse up -d

# 停止监控全家桶
langfuse-down:
	docker-compose -f docker-compose.langfuse.yml down
