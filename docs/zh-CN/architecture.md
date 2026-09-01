# Hostpin 系统架构

[返回文档首页](README.md) · [English](../architecture.md)

Hostpin 是一个单站点监控系统，由两个 Go 二进制组成，并且不存在远程执行通道。

## 可维护性边界

`cmd/hostpin-server` 是服务端的组合根。领域包不导入 HTTP 层，并且只接收自身所需
的最小 Repository 能力。存储契约拆分为生命周期、身份、设置、节点、指标、探测、
告警、主题和审计接口；SQL 实现再将这些接口组合给服务端使用。这样可分别测试
告警、通知、主题和维护任务，也避免兼容适配器演变为第二套核心架构。

主要包边界如下：

- `internal/collector`、`agent`、`probe`、`enrollment` 和 `service`：低占用的
  端点运行时；
- `internal/core`：实时状态、流量增量、广播和有界持久化；
- `internal/store` 与 `store/sqlstore`：版本化存储契约及 SQLite/PostgreSQL
  实现；
- `internal/alerting` 与 `notification`：规则状态机和持久化通知投递；
- `internal/httpapi`：原生传输层以及隔离的 `komari*` 适配器；
- `internal/theme`：不受信任主题压缩包的验证和资源管理；
- `internal/backup`：加密站点包、一致性快照、恢复校验与回滚；
- `web/src`：原生 Vue 运维控制台。

新增数据库应实现 Repository 能力，而不是把 SQL 泄漏到 Handler。新增通知渠道应
进入 Dispatcher。新增固定探测类型必须具有明确的模型、校验器、Agent 实现和测试；
系统故意不提供通用命令逃生口。

## 数据链路

1. `hostpin-agent install` 在本地生成安装 UUID 和随机独立凭据。共享 PIN 只发送到
   `POST /api/v1/enrollments`。
2. Agent 每 3 秒通过 WebSocket 发送紧凑样本；服务端每 60 秒将一个样本放入有界
   持久化队列。
3. 当前状态保存在内存并广播给浏览器。SQLite 使用 WAL 和短批量写入；PostgreSQL
   把指标写入按月分区。
4. 60 秒原始数据先聚合到 5 分钟和 1 小时表，再由保留任务清理过期数据。

持久化队列有固定上限。存储不可用时，实时状态和浏览器更新仍在内存中继续，写入
则重试。队列达到上限后会丢弃最旧历史项，`/healthz` 报告 degraded 状态和准确的
丢弃数量。计数器与重置规则见[流量统计](traffic-accounting.md)。

## 延迟测量节点

节点注册时拥有不可变角色：`monitor` 或 `probe`。监控节点采集主机指标，也可执行
服务探测；Probe Node 跳过主机指标采集，只运行服务端定义的 ICMP/TCP 延迟目标。
使用已有安装身份切换角色会被拒绝。

Probe Node 始终主动连接 Hostpin，服务端永远不会反向拨号。因此 NAT 或 CGNAT
后的路由器不需要公网地址、端口转发、防火墙例外或动态 DNS。相同的认证出站流会
承载心跳、结构化目标配置以及包含平均 RTT 和丢包率的多样本结果。

延迟目标关联到被监控服务器，但地址不必等于该服务器检测到的 IP。v1 会把同一个
目标地址下发给所有 Probe Node：如果站点只有能访问同一内网的私有测量点，可以
使用 LAN 地址；如果混合公网和家庭测量点，则必须填写所有测量点都能访问的地址，
否则无法访问它的测量点会正常记录失败。每个 Probe Node 独立覆盖目标地址不属于
v1。目标地址和原始网络错误仅管理员可见。公共 API 只公开启用且公开的目标、未隐藏
的 Probe Node、RTT/丢包结果和展示元数据。

Agent 协议只包含样本、心跳、固定探测任务、探测结果和采集配置，没有 Shell、
PTY、命令、容器或日志消息类型。

## 兼容层隔离

Komari 支持属于展示适配器，不是上游 Fork。原生模型、Agent 协议、存储、告警和
管理功能不依赖 Komari。`internal/httpapi/komari.go`、`komari_rpc.go` 和
`komari_metrics.go` 只翻译面向公共主题的 REST/WebSocket/RPC2 契约。不支持的
Agent、插件、管理和终端调用不会进入 Hostpin 核心。

即使启用外部主题，内置 Vue 界面仍是恢复与管理入口。主题资源只从已验证的活动
主题 `dist/` 目录提供；登录、首次设置和后台管理始终使用内嵌原生应用。

## 信任边界

- 注册 PIN 哈希、管理员密码、Agent Secret、API Key 和分享 Token 均不以明文
  保存。
- 通知和 TOTP 凭据使用 `data/master.key` 加密；未通过环境变量提供时，系统会以
  `0600` 权限生成该文件。
- 只有直接连接来源属于 `security.trusted_proxies` 时，才信任代理地址头。
- 主题 ZIP 属于不受信任输入；解压有容量边界，并拒绝目录穿越、链接、重复路径、
  异常压缩率和无效清单。
- Agent 自动更新默认关闭，且只接受由编译进正式版二进制的 Ed25519 公钥验证通过
  的清单。
- 一键备份使用独立口令加密，导入时验证每个认证分块、ZIP 安全、SHA-256、数据库
  完整性和主密钥匹配，恢复前的数据保留为回滚副本。

## 容量边界

SQLite 是默认数据库，目标规模约 100 个节点；PostgreSQL 16+ 面向约 1,000 个
节点。Hostpin v1 明确只运行一个服务端实例，不承诺多副本一致性。

原生前端按路由延迟加载页面和 ECharts，首页无需承担图表包的加载成本。设计 Token
支持深色、浅色和跟随系统模式，图表可在不刷新页面的情况下响应主题变化。工业化
遥测布局强调紧凑比较、明确单位、键盘焦点、语义状态和 WCAG AA 文本对比度。
