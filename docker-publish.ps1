# docker-publish.ps1 - Build and publish Docker images automatically.
#
# The script injects Git build metadata and pushes images to Docker Hub.

# Stop on errors.
$ErrorActionPreference = "Stop"

# Configuration
$DOCKER_USER = "coolxll"
$REPO_NAME = "cli-proxy-api"

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  CLIProxyAPI Docker Publish Script" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 1. Log in to Docker.
Write-Host "Step 1: 检查 Docker 登录状态..." -ForegroundColor Yellow
docker login

# 2. Generate a date-based version.
Write-Host "Step 2: 生成版本号..." -ForegroundColor Yellow
$DATE_STR = Get-Date -Format "yyyyMMdd"
$VERSION_BASE = "v$DATE_STR"
$VERSION = $VERSION_BASE
$COUNTER = 2

# Increment the suffix when the version already exists locally.
while (docker images -q "${DOCKER_USER}/${REPO_NAME}:${VERSION}") {
    $VERSION = "${VERSION_BASE}-${COUNTER}"
    $COUNTER++
}

$IMAGE_NAME = "${DOCKER_USER}/${REPO_NAME}:${VERSION}"
$LATEST_NAME = "${DOCKER_USER}/${REPO_NAME}:latest"

# 3. Read Git build metadata.
Write-Host "Step 3: 获取 Git 元数据..." -ForegroundColor Yellow
$COMMIT  = (git rev-parse --short HEAD)
$BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

Write-Host "  - Target Version: $VERSION"
Write-Host "  - Commit: $COMMIT"
Write-Host "  - Build Date: $BUILD_DATE"

# 4. Build the image.
Write-Host "Step 4: 开始构建镜像: $IMAGE_NAME ..." -ForegroundColor Yellow
docker build `
    --build-arg VERSION=$VERSION `
    --build-arg COMMIT=$COMMIT `
    --build-arg BUILD_DATE=$BUILD_DATE `
    -t $IMAGE_NAME `
    -t $LATEST_NAME .

# 5. Push the image.
Write-Host "Step 5: 开始推送到 Docker Hub..." -ForegroundColor Yellow
Write-Host "  - 推送 $IMAGE_NAME"
docker push $IMAGE_NAME
Write-Host "  - 推送 $LATEST_NAME"
docker push $LATEST_NAME

Write-Host "========================================" -ForegroundColor Green
Write-Host "  推送完成！镜像已上传至 Docker Hub。" -ForegroundColor Green
Write-Host "  地址: https://hub.docker.com/r/$DOCKER_USER/cli-proxy-api" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
