# 部署与数据库迁移

[返回文档首页](README.md) · [English](../deployment.md)

Hostpin 默认使用 SQLite。无论直接运行二进制、使用示例配置还是使用默认推荐的
Docker Compose，均不需要 PostgreSQL；首次启动会自动执行数据库迁移并创建数据
文件。

## 获取程序

本文中的 `deploy/...` 和 Docker 命令均假定当前目录是 Hostpin 源码仓库根目录。
可以从正式版发布附件取得与系统/架构匹配的 `hostpin-server`，也可以在源码目录使用
Go 1.26.x 和锁定的 pnpm 依赖自行构建：

```sh
make build GO=/path/to/go1.26/bin/go
```

构建结果位于 `bin/hostpin-server` 和 `bin/hostpin-agent`。下文以源码构建产物为
例；使用发布附件时，将 `./bin/hostpin-server` 替换为实际下载路径。

## 单二进制 + SQLite

无配置启动时，Hostpin 监听 `:8080`，数据目录为 `./data`，数据库为
`./data/hostpin.db`：

```sh
./bin/hostpin-server serve
```

生产环境建议创建专用用户和目录，将 `deploy/hostpin.example.yaml` 复制到
`/etc/hostpin/hostpin.yaml`，并安装 `deploy/hostpin-server.service`。服务账户只需
拥有 `/var/lib/hostpin` 的写权限。

```sh
sudo install -m 0755 ./bin/hostpin-server /usr/local/bin/hostpin-server
sudo install -m 0644 deploy/hostpin-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now hostpin-server
```

示例 YAML 已明确配置 SQLite：

```yaml
data_dir: /var/lib/hostpin
database:
  driver: sqlite
  dsn: /var/lib/hostpin/hostpin.db
```

请将 Hostpin 放在 HTTPS 反向代理后，并把 `public_url` 设置为外部访问地址。仅将
真实代理所在网段加入 `security.trusted_proxies`；其他来源发来的
`X-Forwarded-For` 和 `X-Real-IP` 会被忽略。

## Docker Compose

推荐的 SQLite 部署：

```sh
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f deploy/docker-compose.sqlite.yml up -d --build
```

数据保存在 `hostpin-data` 卷中。除非明确选择 PostgreSQL，不要设置
`HOSTPIN_DB_DRIVER` 或 `HOSTPIN_DB_DSN` 为其他值。

可选的 PostgreSQL 16 部署：

```sh
POSTGRES_PASSWORD='replace-this' \
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f deploy/docker-compose.postgres.yml up -d --build
```

PostgreSQL 模式面向更大规模的单实例站点，不会启用多副本、高可用或 Redis。

## 首次设置

启动服务后打开 `https://monitor.example.com/setup`，依次创建：

1. 唯一管理员账户；
2. 至少 6 位数字的注册 PIN；
3. 站点名称和公开/私有策略。

6 位 PIN 可以使用，但后台会持续提示其强度较弱。PIN 只在注册时使用，更换 PIN
不会影响已加入节点的独立凭据。

## 安装监控 Agent

安装器默认从 Hostpin 官方 GitHub Release 下载 Agent。完全自托管时可设置
`agent_release_base`（或 `HOSTPIN_AGENT_RELEASE_BASE`），指向一个 HTTPS 目录；
目录中需提供各平台的 `hostpin-agent-<os>-<arch>` 文件及对应 `.sha256` 文件。

Linux、Alpine、OpenWrt、NAS、macOS 和 FreeBSD：

```sh
curl -fsSL https://monitor.example.com/install.sh | sh
```

安装脚本识别平台、下载对应二进制并校验 SHA-256。之后由 Agent 通过 `/dev/tty`
询问 PIN、节点名称、分组、地区和标签。使用 `--advanced` 可设置计费、流量、网卡、
挂载点、GPU、可见性和自动更新等选项；其中每月流量额度直接以 GiB 输入，`0` 表示
不限量。

交互问答没有网络超时限制。只有全部问题回答完成、Agent 即将发送注册请求时，才会
启动 65 秒网络时限。首次请求遇到临时网络错误会复用同一份 `install_id` 和节点令牌
自动重试；待注册身份以 `0600` 权限暂存且不包含 PIN，因此即使重新执行安装命令也
不会因为响应丢失而重复创建节点。

Windows 用户应先下载并检查 `/install.ps1`，再在管理员 PowerShell 中执行。PIN
不能作为普通命令行参数。自动化环境只能使用受限的 `HOSTPIN_PIN` 环境变量，或
权限为 `0600` 的 PIN 文件。

## 安装延迟测量节点

在 Linux、OpenWrt、NAS、macOS 或 FreeBSD 上安装只出站的 Probe Node：

```sh
curl -fsSL https://monitor.example.com/install.sh | sh -s -- --probe-node
```

Windows：

```powershell
Invoke-WebRequest -UseBasicParsing 'https://monitor.example.com/install.ps1' -OutFile .\hostpin-install.ps1
.\hostpin-install.ps1 -ProbeNode
```

Probe Node 主动建立 WSS/HTTPS 连接，不监听端口，因此无需公网 IP、动态 DNS、
端口转发或入站防火墙规则。进入后台的“延迟节点”，添加 ICMP 或 TCP 目标并设置
测量点和目标是否公开。完整说明见[延迟测量节点](latency-probes.md)。

已有普通监控 Agent 也可以兼任：在“管理后台 → 节点”中编辑并开启“同时作为延迟
测量节点”。它会继续采集完整主机指标，无需重装。

## 一行卸载 Agent

在 Agent 所在设备上，以安装时相同的用户执行：

```sh
curl -fsSL https://monitor.example.com/uninstall.sh | sh
```

如果最初安装为系统服务，则使用：

```sh
curl -fsSL https://monitor.example.com/uninstall.sh | sudo sh
```

默认卸载服务和二进制，但保留 `agent.json` 中的节点身份；以后重新安装时会继续使用
原节点。彻底清理本地身份需要显式确认：

```sh
curl -fsSL https://monitor.example.com/uninstall.sh | sudo sh -s -- --purge
```

可先用 `--dry-run` 查看将执行的操作而不修改系统。Windows 管理员 PowerShell：

```powershell
irm https://monitor.example.com/uninstall.ps1 | iex
```

卸载不会删除面板中的节点和历史数据；如不再保留，请在 Agent 停止后到“节点”页面
手动删除。

## HTTP 与 HTTPS 限制

公网地址不允许使用明文 HTTP 注册。Loopback 和私网地址仍需交互确认，或显式传入
`--allow-http`。服务端同样默认拒绝公网地址的 `http://` `public_url`。

`HOSTPIN_ALLOW_INSECURE_HTTP=true` 仅用于明确接受风险的旧环境，不能用于
互联网部署。

## 从 SQLite 迁移到 PostgreSQL

这是离线、单向迁移。迁移前必须停止 Hostpin，避免源库在复制期间继续变化；目标
必须是空的 PostgreSQL 16+ 数据库。

1. 停止 Hostpin 服务。
2. 备份 SQLite 数据库、`-wal`/`-shm` 文件、`master.key` 和 `themes/`。
3. 创建空 PostgreSQL 数据库和权限受限的迁移账户。
4. 执行：

```sh
hostpin-server migrate sqlite-to-postgres \
  --source /var/lib/hostpin/hostpin.db \
  --target 'postgres://hostpin:password@db:5432/hostpin?sslmode=require'
```

迁移命令会：

- 在目标库执行版本化迁移并创建所需的指标分区；
- 复制所有业务表；
- 校验每张表的记录数以及指标/事件的时间范围；
- 保留源 SQLite 数据库，不执行删除。

迁移成功后，把服务配置改为：

```yaml
database:
  driver: postgres
  dsn: postgres://hostpin:password@db:5432/hostpin?sslmode=require
```

启动服务并检查节点、历史、流量、探测、告警和主题后，再对 PostgreSQL 做首次
备份。建议保留 SQLite 源库及其 WAL 文件，直到 PostgreSQL 部署完成验证。

注意：当前不支持在线迁移，也不提供 PostgreSQL → SQLite 的反向迁移。命令行中的
DSN 可能出现在本机进程列表中，建议使用临时、权限受限的迁移凭据，并在完成后
轮换密码。

## 备份与恢复

SQLite 站点可以直接使用后台的“Backup & restore”导出加密完整包，并在新服务端
导入后自动重载。操作、验证、回滚和服务端切换步骤见
[界面一键备份与恢复](backup-restore.md)。以下手工流程仍适用于离线维护。

SQLite：停止 Hostpin，或使用 SQLite 在线备份工具，然后一起保存：

- `hostpin.db`（在线复制时还需正确处理 `hostpin.db-wal` 和 `hostpin.db-shm`）；
- `master.key`；
- `themes/` 目录。

PostgreSQL：使用 `pg_dump --format=custom`，同时单独备份 `master.key` 和主题目录。
如果丢失 `master.key`，已有 TOTP 和通知渠道凭据将无法解密，但不会因此泄露。

每次升级前都应保存数据库、整个数据目录和旧版二进制。Agent 自动更新会在可执行
文件旁保留 `.rollback` 文件。
