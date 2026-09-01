# Komari 主题兼容

[返回文档首页](README.md) · [English](../theme-compatibility.md)

Hostpin 接受根目录含 `komari-theme.json` 和 `dist/index.html` 的 Komari 主题 ZIP。
它支持本地化元数据、预览、SPA Fallback，以及 `managed`、`raw` 和安全的站内相对
`redirect` 配置。Managed 默认值会与已保存值合并，并由 `GET /api/public` 通过
`theme_settings` 返回。

面向公共主题的 REST、WebSocket 和 RPC2 接口使用当前 Komari 主题所依赖的相同
路径。Hostpin 明确不实现 Komari Agent 协议、插件协议、管理 API、远程终端或命令
结果 RPC 方法。

主题 ZIP 属于不受信任输入。安装流程会拒绝：

- 目录穿越和绝对路径；
- 符号链接及其他链接条目；
- 重复路径、文件数量或解压大小超限；
- 可疑压缩率和 ZIP bomb；
- 无效清单或 SHA-256 不匹配。

兼容基线固定为 2026-08-23 审查过的公共主题契约。官方 Komari Web 压缩包使用保留
短名称 `default`；Hostpin 可原样接受该包，并在内部保存为 `komari-default`，从而
保留内置 Hostpin 界面。只有官方 Komari Web、Carbon 和 Pulse 固定样本全部通过
兼容测试后，才应跟进上游契约变化。

第三方主题会在访客浏览器中执行代码。仅安装经过审查的主题，并尽量让管理员会话与
公共主题浏览环境隔离。

