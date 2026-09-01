# Hostpin 中文文档

[返回中文 README](../../README.zh-CN.md) · [English documentation](../deployment.md)

Hostpin 是一个单站点、单管理员、单服务实例的自托管探针平台。服务端默认使用
SQLite；监控 Agent 和延迟测量节点都从被监控网络主动连接面板，因此可部署在
NAT、CGNAT 或没有公网 IP 的路由器中。

## 推荐阅读顺序

1. [部署与迁移](deployment.md)：先用默认 SQLite 启动服务并完成管理员初始化。
2. [三网延迟与测量节点](latency-probes.md)：查看节点三网曲线，或添加公网/家庭路由器测量点。
3. [一键备份与恢复](backup-restore.md)：导出加密站点包或切换服务端。
4. [流量统计](traffic-accounting.md)：理解流量基线、月度重置和配额模式。
5. [主题兼容](theme-compatibility.md)：安装和切换 Komari 前台主题。
6. [API 与协议](api.md)：对接原生公共接口或理解 Agent 数据边界。

参与开发或准备发布时，再阅读[系统架构](architecture.md)和
[测试与发布](testing.md)；维护者打正式 Tag 前还需按
[发布指南](releasing.md)配置更新签名密钥并完成清单。

## 默认数据库

Hostpin 无配置启动时使用 SQLite，数据库路径为 `./data/hostpin.db`。SQLite 模式
不依赖外部数据库服务，适合个人、自托管用户和约 100 个节点的站点。

当规模接近 1,000 个节点时，可切换到 PostgreSQL 16+。Hostpin 提供离线
SQLite → PostgreSQL 迁移命令，会复制数据并核对记录数量与时间范围，不会删除
原 SQLite 数据库。详细步骤见[部署与迁移](deployment.md#从-sqlite-迁移到-postgresql)。

## 功能边界

Hostpin 支持主机指标、历史、流量、三网延迟、固定类型服务探测、独立或监控兼任的
延迟测量点、告警、通知、计费信息和主题。它明确不提供远程终端、Shell、任意命令、容器控制
或日志采集；服务端也不能向 Agent 下发脚本。
