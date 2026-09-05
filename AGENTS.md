# acmeDeliver — Agent 规则

## 项目定位

轻量 `acme.sh` 证书分发服务（当前代码版本 **3.1.1**）。服务端监控证书目录并通过 **WebSocket** 推送；客户端支持 Pull（`--deploy` / `--status`）与 Daemon（`--daemon`）两种模式。

## 怎么跑

```bash
go mod tidy
make build                  # 产出 acmedeliver-server / acmedeliver-client
make test                   # go test ./...
./acmedeliver-server --gen-config > config.yaml
./acmedeliver-server -c config.yaml
./acmedeliver-client -c client-config.yaml --status
./acmedeliver-client -c client-config.yaml --daemon
```

默认端口 **9090**（TLS 默认 **9443**）。客户端配置根节点必须是 `client:`，权威示例见 `client-config.yaml.example`。

## 技术栈

- Go **1.21**，模块 `github.com/Catker/acmeDeliver`
- 依赖：gorilla/websocket、fsnotify、yaml.v3
- 发布：GoReleaser + `.github/workflows/release.yml`（tag `v*`）
- 产物名：`acmeDeliver_<version>_<os>_<arch>.tar.gz`（与 `scripts/update.sh` 一致）

## 目录与约定

| 路径 | 用途 |
|------|------|
| `cmd/server`、`cmd/client` | 双端入口，`VERSION` 常量当前为 `3.1.1` |
| `pkg/server`、`pkg/websocket`、`pkg/watcher` | 服务端编排 / WS / 证书目录监控 |
| `pkg/client`、`pkg/deployer` | 客户端连接、Daemon、部署 |
| `pkg/security` | 签名（默认时间戳容差 30s）与 IP 白名单 |
| `pkg/config` | 配置加载与热重载（服务端 `ip_whitelist`；客户端 `subscribe`/`sites`/`heartbeat_interval`） |
| `config.yaml.example`、`client-config.yaml.example` | 配置权威示例 |
| `Dockerfile` | 多阶段构建，`APP=server\|client`，产物 `/usr/local/bin/acmedeliver` |

- 协议以 WS 消息为准（`/ws`）；`GET /` 仅健康检查返回 `Running`。
- 密钥、密码不要硬编码；用配置文件或 `ACMEDELIVER_*` 环境变量。
- `.gitignore` 忽略本地二进制、`.gocache`、`.gomodcache`、`.codegraph`、`CLAUDE.md`、`integration_test.sh`。

## 当前状态 / 下一步

- **现役**：master @ `v3.1.1` 已发布（GitHub Releases）。
- 文档以本文件 + `README.md` + 两个 `*.example` 为入口；不要再引用不存在的 `pkg/orchestrator`、`pkg/updater` 或旧 HTTP API。
- GoReleaser 的 `-X main.version` 与源码里 `const VERSION` 不兼容（const 无法被 ldflags 覆盖）——发版版本号以 git tag / 源码常量为准，改版本时两边一起改。
- **未生效入口**（改动相关代码前先确认，勿假设其有效）：`IPMode`（`ip_mode`/env/`-4`/`-6`，`cmd/client/main.go` 写入后无读取）；配置文件 `debug` 字段（仅命令行 `-d` 生效）；`Force`（`-f`，服务端无时间戳比对，实际空操作）；心跳/重连间隔热重载（daemon ticker 启动时固定，不生效）。
