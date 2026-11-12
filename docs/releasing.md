# 发布维护

小幕从 v0.3.0 起使用 MyGo 原生更新器。App 读取本仓库最新正式 Release 的 `update-darwin-universal.json`，下载完整更新包并用内置 Ed25519 公钥验证，校验通过后才替换 App。签名更新与 Apple Developer ID 签名、公证是不同的机制。

## 签名密钥

公钥在 `mygo.json` 的 `updates.publicKey` 中；私钥只保存在维护者本机：

`~/Library/Application Support/Xiaomu/release-keys/mygo-update.key`

请将这份私钥备份到自己的密码管理器或安全离线备份。后续版本必须沿用同一密钥；丢失或更换会让已安装 App 无法验证后续更新。不要把私钥提交到仓库、发布附件或写入日志。`.gitignore` 已忽略 `mygo-update.key`。

## 构建

更新 `mygo.json` 的版本、`CHANGELOG.md` 对应 `## 版本号` 的说明、README 下载版本和许可证声明中的版本；补充 `docs/releases/` 中的版本说明。运行项目测试，再使用：

```sh
go test ./...
go vet ./...
sh scripts/build-release.sh -o build/releases/v0.3.0
```

脚本默认读取上述私钥路径，也可用 `XIAOMU_UPDATE_KEY_FILE` 指定路径，或让 CI 从密钥库注入 `MYGO_UPDATER_PRIVATE_KEY`。缺少密钥时脚本会停止；MyGo 会拒绝与公钥不匹配的密钥。普通 `go tool mygo build` 仍可用于本地测试，但缺少私钥时不会生成在线更新附件，不能直接用作公开发布。

## 发布附件

从干净的发布提交构建 Universal 包，通过 PR 合并，并将 Release 标签指向用于构建的提交。验证 App 版本、正式图标、双架构和签名后，准备以下文件：

- `xiaomu-<版本>.dmg`：用户手动安装。
- `xiaomu-<版本>-macos-universal.zip`：可选的手动安装包。
- `xiaomu-<版本>-darwin-universal.tar.gz`：更新器下载的签名更新包。
- `update-darwin-universal.json`：与该更新包配套的版本、说明、大小和签名。
- `SHA256SUMS`：上述四个文件的 SHA-256。

先建立 Release 草稿，上传全部附件并校验；确认完整后再发布为最新正式版本。每个后续版本都必须同时上传更新清单与其引用的更新包。当前不生成差量包。不得修改已发布更新包的内容后继续沿用旧签名。

## 安装行为

更新窗口使用原生 `ui`，默认每天检查、由用户确认安装。用户在更新窗口勾选自动下载安装后，后续更新才会自动下载并于下次启动生效。安装完成时会保存播放历史。

App 必须已移出只读 DMG，并能写入所在目录。开发构建和没有写入权限的安装不会自动检查，手动检查会显示不可更新的原因。v0.2.0 及更早版本没有更新器，需手动安装一次 v0.3.0。
