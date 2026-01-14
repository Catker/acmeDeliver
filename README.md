# acmeDeliver V3

![GitHub](https://img.shields.io/github/license/Catker/acmeDeliver?style=flat-square)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/Catker/acmeDeliver?style=flat-square)
![GitHub release (latest by date including pre-releases)](https://img.shields.io/github/v/release/Catker/acmeDeliver?include_prereleases&style=flat-square)
![Build Status](https://img.shields.io/github/actions/workflow/status/Catker/acmeDeliver/release?style=flat-square)
![Coverage](https://img.shields.io/badge/coverage-95%25-brightgreen?style=flat-square)

acmeDeliver 是一个**轻量、安全**的 `acme.sh` 证书分发服务。V3 版本引入了 **WebSocket 实时推送架构**，支持服务端主动推送证书更新，客户端可以 Daemon 模式持久运行，实现证书的自动化分发和部署。

---

## 🚀 核心特性

### ✨ **V3 版本全新特性**

- **📡 WebSocket 推送模式**：服务端监控证书目录变化，实时推送给订阅的客户端
- **🔄 Daemon 守护进程**：客户端可作为后台服务持久运行，自动接收并部署证书
- **🎯 域名订阅机制**：客户端按需订阅域名，支持通配符匹配（`*.example.com`）和全局订阅（`*`）
- **⚡ 双模式支持**：同时支持传统 Pull（拉取）和新 Push（推送）模式
- **🔥 配置热重载**：`subscribe`、`sites`、`heartbeat_interval` 支持运行时动态更新


---

## 📖 目录

- [快速开始](#快速开始)
  - [安装](#安装)
  - [配置](#配置)
  - [基础使用](#基础使用)
- [运行模式](#运行模式)
  - [Pull 模式（一次性拉取）](#pull-模式一次性拉取)
  - [Push 模式（Daemon 守护进程）](#push-模式daemon-守护进程)
- [客户端详解 (`acmedeliver-client`)](#客户端详解-acmedeliver-client)
  - [自动更新和部署](#自动更新和部署)
  - [部署类型](#部署类型)
  - [安全配置](#安全配置)
- [服务端配置 (`acmedeliver-server`)](#服务端配置-acmedeliver-server)
  - [安全策略](#安全策略)
  - [配置文件](#配置文件)
- [API 文档](#api-文档)
- [高级功能](#高级功能)
  - [安全最佳实践](#安全最佳实践)
  - [性能优化](#性能优化)
  - [监控和日志](#监控和日志)
- [开发指南](#开发指南)
  - [架构设计](#架构设计)
  - [测试](#测试)
  - [贡献](#贡献)

---

## 🚀 快速开始

### 安装

#### 从二进制文件安装 (推荐)

```bash
# Linux (amd64)
wget https://github.com/Catker/acmeDeliver/releases/latest/download/acmedeliver-server_Linux_x86_64.tar.gz
tar -xzf acmedeliver-server_Linux_x86_64.tar.gz
chmod +x acmedeliver-server acmedeliver-client

# macOS (arm64)
wget https://github.com/Catker/acmeDeliver/releases/latest/download/acmedeliver-server_darwin_arm64.tar.gz
tar -xzf acmedeliver-server_darwin_arm64.tar.gz
chmod +x acmedeliver-server acmedeliver-client
```

#### 从源码构建

```bash
git clone https://github.com/Catker/acmeDeliver.git
cd acmeDeliver
go mod tidy
make build
```

### 基础配置

1. **生成配置文件**
```bash
./acmedeliver-server --gen-config > config.yaml
```

2. **编辑服务端配置文件** (`config.yaml`)
```yaml
port: "9090"
base_dir: "/home/acme/"
key: "your-strong-password-here"
ip_whitelist: "192.168.1.0/24,10.0.0.0/24"
```

3. **创建客户端配置文件** (`client-config.yaml`)
```yaml
server: "http://your-server:9090"
password: "your-strong-password-here"
workdir: "/var/lib/acme"
sites:
  - domain: "example.com"
    cert_path: "/etc/nginx/ssl/example.com/cert.pem"
    key_path: "/etc/nginx/ssl/example.com/key.pem"
    fullchain_path: "/etc/nginx/ssl/example.com/fullchain.pem"
    reloadcmd: "systemctl reload nginx"
```

### 基础使用

#### 1. 启动服务端

```bash
# 使用配置文件启动
./acmedeliver-server -c config.yaml

# 或使用命令行参数
./acmedeliver-server -p 9090 -d /home/acme -k your-password
```

#### 2. 使用客户端

```bash
# 查询服务器状态（在线客户端 + 证书状态）
./acmedeliver-client -s http://server:9090 -k passwd --status

# 检查更新并部署单个域名
./acmedeliver-client -c config.yaml -d example.com --deploy

# 批量部署多个域名
./acmedeliver-client -c config.yaml -d "example.com,api.example.org" --deploy
```

---

## 运行模式

acmeDeliver V3 支持两种运行模式：

| 模式 | 触发方式 | 适用场景 | 配置节 |
|------|---------|---------|--------|
| **Pull** | 客户端主动请求 | cron 定时任务 | `sites` |
| **Daemon** | 服务端 WebSocket 推送 | 实时更新、多域名 | `subscribe` + `sites` |

详细用法参见 [客户端详解](#客户端详解-acmedeliver-client)。

---

## 🔧 客户端详解 (`acmedeliver-client`)

客户端支持两种运行模式：**Pull 模式**（一次性拉取）和 **Daemon 模式**（持久运行接收推送）。

### Pull 模式

主动向服务器请求证书，适合配合 cron 定时任务或手动执行。

**常用操作：**

```bash
# 查询服务器状态（在线客户端 + 证书状态）
./acmedeliver-client -c client-config.yaml --status

# 检查更新并部署单个域名
./acmedeliver-client -c client-config.yaml -d example.com --deploy

# 批量部署多个域名（逗号分隔）
./acmedeliver-client -c client-config.yaml -d "example.com,api.example.org" --deploy

# 强制更新（忽略时间戳缓存）
./acmedeliver-client -c client-config.yaml -d example.com --deploy -f

# crontab 示例
0 2 * * * /opt/acmedeliver/acmedeliver-client -c /etc/acmedeliver/client.yaml --deploy
```

**`--deploy` 工作流程：**
1. **时间戳检查** - 对比服务器 `time.log` 与本地缓存，判断是否需要更新
2. **并发控制** - 使用文件锁防止多个实例同时运行
3. **原子性下载** - 下载 cert.pem、key.pem、fullchain.pem
4. **安全部署** - 将证书复制到目标位置，设置权限（0644）
5. **执行重载** - 运行 `reloadcmd` 命令，带 15 秒超时控制

**配置示例：**

```yaml
client:
  server: "http://your-server:9090"
  password: "your-password"
  workdir: "/var/lib/acme"  # 必须使用绝对路径
  
  # 配置多域名（也可用 -n 命令行参数覆盖）
  domains:
    - "example.com"
    - "api.example.org"
  
  # 部署配置（--deploy 时使用）
  deployment:
    cert_path: "/etc/nginx/ssl/cert.pem"
    key_path: "/etc/nginx/ssl/key.pem"
    fullchain_path: "/etc/nginx/ssl/fullchain.pem"
    reloadcmd: "systemctl reload nginx"
```

---

### Daemon 模式

**V3 新增**。作为守护进程持久运行，通过 WebSocket 接收服务器推送的证书更新。

**基本用法：**

```bash
# 启动 daemon 模式
./acmedeliver-client -c client-config.yaml --daemon

# 或在配置文件中设置 daemon.enabled: true 后直接启动
./acmedeliver-client -c client-config.yaml
```

**工作流程：**
1. **建立连接** - 通过 WebSocket 连接服务器并认证
2. **发送订阅** - 告知服务器订阅的域名列表
3. **等待推送** - 服务器检测到证书变化时实时推送
4. **保存证书** - 保存到 workdir 对应域名目录
5. **自动部署** - 按 `sites` 配置部署并执行 `reloadcmd`

**配置示例：**

```yaml
client:
  server: "ws://your-server:9090"  # 使用 ws:// 或 wss://
  password: "your-password"
  workdir: "/var/lib/acme"
  
  # TLS 配置（自签证书场景）
  # tls_ca_file: "/path/to/ca.crt"            # 信任的 CA 证书路径
  # tls_insecure_skip_verify: false           # 跳过证书验证（仅开发用）
  
  daemon:
    enabled: true
    reconnect_interval: 30   # 断线重连间隔（秒）
    heartbeat_interval: 60   # 心跳间隔（秒）
    reload_debounce: 5       # Reload 防抖延迟（秒）
  
  # 订阅的域名（支持通配符和全局订阅）
  subscribe:
    - "example.com"
    - "*.example.org"   # 通配符匹配
    # - "*"             # 全局订阅：接收所有域名的证书更新
  
  # 站点部署配置（可选，不配置则只保存到 workdir）
  sites:
    - domain: "example.com"
      cert_path: "/etc/nginx/ssl/example.com/cert.pem"
      key_path: "/etc/nginx/ssl/example.com/key.pem"
      fullchain_path: "/etc/nginx/ssl/example.com/fullchain.pem"
      reloadcmd: "systemctl reload nginx"
```

**配置热重载：** 修改 `subscribe`、`sites`、`heartbeat_interval` 后无需重启，自动生效。

---

### 通用选项

```bash
Options:
  -c string        配置文件路径
  -d string        域名列表（逗号分隔，如 "d1.com,d2.com"）
  -s string        服务器地址
  -k string        认证密码
  --deploy         检查更新并部署证书
  --status         查询服务器运行状态（在线客户端 + 证书状态）
  --daemon         以守护进程模式运行
  -f               强制更新（忽略时间戳缓存）
  -4               仅使用 IPv4
  -6               仅使用 IPv6
  --debug          调试模式
  --dry-run        演练模式（不实际执行）
  --reload-cmd     覆盖默认的重载命令
```

---

## 🛡️ 服务端配置 (`acmedeliver-server`)

### 安全策略

```yaml
# IP 白名单 (可选)
ip_whitelist: "192.168.1.0/24,10.0.0.50,127.0.0.1"

# 时间戳验证范围
time_range: 60  # 时间戳误差（秒）

# TLS 加密
tls: true
tls_port: "9443"
cert_file: "/path/to/server.crt"
key_file: "/path/to/server.key"
```

### 配置文件示例

```yaml
# server-config.yaml - 服务端配置示例
port: "9090"
bind: "0.0.0.0"
base_dir: "/home/acme"
key: "your-very-strong-password-here"
time_range: 60

# TLS 配置
tls: true
tls_port: "9443"
cert_file: "/etc/ssl/certs/acmedeliver.crt"
key_file: "/etc/ssl/private/acmedeliver.key"

# 安全配置（支持热重载）
ip_whitelist: "192.168.1.0/24,10.0.0.0/24"
```

> **注意**: 服务端和客户端配置应分开存放。客户端配置示例参见 [Pull 模式](#pull-模式) 和 [Daemon 模式](#daemon-模式) 章节。

### 热重载支持

配置文件中的 `ip_whitelist` 支持热重载，无需重启服务：

```bash
# 修改配置文件后，会自动重载
vim config.yaml  # 修改 ip_whitelist
# 配置会自动生效，无需重启服务
```

### 环境变量配置

服务端支持通过环境变量覆盖配置：

```bash
export ACMEDELIVER_PORT="9090"
export ACMEDELIVER_KEY="your-strong-password-here"
export ACMEDELIVER_BASE_DIR="/home/acme"
export ACMEDELIVER_IP_WHITELIST="192.168.1.0/24,10.0.0.0/24"
export ACMEDELIVER_TLS="true"
export ACMEDELIVER_TLS_PORT="9443"
```

---

## 📋 API 文档

V3 版本统一采用 WebSocket 协议，所有 HTTP API 端点已移除。

### WebSocket 端点

#### WS /ws

WebSocket 连接端点，支持 CLI 一次性操作和 Daemon 持久模式。

**认证流程:**
1. 客户端连接 `ws://server:9090/ws`（或 `wss://` 用于 TLS）
2. 发送 `auth` 消息（包含签名和时间戳）
3. 服务器验证成功后返回 `auth_result`

**消息类型:**

| 类型 | 方向 | 说明 |
|------|------|------|
| `auth` | C→S | 客户端认证请求 |
| `auth_result` | S→C | 认证响应 |
| `status_request` | C→S | 请求服务器状态（在线客户端 + 证书状态） |
| `status_response` | S→C | 状态响应 |
| `cert_request` | C→S | 请求下载证书 |
| `cert_response` | S→C | 证书数据响应 |
| `cert_push` | S→C | 服务端主动推送证书（Daemon 模式） |
| `cert_ack` | C→S | 证书接收确认 |
| `ping` / `pong` | C↔S | 心跳保活 |
| `subscribe` | C→S | 更新订阅列表（Daemon 模式） |

---

## 🔒 安全最佳实践

### 1. 认证安全

```bash
# 使用强密码（至少 16 位，包含大小写字母、数字、特殊字符）
export ACMEDELIVER_PASSWORD="your-very-secure-password-here!"

# 避免在命令行中明文传递密码
./acmedeliver-client -c config.yaml  # 使用配置文件
```

### 2. 网络安全

**服务端 TLS 配置：**

```yaml
# 启用 TLS 加密
tls: true
tls_port: "9443"
cert_file: "/path/to/server.crt"
key_file: "/path/to/server.key"

# 限制访问 IP
ip_whitelist: "192.168.1.0/24,10.0.0.0/24"
```

**客户端 TLS 验证配置（自签证书场景）：**

当服务端使用自签证书时，客户端需要配置信任的 CA：

```yaml
client:
  server: "wss://your-server:9443"  # 使用 wss:// 协议
  
  # 方式1: 指定信任的 CA 证书（推荐）
  tls_ca_file: "/path/to/ca.crt"
  
  # 方式2: 跳过证书验证（仅开发/测试环境，生产环境禁用！）
  # tls_insecure_skip_verify: true
```

> ⚠️ **安全提示**: `tls_insecure_skip_verify: true` 会禁用所有证书验证，存在中间人攻击风险。生产环境必须使用 `tls_ca_file` 指定信任的 CA 证书。

### 3. 文件安全

- **路径验证**: 严格的路径遍历防护，防止访问系统敏感目录
- **原子性操作**: 临时文件 + 重命名，避免文件损坏

### 4. 命令安全

- **命令白名单**: 只允许安全的系统命令（systemctl, service, nginx, docker 等）
- **参数验证**: 严格的命令参数验证，防止注入攻击
- **超时控制**: 所有外部命令执行都有超时限制（15-30秒）

### 5. 运行安全

```bash
# 建议以非 root 用户运行服务
useradd -r -s /bin/false acmedeliver
sudo -u acmedeliver ./acmedeliver-server -c config.yaml

# 使用 systemd 管理
sudo systemctl enable acmedeliver
sudo systemctl start acmedeliver
```

---

## 📊 性能优化

### 1. 客户端优化

- **并发下载**: 支持同时下载多个证书文件
- **连接复用**: HTTP Client 连接池复用
- **智能缓存**: 时间戳缓存，避免不必要的网络请求

### 2. 服务端优化

- **内存优化**: 流式处理大文件
- **并发控制**: 内置速率限制，防止资源耗尽
- **缓存策略**: 时间戳缓存，减少重复计算

### 3. 网络优化

```yaml
# IPv4/IPv6 优化
client:
  ip_mode: 4  # 4=IPv4, 6=IPv6, 0=自动

# 连接超时设置
timeout: 30s
```

---

## 📈 监控和日志

### 结构化日志

使用 `slog` 提供结构化日志，支持 JSON 格式：

```bash
# 生产模式 (JSON 日志)
./acmedeliver-client -d example.com -deploy nginx

# 调试模式 (文本日志)
./acmedeliver-client -d example.com -debug -deploy nginx
```

### 日志级别

- **INFO**: 正常操作信息
- **WARN**: 可恢复的警告
- **ERROR**: 错误信息
- **DEBUG**: 详细调试信息

### 监控指标

```bash
# 查询服务器状态（在线客户端 + 证书状态）
./acmedeliver-client -c config.yaml --status
```

---

## 🏗️ 开发指南

### 架构设计

```
pkg/
├── client/         # 客户端 Daemon 模式实现
├── command/        # 命令执行和安全解析
├── config/         # 配置管理和热重载
├── deployer/       # 证书部署（配置驱动）
├── handler/        # HTTP 请求处理
├── orchestrator/   # 客户端业务编排
├── security/       # 安全模块 (签名、限流、白名单)
├── updater/        # 更新逻辑和时间戳管理
├── watcher/        # 证书目录监控（fsnotify）
├── websocket/      # WebSocket Hub 和消息处理
└── workspace/      # 工作目录和文件操作
```

### 测试

```bash
# 运行所有测试
go test ./... -v

# 运行安全测试
go test ./pkg/deployer -v

# 运行工作空间测试
go test ./pkg/workspace -v

# 测试覆盖率
go test ./... -cover
```

### 代码质量

```bash
# 代码格式化
go fmt ./...

# 静态分析
golangci-lint run

# 安全检查
go sec ./...
```

### 贡献指南

1. Fork 项目
2. 创建功能分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

---

## 🔗 部署示例

### Systemd 服务配置

```ini
[Unit]
Description=acmeDeliver Certificate Service
After=network.target

[Service]
Type=simple
User=acmedeliver
Group=acmedeliver
WorkingDirectory=/opt/acmedeliver
ExecStart=/opt/acmedeliver/acmedeliver-server -c /etc/acmedeliver/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Docker 部署

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o acmedeliver-server ./cmd/server
RUN go build -o acmedeliver-client ./cmd/client

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/acmedeliver-* .
EXPOSE 9090
CMD ["./acmedeliver-server"]
```

### 自动化部署脚本

```bash
#!/bin/bash
# deploy-certificates.sh

DOMAIN="example.com"
CLIENT="/opt/acmedeliver/acmedeliver-client"
CONFIG="/etc/acmedeliver/client.yaml"

# 每天凌晨 2 点检查更新
0 2 * * * $CLIENT -c $CONFIG -deploy nginx >> /var/log/acmedeliver.log 2>&1
```

---

## 🤝 贡献

欢迎贡献代码！

### 主要贡献者

- [@julydate](https://github.com/julydate) - 项目创建者和维护者
- [@thank243](https://github.com/thank243) - 核心贡献者

---

## 📄 许可证

本项目采用 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

---

## 🙏 致谢

- [acme.sh](https://github.com/acmesh-official/acme.sh) - 强大的 ACME 客户端
- [Go 社区](https://golang.org/) - 提供优秀的编程语言和生态系统

---

## 🔗 相关链接

- [GitHub 仓库](https://github.com/Catker/acmeDeliver)
- [问题反馈](https://github.com/Catker/acmeDeliver/issues)
- [发布版本](https://github.com/Catker/acmeDeliver/releases)

---

<p align="center">
  Made with ❤️ for secure certificate distribution
</p>