# docker-publish.ps1 - 自动构建并推送 Docker 镜像脚本
#
# 该脚本会自动获取 Git 版本信息并注入镜像，然后推送到 Docker Hub。

# 设置错误时停止执行
$ErrorActionPreference = "Stop"

# --- 配置区 ---
$DOCKER_USER = "coolxll"
$REPO_NAME = "cli-proxy-api"

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  CLIProxyAPI Docker Publish Script" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 1. 登录 Docker
Write-Host "Step 1: 检查 Docker 登录状态..." -ForegroundColor Yellow
docker login

# 2. 生成基于日期的版本号
Write-Host "Step 2: 生成版本号..." -ForegroundColor Yellow
$DATE_STR = Get-Date -Format "yyyyMMdd"
$VERSION_BASE = "v$DATE_STR"
$VERSION = $VERSION_BASE
$COUNTER = 2

# 检查本地是否已存在该版本的镜像，如果存在则自动递增后缀
while (docker images -q "${DOCKER_USER}/${REPO_NAME}:${VERSION}") {
    $VERSION = "${VERSION_BASE}-${COUNTER}"
    $COUNTER++
}

$IMAGE_NAME = "${DOCKER_USER}/${REPO_NAME}:${VERSION}"
$LATEST_NAME = "${DOCKER_USER}/${REPO_NAME}:latest"

# 3. 获取构建元数据 (Git)
Write-Host "Step 3: 获取 Git 元数据..." -ForegroundColor Yellow
$COMMIT  = (git rev-parse --short HEAD)
$BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

Write-Host "  - Target Version: $VERSION"
Write-Host "  - Commit: $COMMIT"
Write-Host "  - Build Date: $BUILD_DATE"

# 4. 编译镜像
Write-Host "Step 4: 开始构建镜像: $IMAGE_NAME ..." -ForegroundColor Yellow
docker build `
    --build-arg VERSION=$VERSION `
    --build-arg COMMIT=$COMMIT `
    --build-arg BUILD_DATE=$BUILD_DATE `
    -t $IMAGE_NAME `
    -t $LATEST_NAME .

# 5. 推送镜像
Write-Host "Step 5: 开始推送到 Docker Hub..." -ForegroundColor Yellow
Write-Host "  - 推送 $IMAGE_NAME"
docker push $IMAGE_NAME
Write-Host "  - 推送 $LATEST_NAME"
docker push $LATEST_NAME

Write-Host "========================================" -ForegroundColor Green
Write-Host "  推送完成！镜像已上传至 Docker Hub。" -ForegroundColor Green
Write-Host "  地址: https://hub.docker.com/r/$DOCKER_USER/cli-proxy-api" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
