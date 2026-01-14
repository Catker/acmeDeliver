#!/bin/bash
# =============================================================================
# acmeDeliver 一键更新脚本
# 从 GitHub Releases 下载并更新 acmedeliver-server 和 acmedeliver-client
# =============================================================================

set -e

# ===== 默认配置（均可通过命令行参数覆盖）=====
INSTALL_DIR="/usr/local/bin"          # --install-dir DIR
SERVICE_NAME="acmeDeliver"            # --service NAME
COMPONENT="all"                       # --component all/server/client
VERSION="latest"                      # --version VER
DRY_RUN=false                         # --dry-run
SKIP_RESTART=false                    # --skip-restart
FORCE=false                           # --force

# ===== 常量 =====
REPO="Catker/acmeDeliver"
GITHUB_API="https://api.github.com/repos/${REPO}/releases"
GITHUB_DOWNLOAD="https://github.com/${REPO}/releases/download"
TMP_DIR=$(mktemp -d)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ===== 颜色输出 =====
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info()  { echo -e "${BLUE}[INFO]${NC} $1"; }
log_ok()    { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# ===== 清理函数 =====
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

# ===== 帮助信息 =====
show_help() {
    cat << EOF
acmeDeliver 一键更新脚本

用法:
  $(basename "$0") [选项]
  curl -sSL <URL> | bash -s -- [选项]

选项:
  --install-dir DIR    安装目录 (默认: /usr/local/bin)
  --service NAME       systemd 服务名 (默认: acmeDeliver)
  --version VER        指定版本号 (默认: latest)
  --component COMP     更新组件: all/server/client (默认: all)
  --skip-restart       跳过服务重启
  --force              强制更新（即使版本相同）
  --dry-run            演练模式，只显示将执行的操作
  -h, --help           显示帮助信息

示例:
  # 本地执行
  $(basename "$0")                           # 更新双端到最新版本
  $(basename "$0") --version v3.0.3          # 更新到指定版本
  $(basename "$0") --component server        # 仅更新服务端
  $(basename "$0") --install-dir /opt/acme   # 自定义安装目录

  # 远程执行（一行命令更新）
  curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash
  curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash -s -- --component client
  curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash -s -- --install-dir /opt/acme
EOF
}

# ===== 参数解析 =====
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --install-dir)
                INSTALL_DIR="$2"
                shift 2
                ;;
            --service)
                SERVICE_NAME="$2"
                shift 2
                ;;
            --version)
                VERSION="$2"
                shift 2
                ;;
            --component)
                COMPONENT="$2"
                if [[ ! "$COMPONENT" =~ ^(all|server|client)$ ]]; then
                    log_error "无效的组件: $COMPONENT (可选: all/server/client)"
                    exit 1
                fi
                shift 2
                ;;
            --skip-restart)
                SKIP_RESTART=true
                shift
                ;;
            --force)
                FORCE=true
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            *)
                log_error "未知选项: $1"
                show_help
                exit 1
                ;;
        esac
    done
}

# ===== 系统检测 =====
detect_system() {
    # 检测操作系统
    case "$(uname -s)" in
        Linux*)  OS="linux" ;;
        Darwin*) OS="darwin" ;;
        *)
            log_error "不支持的操作系统: $(uname -s)"
            exit 1
            ;;
    esac

    # 检测架构
    case "$(uname -m)" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *)
            log_error "不支持的架构: $(uname -m)"
            exit 1
            ;;
    esac

    log_info "检测到系统: ${OS}_${ARCH}"
}

# ===== 依赖检查 =====
check_dependencies() {
    local missing=()
    
    for cmd in curl tar sha256sum; do
        # macOS 使用 shasum 代替 sha256sum
        if [[ "$cmd" == "sha256sum" && "$OS" == "darwin" ]]; then
            if ! command -v shasum &> /dev/null; then
                missing+=("shasum")
            fi
        elif ! command -v "$cmd" &> /dev/null; then
            missing+=("$cmd")
        fi
    done

    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "缺少依赖: ${missing[*]}"
        exit 1
    fi
}

# ===== 获取版本信息 =====
get_latest_version() {
    log_info "获取最新版本信息..."
    
    local response
    response=$(curl -sL "${GITHUB_API}/latest")
    
    if [[ -z "$response" ]]; then
        log_error "无法获取版本信息，请检查网络连接"
        exit 1
    fi

    # 提取版本号
    LATEST_VERSION=$(echo "$response" | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)
    
    if [[ -z "$LATEST_VERSION" ]]; then
        log_error "无法解析版本号"
        exit 1
    fi

    log_info "最新版本: $LATEST_VERSION"
}

# ===== 下载文件 =====
download_file() {
    local url="$1"
    local output="$2"
    
    log_info "下载: $(basename "$output")"
    
    if $DRY_RUN; then
        log_info "[DRY-RUN] curl -sL '$url' -o '$output'"
        return 0
    fi
    
    if ! curl -sL "$url" -o "$output"; then
        log_error "下载失败: $url"
        return 1
    fi
}

# ===== 校验文件 =====
verify_checksum() {
    local file="$1"
    local checksums_file="$2"
    local filename
    filename=$(basename "$file")
    
    log_info "校验: $filename"
    
    if $DRY_RUN; then
        log_info "[DRY-RUN] 校验 $filename"
        return 0
    fi
    
    # 从 checksums.txt 获取预期的 hash
    local expected_hash
    expected_hash=$(grep "$filename" "$checksums_file" | awk '{print $1}')
    
    if [[ -z "$expected_hash" ]]; then
        log_error "未找到 $filename 的校验和"
        return 1
    fi
    
    # 计算实际 hash
    local actual_hash
    if [[ "$OS" == "darwin" ]]; then
        actual_hash=$(shasum -a 256 "$file" | awk '{print $1}')
    else
        actual_hash=$(sha256sum "$file" | awk '{print $1}')
    fi
    
    if [[ "$expected_hash" != "$actual_hash" ]]; then
        log_error "校验失败: $filename"
        log_error "  预期: $expected_hash"
        log_error "  实际: $actual_hash"
        return 1
    fi
    
    log_ok "校验通过: $filename"
}

# ===== 备份文件 =====
backup_file() {
    local file="$1"
    
    if [[ -f "$file" ]]; then
        local backup="${file}.bak.$(date +%Y%m%d_%H%M%S)"
        
        if $DRY_RUN; then
            log_info "[DRY-RUN] 备份 $file -> $backup"
            return 0
        fi
        
        cp "$file" "$backup"
        log_info "已备份: $backup"
    fi
}

# ===== 服务管理 =====
stop_service() {
    if $SKIP_RESTART; then
        return 0
    fi
    
    # 检查 systemd 是否可用
    if ! command -v systemctl &> /dev/null; then
        log_warn "systemctl 不可用，跳过服务管理"
        return 0
    fi
    
    # 检查服务是否存在
    if ! systemctl list-unit-files | grep -q "^${SERVICE_NAME}\.service"; then
        log_warn "服务 ${SERVICE_NAME} 不存在，跳过"
        return 0
    fi
    
    # 检查服务是否正在运行
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        log_info "停止服务: $SERVICE_NAME"
        
        if $DRY_RUN; then
            log_info "[DRY-RUN] systemctl stop $SERVICE_NAME"
            return 0
        fi
        
        if ! sudo systemctl stop "$SERVICE_NAME"; then
            log_warn "停止服务失败，继续更新..."
        else
            log_ok "服务已停止"
        fi
    fi
}

start_service() {
    if $SKIP_RESTART; then
        return 0
    fi
    
    if ! command -v systemctl &> /dev/null; then
        return 0
    fi
    
    if ! systemctl list-unit-files | grep -q "^${SERVICE_NAME}\.service"; then
        return 0
    fi
    
    log_info "启动服务: $SERVICE_NAME"
    
    if $DRY_RUN; then
        log_info "[DRY-RUN] systemctl start $SERVICE_NAME"
        return 0
    fi
    
    if ! sudo systemctl start "$SERVICE_NAME"; then
        log_error "启动服务失败"
        return 1
    fi
    
    log_ok "服务已启动"
}

# ===== 安装二进制 =====
install_binary() {
    local binary="$1"
    local dest="${INSTALL_DIR}/${binary}"
    
    log_info "安装: $binary -> $dest"
    
    if $DRY_RUN; then
        log_info "[DRY-RUN] 安装 $binary 到 $dest"
        return 0
    fi
    
    # 备份旧版本
    backup_file "$dest"
    
    # 安装新版本
    if ! sudo cp "${TMP_DIR}/${binary}" "$dest"; then
        log_error "安装失败: $binary"
        return 1
    fi
    
    # 设置权限
    sudo chmod 755 "$dest"
    
    log_ok "安装完成: $binary"
}

# ===== 执行更新 =====
do_update() {
    local version="$VERSION"
    
    # 获取版本号
    if [[ "$version" == "latest" ]]; then
        get_latest_version
        version="$LATEST_VERSION"
    fi
    
    # 确保版本号以 v 开头
    if [[ ! "$version" =~ ^v ]]; then
        version="v${version}"
    fi
    
    log_info "准备更新到版本: $version"
    
    # 构建下载 URL
    # 文件名格式: acmeDeliver_版本_系统_架构.tar.gz
    # 注意: 版本号不含 'v' 前缀
    local version_num="${version#v}"
    local archive_name="acmeDeliver_${version_num}_${OS}_${ARCH}.tar.gz"
    local checksums_name="checksums.txt"
    
    local archive_url="${GITHUB_DOWNLOAD}/${version}/${archive_name}"
    local checksums_url="${GITHUB_DOWNLOAD}/${version}/${checksums_name}"
    
    log_info "下载地址: $archive_url"
    
    # 下载文件
    download_file "$checksums_url" "${TMP_DIR}/${checksums_name}"
    download_file "$archive_url" "${TMP_DIR}/${archive_name}"
    
    # 校验
    verify_checksum "${TMP_DIR}/${archive_name}" "${TMP_DIR}/${checksums_name}"
    
    # 解压
    log_info "解压: $archive_name"
    if ! $DRY_RUN; then
        tar -xzf "${TMP_DIR}/${archive_name}" -C "$TMP_DIR"
    else
        log_info "[DRY-RUN] tar -xzf ${archive_name}"
    fi
    
    # 停止服务
    stop_service
    
    # 安装二进制
    case "$COMPONENT" in
        all)
            install_binary "acmedeliver-server"
            install_binary "acmedeliver-client"
            ;;
        server)
            install_binary "acmedeliver-server"
            ;;
        client)
            install_binary "acmedeliver-client"
            ;;
    esac
    
    # 启动服务
    start_service
    
    echo ""
    log_ok "更新完成！版本: $version"
}

# ===== 主函数 =====
main() {
    echo "========================================="
    echo "  acmeDeliver 一键更新脚本"
    echo "========================================="
    echo ""
    
    parse_args "$@"
    detect_system
    check_dependencies
    
    if $DRY_RUN; then
        log_warn "演练模式 - 不会执行实际操作"
        echo ""
    fi
    
    log_info "安装目录: $INSTALL_DIR"
    log_info "服务名称: $SERVICE_NAME"
    log_info "更新组件: $COMPONENT"
    log_info "目标版本: $VERSION"
    echo ""
    
    do_update
}

main "$@"
