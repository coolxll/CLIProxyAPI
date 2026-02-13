BINARY_NAME=CLIProxyAPI.exe
DEBUG_BINARY=CLIProxyAPI-debug.exe
MAIN_PATH=./cmd/server/main.go

# Release 编译参数：去掉调试符号以减小体积
LDFLAGS_RELEASE=-s -w

.DEFAULT_GOAL := help

.PHONY: build build-debug tidy run clean langfuse-up langfuse-down help

# 显示帮助信息
help:
	@echo "可用命令:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# 默认编译为 Release 版本
build: tidy ## 编译为 Release 版本
	go build -ldflags "$(LDFLAGS_RELEASE)" -o $(BINARY_NAME) $(MAIN_PATH)

# 编译为 Debug 版本（包含完整调试信息，文件名带 -debug）
build-debug: tidy ## 编译为 Debug 版本（包含完整调试信息）
	go build -o $(DEBUG_BINARY) $(MAIN_PATH)

# 整理依赖
tidy: ## 整理 Go 依赖模块
	go mod tidy

# 运行（默认使用 Debug 版本方便调试）
run: build-debug ## 编译并运行调试版本
	OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4318 ./$(DEBUG_BINARY)

# 清理所有产物
clean: ## 清理编译产物
	rm -f $(BINARY_NAME) $(DEBUG_BINARY)

# 启动监控全家桶
langfuse-up: ## 启动 Langfuse 监控容器
	docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse up -d

# 停止监控全家桶
langfuse-down: ## 停止 Langfuse 监控容器
	docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse down

# 重启监控全家桶（确保重新加载环境变量）
langfuse-restart: ## 重启 Langfuse 监控容器（重新加载配置）
	docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse down
	docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse up -d --remove-orphans
