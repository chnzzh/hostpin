# 界面一键备份与恢复

[返回文档首页](README.md) · [English](../backup-restore.md)

Hostpin 的“管理后台 → Backup & restore”提供 SQLite 站点的一键导出、导入和自动
重载。它用于更换服务端、灾难恢复或在升级前创建可验证快照。

## 备份包含什么

加密的 `.hostpin-backup` 文件包含：

- 使用 SQLite Online Backup API 生成的一致性 `hostpin.db` 快照；
- 当前服务端实际使用的 `master.key`；
- `themes/` 中的主题资源；
- 节点身份与 Token 哈希、历史、流量、探测、告警、通知配置、管理员、API Key、
  分享链接和审计记录等全部数据库持久化状态。

备份不包含与新机器环境相关的 `hostpin.yaml`、环境变量、服务端二进制、反向代理、
域名、TLS 证书或 systemd/Docker 配置。这些内容应单独保存并在新服务端配置。

## 导出

1. 打开“Backup & restore”。
2. 输入当前管理员密码。
3. 设置至少 12 个字符的备份口令，并再次确认。
4. 点击“EXPORT ENCRYPTED BACKUP”。

系统先生成在线 SQLite 快照，再把数据库、主密钥和主题写入带 SHA-256 清单的安全
ZIP Payload，最后使用 Argon2id 派生的 AES-256-GCM 密钥进行分块认证加密。备份
口令不会写入数据库、日志或下载文件，丢失后无法找回。

导出不会停止 Agent 上报，但只包含创建快照时已经持久化的数据。工作目录需要大约
两倍于数据库与主题总大小的临时可用空间。

## 导入并恢复

导入会替换当前站点，操作前应再保存当前数据：

1. 选择 `.hostpin-backup` 文件。
2. 输入该文件的备份口令。
3. 输入当前站点管理员密码。
4. 输入大写 `RESTORE`。
5. 点击“IMPORT / REPLACE / RELOAD”。

Hostpin 在触碰活动数据库前会依次验证：

- 加密容器版本和每个认证分块；
- ZIP 路径、文件数量、大小、压缩率和重复路径；
- Manifest 中每个文件的大小和 SHA-256；
- SQLite `quick_check`、必需表和 Schema 版本；
- `master.key` 是否能解密 TOTP 和通知渠道凭据。

验证成功后，恢复文件会被暂存。服务端随后关闭 WebSocket 和写入队列、停止当前
HTTP 实例、替换数据，再在同一个进程内自动启动，无需依赖 systemd 或 Docker
重启策略。

恢复会清空备份中的所有浏览器 Session，因此页面恢复后必须使用备份站点原来的
管理员账户重新登录。Agent UUID 和独立凭据保持不变；只要面板地址不变，Agent 会
自动重连。

## 回滚和服务端切换

替换前的数据库、`master.key` 和 `themes/` 不会删除，而是重命名为带时间和 UUID 的
`.pre-restore-*` 路径。确认恢复正常并完成另一份备份后，再由管理员决定是否清理。

更换服务端时推荐：

1. 在旧服务器导出 `.hostpin-backup`。
2. 在新服务器安装相同或更高版本的 Hostpin，并先完成一个临时管理员初始化。
3. 配置正确的 `public_url`、HTTPS、反向代理和数据目录。
4. 在新服务器后台导入备份；恢复后使用旧站点管理员凭据登录。
5. 保留原域名并切换 DNS 时，Agent 无需修改；域名变化时需更新各 Agent
   `agent.json` 中的 `endpoint`。

如果新服务器通过 `HOSTPIN_MASTER_KEY` 或 YAML 配置外部主密钥，值必须与备份中的
主密钥相同，否则导入会被拒绝。这可以防止数据库恢复后所有加密凭据不可用。

## PostgreSQL

界面会识别 PostgreSQL 并显示运维命令，但不会尝试一键覆盖外部数据库。PostgreSQL
部署应使用 `pg_dump --format=custom` 和 `pg_restore`，同时单独备份 Hostpin 数据
目录中的 `master.key` 与 `themes/`。这样不会假设 Hostpin 容器拥有数据库创建权限
或安装了 `pg_dump`。

