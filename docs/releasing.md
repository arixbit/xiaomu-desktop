# 发布维护

小幕从 v0.3.0 起使用 MyGo 原生更新器。App 从本仓库最新正式 Release 读取当前平台的更新清单，下载完整更新包并用内置 Ed25519 公钥验证，校验通过后才替换 App。macOS 与 Windows 共用发行页，因此每次正式发布都要附上各平台对应的更新清单及更新包。更新包签名与安装器代码签名、公证各自独立。

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

脚本默认读取上述私钥路径，也可用 `XIAOMU_UPDATE_KEY_FILE` 指定路径，或让 CI 从密钥库注入 `MYGO_UPDATER_PRIVATE_KEY`。缺少密钥时脚本会停止；MyGo 会拒绝与公钥不匹配的密钥。脚本固定分架构构建 `darwin/arm64`（Apple Silicon）与 `darwin/amd64`（Intel）两套独立包，并在所选输出目录检查全部 DMG、ZIP、签名更新包、更新清单及单架构二进制，缺少或架构不符都会失败。普通 `go tool mygo build` 仍可用于本地测试，但缺少私钥时不会生成在线更新附件，不能直接用作公开发布。

在从 Universal 切换到分架构发布的**过渡桥梁版**中，可附加 `--with-universal-bridge`（例如 `sh scripts/build-release.sh --with-universal-bridge -o build/releases/v0.9.8`），额外生成不含 DMG/ZIP 的 `xiaomu-<版本>-darwin-universal.tar.gz` 与 `update-darwin-universal.json` 供旧版 Universal 用户自动升到过渡版；过渡版内置的 macOS 更新源会按当前实际运行架构（`arm64` / `amd64`）拉取后续单架构更新清单，再下一版起去掉该参数即可完成无感迁移。

Windows 发布用 `sh scripts/build-release-windows.sh -o build/releases/vX.Y.Z`，同样需要上述更新签名密钥。脚本固定构建 `windows/amd64`（因 `mygo-player v0.5.1` 暂仅提供 `windows-amd64` 动态库 `mpv-2.dll`，当前不构建 `windows/arm64` 产物），并在所选输出目录检查安装包、签名更新包及更新清单，缺少任一产物都会失败。验证后按下节上传附件。

Windows 上 MyGo 会自动下载 NSIS，macOS/Linux 上需要 `makensis`。安装包未做代码签名，用户首次安装会遇到 SmartScreen 提示。`scripts/installer.nsh` 按固定文件名清理小幕的安装文件，安装目录为空时才移除。新增 Windows 打包资源时同步维护卸载列表。

## 发布附件

从干净的发布提交构建 macOS（`darwin/arm64` 与 `darwin/amd64`）与 Windows（`windows/amd64`）包，通过 PR 合并，并将 Release 标签指向用于构建的提交。验证 App 版本、正式图标、各自单架构与签名后，准备以下文件：

- `xiaomu-<版本>-arm64.dmg` / `xiaomu-<版本>-amd64.dmg`：macOS Apple Silicon / Intel 手动安装镜像。
- `xiaomu-<版本>-macos-arm64.zip` / `xiaomu-<版本>-macos-amd64.zip`：macOS 可选手动安装压缩包。
- `xiaomu-<版本>-darwin-arm64.tar.gz` / `xiaomu-<版本>-darwin-amd64.tar.gz`：macOS 更新器下载的对应架构签名更新包。
- `update-darwin-arm64.json` / `update-darwin-amd64.json`：与各架构更新包配套的版本、说明、大小和签名。
- `SHA256SUMS`：所有上传附件（除校验文件自身）的 SHA-256。
- `xiaomu-setup-<版本>-amd64.exe`：Windows x64 安装包（NSIS，内置 `mpv-2.dll`）。
- `xiaomu-<版本>-windows-amd64.tar.gz`：Windows x64 更新器下载的签名更新包。
- `update-windows-amd64.json`：与 Windows x64 更新包配套的版本、说明、大小和签名。

先建立 Release 草稿，上传全部附件并校验；确认完整后再发布为最新正式版本。每个后续版本都必须同时上传更新清单与其引用的更新包。当前不生成差量包。不得修改已发布更新包的内容后继续沿用旧签名。

## 安装行为

更新窗口使用原生 `ui`，默认每天检查、由用户确认安装。用户在更新窗口勾选自动下载安装后，后续更新才会自动下载并于下次启动生效。安装完成时会保存播放历史。

App 必须已移出只读 DMG，并能写入所在目录。开发构建和没有写入权限的安装不会自动检查，手动检查会显示不可更新的原因。v0.2.0 及更早版本没有更新器，需手动安装一次新版本。v0.3.0 至 v0.9.7 的 macOS Universal 包会先通过过渡桥梁版的 `update-darwin-universal.json` 自动更新；自过渡版起，macOS 应用内更新器按实际运行芯片动态请求 `update-darwin-arm64.json` 或 `update-darwin-amd64.json`（若单架构清单缺失则自动回退 `update-darwin-universal.json`），后续更新会自动将旧版 Universal 替换为对应芯片的轻量单架构包。若用户跳过过渡版长期未升级，只需手动下载安装一次对应架构的新包即可。
