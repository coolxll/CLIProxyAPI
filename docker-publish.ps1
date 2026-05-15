# docker-publish.ps1 - 自动构建并推送 Docker 镜像脚本
#
# 该脚本会自动获取 Git 版本信息并注入镜像，然后推送到 Docker Hub。

# 设置错误时停止执行
$ErrorActionPreference = "Stop"

# --- 配置区 ---
$DOCKER_USER = "coolxll"
$IMAGE_NAME = "$DOCKER_USER/cli-proxy-api:latest"

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  CLIProxyAPI Docker Publish Script" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 1. 登录 Docker
# 如果您已经处于登录状态，此步骤会很快跳过
Write-Host "Step 1: 检查 Docker 登录状态..." -ForegroundColor Yellow
docker login

# 2. 获取元数据
Write-Host "Step 2: 获取构建元数据 (Git)..." -ForegroundColor Yellow
$VERSION = (git describe --tags --always --dirty)
$COMMIT  = (git rev-parse --short HEAD)
$BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

Write-Host "  - Version: $VERSION"
Write-Host "  - Commit: $COMMIT"
Write-Host "  - Date: $BUILD_DATE"

# 3. 编译镜像
Write-Host "Step 3: 开始构建镜像: $IMAGE_NAME ..." -ForegroundColor Yellow
docker build `
    --build-arg VERSION=$VERSION `
    --build-arg COMMIT=$COMMIT `
    --build-arg BUILD_DATE=$BUILD_DATE `
    -t $IMAGE_NAME .

# 4. 推送镜像
Write-Host "Step 4: 开始推送到 Docker Hub..." -ForegroundColor Yellow
docker push $IMAGE_NAME

Write-Host "========================================" -ForegroundColor Green
Write-Host "  推送完成！镜像已上传至 Docker Hub。" -ForegroundColor Green
Write-Host "  地址: https://hub.docker.com/r/$DOCKER_USER/cli-proxy-api" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
