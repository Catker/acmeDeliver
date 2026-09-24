# acmeDeliver V3

![License](https://img.shields.io/badge/license-MIT-blue?style=flat-square)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/Catker/acmeDeliver?style=flat-square)
![GitHub release (latest by date including pre-releases)](https://img.shields.io/github/v/release/Catker/acmeDeliver?include_prereleases&style=flat-square)
![Build Status](https://img.shields.io/github/actions/workflow/status/Catker/acmeDeliver/release.yml?style=flat-square)
![Tests](https://img.shields.io/badge/tests-go%20test%20%2e%2e%2f-informational?style=flat-square)

acmeDeliver 是一个**轻量、安全**的 `acme.sh` 证书分发服务。V3 版本引入了 **WebSocket 实时推送架构**，支持服务端主动推送证书更新，客户端可以 Daemon 模式持久运行，实现证书的自动化分发和部署。

---

## 🚀 核心特性

### ✨ **V3 版本全新特性**

- **📡 WebSocket 推送模式**：服务端监控证书目录变化，实时推送给订阅的客户端
- **🔄 Daemon 守护进程**：客户端可作为后台服务持久运行，自动接收并部署证书
- **🎯 域名订阅机制**：客户端按需订阅域名，支持通配符匹配（`*.example.com`）和全局订阅（`*`）
- **⚡ 双模式支持**：同时支持传统 Pull（拉取）和新 Push（推送）模式
- **🔥 配置热重载**：`subscribe`、`sites` 支持运行时动态更新
- **🔄 重连自动同步**：客户端断线重连后自动同步缺失的证书，确保不会错过更新


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
# 发布包命名：acmeDeliver_<version>_<os>_<arch>.tar.gz（version 不带 v 前缀）
# 先查询 latest 版本号，再拼接下载地址
VERSION=$(curl -s https://api.github.com/repos/Catker/acmeDeliver/releases/latest | grep -o '"tag_name": *"v[^"]*"' | grep -o '[0-9][^"]*')

# Linux (amd64)
wget https://github.com/Catker/acmeDeliver/releases/download/v${VERSION}/acmeDeliver_${VERSION}_linux_amd64.tar.gz
tar -xzf acmeDeliver_${VERSION}_linux_amd64.tar.gz
chmod +x acmedeliver-server acmedeliver-client

# macOS (arm64)
wget https://github.com/Catker/acmeDeliver/releases/download/v${VERSION}/acmeDeliver_${VERSION}_darwin_arm64.tar.gz
tar -xzf acmeDeliver_${VERSION}_darwin_arm64.tar.gz
chmod +x acmedeliver-server acmedeliver-client
```

> 已安装后可用下方 `scripts/update.sh` 自动更新到 latest。

#### 一键更新（远程执行）

已安装的环境可通过一行命令更新到最新版本：

```bash
# 更新双端到最新版本
curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash

# 仅更新客户端
curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash -s -- --component client

# 仅更新服务端
curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash -s -- --component server

# 自定义安装目录
curl -sSL https://raw.githubusercontent.com/Catker/acmeDeliver/master/scripts/update.sh | bash -s -- --install-dir /opt/acmedeliver
```

更多选项请参考 `scripts/update.sh --help`。

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

3. **创建客户端配置文件** (`client-config.yaml`，根节点必须是 `client:`)
```yaml
client:
  server: "http://your-server:9090"
  password: "your-strong-password-here"
  workdir: "/var/lib/acme"  # 必须是绝对路径
  sites:
    - domain: "example.com"
      cert_path: "/etc/nginx/ssl/example.com/cert.pem"
      key_path: "/etc/nginx/ssl/example.com/key.pem"
      fullchain_path: "/etc/nginx/ssl/example.com/fullchain.pem"
      reloadcmd: "systemctl reload nginx"
```

4. **准备服务端证书目录与 time.log**

服务端按 `<base_dir>/<domain>/` 读取并下发以下文件：

```
<base_dir>/
└── example.com/
    ├── cert.pem
    ├── key.pem
    ├── fullchain.pem
    └── time.log      # 证书更新时间戳（Unix 秒），acme.sh 不会生成
```

用 acme.sh 的 `--install-cert` 把证书安装到该目录，并通过 `--reloadcmd` 写入 time.log：

```bash
acme.sh --install-cert -d example.com \
  --cert-file      /path/to/base_dir/example.com/cert.pem \
  --key-file       /path/to/base_dir/example.com/key.pem \
  --fullchain-file /path/to/base_dir/example.com/fullchain.pem \
  --reloadcmd      "date +%s > /path/to/base_dir/example.com/time.log"
```

- time.log 是时间戳比对的前提：缺少时 Daemon 重连/定时同步不会补推送，CLI `--deploy` 每次都会全量部署并 reload。
- 服务端 watcher 仅响应上述四个文件的变化，在 time.log 写入后（5 秒防抖）推送给订阅的 Daemon。
- 不要把 `base_dir` 直接指向 acme.sh 自身的工作目录（如 `~/.acme.sh`），其中的文件名与结构不符合上述约定。

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
./acmedeliver-client -s http://server:9090 -k your-password --status

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

# 强制部署（跳过与工作目录 time.log 的时间戳比较）
./acmedeliver-client -c client-config.yaml -d example.com --deploy -f

# crontab 示例
0 2 * * * /opt/acmedeliver/acmedeliver-client -c /etc/acmedeliver/client.yaml --deploy
```

**`--deploy` 工作流程：**
1. **并发控制** - 使用文件锁防止多个实例同时运行
2. **下载证书** - 下载 cert.pem、key.pem、fullchain.pem、time.log
3. **时间戳检查** - 工作目录 `<work_dir>/<domain>/time.log` 不旧于服务器时间戳时跳过保存、部署与重载（`-f` 跳过此检查强制部署）
4. **安全部署** - 同目录临时文件 + 原子替换写入目标位置；新建文件 key.pem 权限 0600、其余 0644，已存在的文件保留原权限与属主，软链接保留并写入其真实文件；部署成功后才更新工作目录 time.log
5. **执行重载** - 运行 `reloadcmd` 命令（批量去重），带 15 秒超时控制

**配置示例：**

```yaml
client:
  server: "http://your-server:9090"
  password: "your-password"
  workdir: "/var/lib/acme"  # 必须使用绝对路径

  # 无 -d 时，可按 domains 列表批量处理
  domains:
    - "example.com"
    - "api.example.org"

  # 站点部署配置（--deploy 与 Daemon 共用）
  sites:
    - domain: "example.com"
      cert_path: "/etc/nginx/ssl/example.com/cert.pem"
      key_path: "/etc/nginx/ssl/example.com/key.pem"
      fullchain_path: "/etc/nginx/ssl/example.com/fullchain.pem"
      reloadcmd: "systemctl reload nginx"
```

完整字段见仓库根目录 `client-config.yaml.example`。

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
5. **自动部署** - 按 `sites` 配置部署，成功后才写入工作目录 time.log，并防抖执行 `reloadcmd`（站点未配置时使用 `default_reload_cmd`）

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
    reload_debounce: 5       # Reload 防抖延迟（秒）
    sync_interval: 3600      # 定时同步间隔（秒），0/不设置=默认1小时
                             # 重连后会自动同步一次，此为额外的定时同步
                             # 设为 -1 可禁用定时同步（仍保留重连同步）
  
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

**配置热重载：** 修改 `subscribe`、`sites` 后无需重启，自动生效。

**连接保活：** 依赖 WebSocket 控制帧——服务端每 45 秒发送 ping，客户端自动回复 pong；客户端 3 分钟内未收到 ping 即判定连接失效并退避重连。

**证书同步机制：** Daemon 模式包含两重保障：
- **重连同步**：认证成功后立即同步，确保不错过离线期间的更新
- **定时轮询**：按 `sync_interval` 定期检查，作为安全网兆底

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
  -f               强制部署（跳过时间戳比较，仅 --deploy）
  --debug          调试模式（也可在配置中设置 debug: true）
  --dry-run        演练模式（不实际执行）
  --reload-cmd     覆盖默认的重载命令
```

---

## 🛡️ 服务端配置 (`acmedeliver-server`)

### 安全策略

```yaml
# IP 白名单 (可选，支持热重载)
ip_whitelist: "192.168.1.0/24,10.0.0.50,127.0.0.1"

# 时间戳容差由代码固定为 30 秒（签名校验），不是配置项

# TLS 加密
tls: true
tls_port: "9443"
cert_file: "/path/to/server.crt"
key_file: "/path/to/server.key"
```

### 配置文件示例

```yaml
# config.yaml - 服务端配置示例
port: "9090"
bind: "0.0.0.0"
base_dir: "/home/acme"
key: "your-very-strong-password-here"

# TLS 配置
tls: true
tls_port: "9443"
cert_file: "/etc/ssl/certs/acmedeliver.crt"
key_file: "/etc/ssl/private/acmedeliver.key"

# 安全配置（支持热重载）
ip_whitelist: "192.168.1.0/24,10.0.0.0/24"
trust_proxy: false  # 仅在可信反向代理后才开启；开启后取 X-Forwarded-For 最右一项
```

> **注意**: 服务端和客户端配置应分开存放。客户端配置示例参见 [Pull 模式](#pull-模式) 和 [Daemon 模式](#daemon-模式) 章节。

### 热重载支持

配置文件中的 `ip_whitelist`、`trust_proxy` 支持热重载，无需重启服务：

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
| `sync_request` | C→S | 证书同步请求（客户端发送本地时间戳，服务端推送差异证书） |
| `ping` / `pong` | C→S / S→C | 旧版客户端的应用层心跳，服务端仅为兼容保留回复；新版客户端依赖 WebSocket 控制帧 ping/pong 保活 |
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

> ⚠️ **生产环境必须走 TLS**：证书私钥随推送/下载在连接中传输，`ws://` 明文会暴露私钥，认证签名也可在 30 秒时间窗内被重放。
> 启用 `tls: true` 后明文端口 `port` 仍会监听，请将 `bind` 设为 `127.0.0.1` 或用防火墙屏蔽该端口，或改用下面的反向代理方案。

**反向代理（Nginx 负责 TLS）：**

服务端只监听本机明文端口，由 Nginx 终结 TLS 并转发 WebSocket：

```yaml
# 服务端 config.yaml
bind: "127.0.0.1"
port: "9090"
trust_proxy: true   # 白名单按 X-Forwarded-For 最右一项（Nginx 追加的真实来源 IP）判断
```

```nginx
server {
    listen 443 ssl;
    server_name deliver.example.com;
    ssl_certificate     /etc/nginx/ssl/deliver.example.com/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/deliver.example.com/key.pem;

    location /ws {
        proxy_pass http://127.0.0.1:9090;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 300s;   # 服务端每 45s ping，默认 60s 亦可，适当放宽更稳妥
    }
}
```

客户端配置 `server: "wss://deliver.example.com"`（自动补 `/ws`）；若挂在子路径，如 `https://example.com/acme`，客户端会连接 `/acme/ws`，Nginx 的 `location` 与 `proxy_pass` 需相应调整。

### 3. 文件安全

- **路径验证**: 严格的路径遍历防护，防止访问系统敏感目录
- **原子性操作**: 临时文件 + 重命名，避免文件损坏

### 4. 命令安全

- **通过 `sh -c` 执行**: `reloadcmd` 仅来自本地配置（不接受服务端下发），与 `acme.sh --reloadcmd` 语义一致，可使用 `&&`、`|` 等 shell 语法；请确保配置文件仅可信用户可写
- **超时控制**: 重载命令执行超时 15 秒

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

- **按时间戳跳过**: `--deploy` 比较工作目录 time.log 与服务端时间戳，未更新时跳过保存、部署与重载；Daemon 重连/定时同步只推送服务端较新的证书
- **推送收敛**: 只下发 cert.pem、key.pem、fullchain.pem、time.log；广播时消息只序列化一次
- **目录监控防抖**: 同一域名 5 秒内的多次文件变化合并为一次推送；Daemon 端 reload 命令防抖去重

---

## 📈 监控和日志

### 结构化日志

使用 `slog` 提供结构化日志，支持 JSON 格式：

```bash
# 普通部署（默认 JSON 日志）
./acmedeliver-client -c client-config.yaml -d example.com --deploy

# 调试模式（文本日志 + DEBUG 级别）
./acmedeliver-client -c client-config.yaml -d example.com --deploy --debug
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
cmd/
├── server/         # acmedeliver-server 入口（版本号构建时由 git tag 注入）
└── client/         # acmedeliver-client 入口
pkg/
├── cert/           # 证书读取与域名状态
├── client/         # WebSocket 客户端、Daemon、证书保存与站点部署
├── command/        # 重载命令执行（sh -c）
├── config/         # 服务端/客户端配置与热重载
├── security/       # 签名校验、IP 白名单
├── server/         # HTTP/WS 服务编排、健康检查（GET / → "Running"）与关闭
├── watcher/        # 证书目录监控（fsnotify）
└── websocket/      # Hub、消息协议与客户端会话
```

### 测试

```bash
# 运行所有测试
go test ./... -v

# 运行客户端保存/部署与 Daemon 测试
go test ./pkg/client -v

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

仓库已提供多阶段 `Dockerfile`（默认构建 server，产物路径 `/usr/local/bin/acmedeliver`）：

```bash
# 构建服务端镜像
docker build --build-arg APP=server -t acmedeliver-server .

# 构建客户端镜像
docker build --build-arg APP=client -t acmedeliver-client .

# 运行服务端（证书目录挂到 /data）
docker run --rm -p 9090:9090 \
  -e ACMEDELIVER_KEY='your-strong-password' \
  -v /home/acme:/data \
  acmedeliver-server
```

### 自动化部署脚本

```bash
# crontab：每天凌晨 2 点检查更新并部署
0 2 * * * /opt/acmedeliver/acmedeliver-client -c /etc/acmedeliver/client.yaml --deploy >> /var/log/acmedeliver.log 2>&1
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
