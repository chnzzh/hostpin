# 测试与发布验证

[返回文档首页](README.md) · [English](../testing.md)

Hostpin 使用分层验证，使协议、存储、浏览器行为、主题兼容、资源占用和目标平台构建
能够独立暴露故障。

## 本地快速验证

使用 Go 1.26.x 和锁定的 pnpm 依赖：

```sh
make test GO=/path/to/go1.26/bin/go
make lint GO=/path/to/go1.26/bin/go
make security GO=/path/to/go1.26/bin/go
make build GO=/path/to/go1.26/bin/go
```

`make test` 会运行全部 Go/SQLite 测试和前端单元测试；`make security` 会扫描可达的
Go 漏洞，并使用 npm 官方安全公告接口审计锁定的前端依赖。如需在 PostgreSQL 16+ 上
执行相同的存储集成测试，可提供测试 DSN：

```sh
HOSTPIN_TEST_POSTGRES='postgres://hostpin:password@127.0.0.1:5432/hostpin_test?sslmode=disable' \
  /path/to/go1.26/bin/go test ./internal/store/sqlstore
```

有状态模块的竞态检查：

```sh
/path/to/go1.26/bin/go test -race \
  ./internal/core ./internal/httpapi ./internal/security \
  ./internal/alerting ./internal/notification ./internal/store/sqlstore \
  ./internal/backup
```

## 浏览器与主题

构建两个二进制。浏览器冒烟与主题兼容共用 `127.0.0.1:18082` 的全新服务端；备份
恢复和三网延迟分别使用 `:18084` 与 `:18085` 的独立全新服务端：

```sh
python3 -m pip install -r tests/e2e/requirements.txt
python3 -m playwright install chromium
python3 tests/e2e/browser_smoke.py
python3 tests/e2e/carrier_latency.py
python3 tests/e2e/theme_compat.py
python3 tests/e2e/backup_restore.py
```

浏览器冒烟测试覆盖首次设置、监控和 Probe Node 注册、认证上报、流量统计、服务与
延迟探测、动态 Agent 配置、离线检测、移动端布局、告警 CRUD、CSRF 拒绝、TOTP
和一次性恢复码登录、会话/API Key 撤销、私有站点以及有有效期的节点分享。

主题测试使用 `.github/workflows/ci.yml` 中固定的三个官方 ZIP 及 SHA-256。它会上传
并激活 Komari Web、Carbon 和 Pulse，并检查 Managed 设置、公共/私有页面、节点、
历史、Ping、REST、批量 RPC2、RPC2 WebSocket 和 `/api/clients` 实时更新。

## 容量与 Agent 资源

`.github/workflows/capacity.yml` 会使用真实、已认证 WebSocket 连续上报 65 秒，覆盖
100 个 SQLite Agent 和 1,000 个 PostgreSQL Agent。以下任一情况会导致失败：

- 实时可见延迟超过 5 秒；
- 最新状态 API p95 超过 300 ms；
- 历史点缺失或流量未累积；
- 持久化进入 degraded；
- 队列丢弃任何记录。

`tests/e2e/linux_smoke.sh` 会提供正式版安装资源，下载面板真实 `install.sh`，验证
Monitor 与 Probe 两种角色的 SHA-256 和参数转发，并检查 WebSocket、API 延迟和 v1
资源预算。HTTP Fallback 与动态配置由 `internal/agent/runtime_test.go` 确定性覆盖。
Probe-only 模式单独测量，因为它不会初始化主机采集器。

## 功能与测试对应表

| 边界 | 权威覆盖 |
| --- | --- |
| PIN 限流、Argon2id、全局熔断、可信代理 | `internal/security`、HTTP 校验测试、浏览器 CSRF 测试 |
| 注册身份和凭据幂等 | SQLite/PostgreSQL 集成、`internal/enrollment`、浏览器安装流程 |
| Unix 安装器下载、校验和与参数转发 | 安装器单测、真实 `install.sh` Linux 冒烟测试 |
| 服务端一键安装、URL 问答、默认监听、校验、SQLite 配置、不安全 HTTP 拒绝与升级保留 | CI 和 `make release-check` 中的 `tests/e2e/server_installer.sh` |
| 一行卸载与身份保留边界 | `uninstall.sh` 语法、危险模式拒绝及 `--dry-run` Linux 冒烟测试 |
| CPU/内存/磁盘/网络/GPU 采集 | `internal/collector`、平台 CI、Linux 冒烟测试 |
| WebSocket 实时与 HTTP 兜底 | `internal/agent`、浏览器/Linux 冒烟、容量负载 |
| 流量与重置语义 | `internal/core/traffic_test.go`、双数据库集成、浏览器、容量负载 |
| 历史、聚合、保留与队列降级 | SQL 集成、`internal/core/persister_test.go`、容量负载 |
| ICMP/TCP/HTTP/DNS 探测 | `internal/probe`、浏览器服务探测 |
| 三网延迟、丢包和节点详情 | HTTP 隐私测试、`carrier_latency.py` 桌面/移动端流程 |
| NAT 安全的延迟节点 | SQLite/PostgreSQL 集成、浏览器矩阵与历史 |
| 普通监控节点兼任延迟节点 | SQL 调度/迁移、HTTP 能力开关、浏览器真实结果流程 |
| 告警持续/恢复/冷却/离线 | 告警引擎测试、告警 SQL 集成、浏览器 CRUD |
| SMTP/Telegram/Bark/Webhook 与重试 | `internal/notification`、告警 SQL 集成 |
| 会话、TOTP、恢复码、API Key、分享 | 安全/存储测试、浏览器访问控制流程 |
| 隐藏/私有数据脱敏 | HTTP 隐私测试、浏览器私有/分享流程 |
| 主题 ZIP 安全与兼容 | `internal/theme`、HTTP 隐私测试、官方主题浏览器套件 |
| SQLite → PostgreSQL | 双数据库集成和迁移校验 |
| 加密备份、导入、回滚和热重载 | `internal/backup`、HTTP 接口测试、`backup_restore.py` |
| Agent 支持平台 | CI 的 `cross-agent` 与 `agent-platform` Job |

## 稳定版门槛

稳定版必须通过单元/集成测试、竞态检查、`go vet`、依赖漏洞扫描、前端 TypeScript
检查/构建/测试、浏览器与官方主题套件、全部 13 个 Agent 交叉构建、Linux Agent
资源检查、服务端安装器模拟测试和定时容量测试。服务端日志还会扫描 panic、fatal
和 error 级故障。

Tag 触发流程和首次 Ed25519 更新签名配置见[维护者发布指南](releasing.md)。
