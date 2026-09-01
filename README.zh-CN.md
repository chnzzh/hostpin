# Hostpin

简体中文 | [English](README.md)

Hostpin 是一个支持 PIN 自助加入的自托管服务器监控平台。安装 Agent 时即可输入
PIN 和节点资料并直接加入，无需先在管理后台创建节点。

项目包含轻量 Go 服务端、跨平台 Go Agent、内嵌 Vue 控制台、SQLite 与
PostgreSQL 存储、结构化网络探测、告警通知，以及兼容 Komari 公共主题的接口。

## 项目状态

Hostpin v0.1.0 是首个公开版本，采用面向生产的单实例架构。原生监控与注册协议从
本版开始进行版本管理；远程终端、任意命令执行、容器管理和日志采集不在项目范围内。

## 主要功能

- 使用 PIN 自助注册，每个节点拥有独立凭据，并支持幂等重装。
- 采集 CPU、负载、内存、Swap、磁盘与 IO、网络速率与流量、连接数、进程数、
  温度、运行时间、平台信息，以及可选的 NVIDIA/AMD GPU 指标。
- 每 3 秒通过 WebSocket 上报实时数据，断线时自动使用 HTTP 兜底；支持分层历史、
  ICMP/TCP/HTTP/DNS 探测、告警恢复事件和到期提醒。
- 支持只出站连接的延迟测量节点，可部署在公共地区、办公室、家庭、NAS 或私有
  路由器上；无需公网 IP、端口转发或入站防火墙规则。
- 任意普通监控节点均可在后台一键兼任延迟测量节点，无需重装 Agent；关闭时保留
  主机监控和已有延迟历史。
- 普通 Agent 内置电信、联通、移动三网延迟与丢包测量，节点详情显示当前值和历史
  曲线；测试目标、协议、采样次数和间隔均可在后台修改。
- 支持 SMTP、Telegram、Bark 和带签名的通用 Webhook，并提供持久化重试。
- 默认使用 SQLite WAL，适合约 100 个节点；可选 PostgreSQL 16+，适合约
  1,000 个节点，并支持经过校验的离线 SQLite → PostgreSQL 迁移。
- 后台一键导出/导入加密 SQLite 站点包，恢复前校验完整性，自动重载并保留回滚
  文件。
- 提供响应式 Vue 运维控制台、TOTP 与恢复码、会话/API Key 管理、私有站点、
  隐藏节点和有有效期的节点分享链接。
- 安全安装 Komari 主题 ZIP，并兼容其公共 REST、WebSocket 与 RPC2 主题接口。

## 快速开始

Hostpin 默认使用 SQLite，不需要额外安装或启动数据库。首次运行会自动创建
`./data/hostpin.db`。

```sh
git clone https://github.com/chnzzh/hostpin.git
cd hostpin
make build GO=/path/to/go1.26/bin/go
./bin/hostpin-server serve
```

打开 `http://localhost:8080/setup`，创建管理员账户和注册 PIN，然后执行面板显示的
Agent 安装命令：

后台“站点设置 → 节点注册”还可以生成一次性临时 PIN，默认 30 分钟有效，成功注册
一台节点后自动失效，适合临时授权而无需透露长期 PIN。

```sh
curl -fsSL http://localhost:8080/install.sh | sh
```

在路由器或地区服务器上安装仅测延迟的节点：

```sh
curl -fsSL http://localhost:8080/install.sh | sh -s -- --probe-node
```

在 Agent 所在设备上使用一行命令卸载；默认保留节点身份，方便以后恢复原节点：

```sh
curl -fsSL http://localhost:8080/uninstall.sh | sh
```

系统级安装将最后的 `sh` 改为 `sudo sh`。只有确定不再恢复该节点时才附加
`--purge` 删除本地身份配置。

互联网环境必须使用 HTTPS。二进制部署、Docker Compose、反向代理、备份、升级和
数据库迁移请参阅[中文部署文档](docs/zh-CN/deployment.md)。

## 中文文档

- [文档首页](docs/zh-CN/README.md)：阅读顺序与功能索引；
- [部署与迁移](docs/zh-CN/deployment.md)：SQLite 默认部署、Docker、代理、备份、
  升级及 SQLite → PostgreSQL；
- [一键备份与恢复](docs/zh-CN/backup-restore.md)：后台导出加密备份、导入恢复和
  更换服务端；
- [系统架构](docs/zh-CN/architecture.md)：模块边界、数据链路和安全边界；
- [流量统计](docs/zh-CN/traffic-accounting.md)：计数器、重置周期和配额模式；
- [三网延迟与测量节点](docs/zh-CN/latency-probes.md)：节点三网曲线，以及公网与 NAT/CGNAT 私网测量点；
- [API 与协议](docs/zh-CN/api.md)：认证、原生接口、Agent 帧和兼容边界；
- [主题兼容](docs/zh-CN/theme-compatibility.md)：Komari 主题兼容范围；
- [测试与发布](docs/zh-CN/testing.md)：本地验证、容量测试和发布门槛；
- [维护者发布指南](docs/zh-CN/releasing.md)：更新签名密钥与 GitHub Release 清单。

项目维护文件：[版本记录](CHANGELOG.md)、[参与贡献](CONTRIBUTING.md)与
[安全策略](SECURITY.md)。

## 验证

```sh
make test GO=/path/to/go1.26/bin/go
make lint GO=/path/to/go1.26/bin/go
make security GO=/path/to/go1.26/bin/go
python tests/e2e/browser_smoke.py
python tests/e2e/theme_compat.py
python tests/e2e/backup_restore.py
```

CI 会在 SQLite 和 PostgreSQL 16 上执行测试，对有状态模块进行竞态检查，并交叉
编译全部 13 个 Agent 目标。定时容量测试使用真实 WebSocket 流量验证 SQLite
100 节点和 PostgreSQL 1,000 节点场景。

## 设计原则

- 只做监控：没有 PTY、网页 Shell 或任意远程命令执行。
- PIN 仅用于首次注册；注册后每个节点使用各自的独立凭据。
- 公共接口绝不暴露 Agent Token、私有备注或未经脱敏的地址。
- 高频实时数据保存在内存，持久化历史按层级降采样。
- 第三方主题属于不受信任代码，安装前应进行审查。

## 许可证

MIT
