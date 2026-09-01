# 维护者发布指南

[返回文档首页](README.md) · [English](../releasing.md)

Git Tag 是正式版本号来源。推送 `v0.1.0` 后，`.github/workflows/release.yml` 会重新
执行源码测试、构建全部附件、检查 Linux Agent 资源占用、签名更新清单，并创建
GitHub Release。

## 首次签名配置

第一次打 Tag 前只生成一次 Agent 更新签名密钥：

```sh
go run ./cmd/hostpin-manifest --generate-key-dir .release/update-signing
gh secret set HOSTPIN_UPDATE_PUBLIC_KEY < .release/update-signing/public.key
gh secret set HOSTPIN_UPDATE_PRIVATE_KEY < .release/update-signing/private.key
```

密钥文件权限为 `0600`，整个 `.release/` 已被 Git 忽略。私钥还应保存一份离线备份。
不要随意轮换：已发布 Agent 只信任编译进二进制的公钥。生成更新清单时会校验两个
GitHub Secret 的格式和配对关系，不匹配则终止发布。

源码 module、默认下载地址与构建元数据均已固定为 `github.com/chnzzh/hostpin`。
工作流仍会按实际 `${owner}/${repository}` 注入 Release 下载地址，因此从 Fork
生成测试版本时，不会错误下载上游仓库的附件。

## 发布清单

1. 更新 `CHANGELOG.md`，新增 `docs/releases/vX.Y.Z.md`。
2. 重新构建前端，确保仓库中的内嵌前端产物为最新版。
3. 使用 Go 1.26.x 执行 `make release-check`。
4. 执行浏览器/主题测试，并手动运行一次 Capacity 工作流。
5. 用 `git status --ignored` 确认没有 `.env`、数据库、备份、密钥或发布二进制将被提交。
6. 推送 `main`，等待 CI 与容量测试通过。
7. 确认两个签名 Secret 已配置，再创建并推送 Tag：

```sh
git tag -s v0.1.0 -m "Hostpin v0.1.0"
git push origin v0.1.0
```

如果没有配置 Git Tag 签名，也可使用 annotated tag。工作流会拒绝非语义化 Tag；
任一测试、构建、资源占用、校验和或密钥校验失败都不会发布。

## 发布后检查

- 确认有 13 个 Agent、2 个服务端、15 个独立校验文件、`SHA256SUMS`、许可证附件、
  第三方许可证包和 `update-manifest.json`。
- 校验 `SHA256SUMS`，并对可运行的服务端附件执行 `version`。
- 启动全新 SQLite 服务，完成首次设置，通过面板脚本分别加入普通节点和纯延迟节点，
  再验证卸载 dry-run。
- 仅在普通用户测试主机上确认签名清单和下载地址正确后，才启用 Agent 自动更新。
