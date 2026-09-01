# API 与协议边界

[返回文档首页](README.md) · [English](../api.md)

Hostpin 原生契约统一位于 `/api/v1`。JSON 时间为 UTC RFC 3339，容量和计数器使用
字节，时长使用秒，百分比为 0 到 100 的数值，节点使用 UUID 标识。

## 认证方式

- 注册仅在 `POST /api/v1/enrollments` 使用一次共享 PIN；成功后节点使用独立的
  高熵 Bearer 凭据。
- 后台可生成默认 30 分钟有效、仅可成功注册一台新节点的临时 PIN。临时 PIN 与
  长期 PIN 并存，明文只在创建响应中返回，使用、到期或撤销后失效。
- 浏览器后台使用 HttpOnly 会话 Cookie；写操作还需要 CSRF Cookie 和
  `X-CSRF-Token` 请求头。
- 可撤销 API Key 使用 `Authorization: Bearer <token>`。v1 Key 具有 `admin`
  Scope，且不会改变公共接口的隐私规则。
- 分享链接只读、具有有效期，并携带明确的节点白名单。

Secret 只在创建时返回。数据库仅保存 Token 哈希，不保存原始 Agent 凭据、API
Key、会话 Token 或分享 Token。

## 原生接口

| 路径 | 用途 |
| --- | --- |
| `POST /api/v1/enrollments` | 使用 PIN 注册 `monitor` 或 `probe` 节点 |
| `GET /api/v1/agent/stream` | 已认证的 Agent WebSocket |
| `POST /api/v1/agent/reports` | 已认证的 HTTP 兜底上报 |
| `GET /api/v1/agent/config` | 当前结构化采集/探测配置 |
| `GET /api/v1/public/site` | 公共站点身份和隐私状态 |
| `GET /api/v1/public/nodes[/{id}]` | 当前可见监控节点状态 |
| `GET /api/v1/public/history` | 自动选择层级的主机历史 |
| `GET /api/v1/public/probes` | 已脱敏的服务探测历史；`purpose=carrier` 返回三网延迟 |
| `GET /api/v1/public/latency` | 公共 Probe Node RTT/丢包矩阵 |
| `GET /api/v1/public/latency/history` | 公共路径历史 |
| `GET /api/v1/public/live` | 公共浏览器 WebSocket |
| `GET|POST|DELETE /api/v1/admin/enrollment/temporary-pin` | 查看、生成或撤销一次性临时 PIN |
| `PUT /api/v1/admin/nodes/{id}/latency` | 开启或关闭普通节点的延迟测量能力 |
| `/api/v1/admin/*` | 节点、探测、告警、主题、存储和安全管理 |

后台备份接口位于 `/api/v1/admin/backups/status|export|import`。导出和导入均要求
管理员会话、CSRF 以及当前密码确认；导入只接受加密的 `.hostpin-backup`，不接受
普通 ZIP。

生成临时 PIN 时，`POST /api/v1/admin/enrollment/temporary-pin` 接收
`expires_in_minutes`（5–1440，默认 30）。创建响应中的 `pin` 只出现一次；后续
`GET` 仅返回 `active|used|expired|revoked` 状态和时间。节点创建与临时 PIN 消耗在
同一数据库事务中完成，因此失败不会消耗，使用同一 `install_id + token` 的网络重试
仍保持幂等。

节点流量校正接口为 `GET|PUT|DELETE /api/v1/admin/nodes/{id}/traffic-correction`。
`PUT` 接收以字节表示的
`rx_bytes` 与 `tx_bytes` 目标总量；响应同时返回原始值、显示值、有符号差值、当前
周期和是否可校正。写入与清除均进入审计日志。

节点延迟能力接口接收 `{"enabled": true|false}`。开启后节点仍保持 `monitor` 角色并
继续采集主机指标，同时由同一个 Agent 接收延迟矩阵任务；关闭后保留节点和已有延迟
历史。

错误响应包含稳定的 `error.code` 和可读的 `error.message`。未知、隐藏、过期和已撤销
资源不会泄露私有元数据。

## Agent 帧

WebSocket 只接受有类型的 `hello`、`sample` 和 `probe_result` 帧。服务端确认帧包含
接受状态、服务端时间、结构化采集配置，以及固定 ICMP/TCP/HTTP/DNS 任务。纯 Probe
Node 只会收到 ICMP/TCP 延迟任务；普通监控节点会收到服务探测和三网测量任务，并在
开启 `latency_enabled` 后额外接收延迟矩阵任务。

协议中没有 Shell、PTY、任意命令、脚本、日志跟踪、反向隧道或管理员指定二进制
URL 字段。

正常情况下，每 3 秒发送一次轻量样本，每 60 秒产生一个持久化样本。HTTP 兜底使用
相同的指标和探测对象。历史以服务端接收时间为准，同时保留 Agent 采集时间以便查看
时钟偏移。

## Komari 主题适配器

`/api`、`/api/rpc2` 和 `/api/clients` 下的兼容接口只服务公共前台主题，在 HTTP
边界将 Hostpin 原生模型转换为 Komari 结构。Hostpin Agent、告警引擎、Repository
和原生控制台都不依赖这些接口。

Komari Agent、插件、管理、命令和远程终端契约明确不受支持。固定兼容基线见
[主题兼容](theme-compatibility.md)。
