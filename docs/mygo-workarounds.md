# MyGo 能力对照与升级体检清单

本文档记录小幕（Xiaomu）当前针对 MyGo 暂缺能力所做的自实现/绕路代码、已发现待评估的 MyGo 新能力，以及每次 MyGo 发新版时的标准升级检查步骤。

- 当前钉扎版本：`github.com/egoist/mygo v0.10.0`（见 `go.mod`）
- 自动监控：已通过 `.github/dependabot.yml` 每天检查 `github.com/egoist/mygo` 新版并自动发起更新 PR 与 CI 校验。

## 一、小幕为 MyGo 暂缺能力所做的绕路清单

每次因为 MyGo 暂不支持或边界问题而在小幕侧兜底实现时，补记一行；上游提供稳定原生能力后优先删除绕路代码（先删后加）。

| 小幕位置 | 自实现 / 绕路内容 | 当前状态（v0.10.0 核实） | 上游满足何种条件时可简化/删除 |
|---|---|---|---|
| `internal/proxy/proxy.go` | 本地 `127.0.0.1` 回环代理注入请求头并改写 m3u8 | `ui/video.OpenURL(url)` 仍不支持自定义请求头（`Referer` / `User-Agent` / `Cookie`） | `video.OpenURL` 原生支持自定义请求头（含 HLS 分片继承）后，可评估简化非改写场景 |
| `cmd/xiaomu/sniff.go` + `sniff.js` | 临时 WKWebView 注入脚本 + 250ms 轮询采集媒体请求 | `mygo.Page` 尚无原生网络请求拦截/监听事件 | `mygo.Page` 提供原生网络请求观察/拦截回调后，可替代轮询抓流 |
| `cmd/xiaomu/auto_next.go` / `playback_power.go` | HLS 距结尾 ≤250ms 且静止 2 秒兜底判定播放结束 | 针对 AVFoundation 部分 HLS 末尾不触发 `Ended()` 的保护 | 上游确认修复 AVFoundation HLS 结尾事件并在本地 HLS 回归测试验证后可移除 |
| `cmd/xiaomu/main.go` (`fitMainWindow`) | Windows 启动/还原/退出全屏后把窗口裁切到工作区内 | 规避 Windows 居中或还原窗口越出可见工作区的问题 | 上游在 Windows 侧内置工作区边界钳制后可删除 |
| `cmd/xiaomu/player_pointer.go` | 260ms 延迟提交单击，避免双击全屏时先暂停再继续 | `ui` 尚无内置单/双击互斥手势 | `ui` 提供原生单/双击防抖手势后可替换 |
| `cmd/xiaomu/danmaku.go` (`danmakuOverlay`) | 4 次偏移 `p.TextLayout` 模拟弹幕四向硬描边（已缓存 `ui.ShapeText` 排版，不再每帧重排） | `ui.Span` 支持模糊 `TextShadow`，但 `Painter` 尚无原生硬描边（stroke text） | `Painter` 原生支持文字描边后，可将每条弹幕 5 次绘制合并为 1 次 |
| `scripts/build-release.sh` | 构建完成后通过 `ditto` 生成 `xiaomu-<ver>-macos-arm64.zip` 与 `xiaomu-<ver>-macos-amd64.zip` | MyGo v0.10.0 的 `mygo build` 默认产出 `.app`、`.dmg` 与更新包，不再自动产出 `.zip` | 若上游恢复内置 macOS `.zip` 产物，可移除脚本中的 `ditto` 步骤 |

## 二、MyGo v0.10.0 新能力接入记录

| MyGo 能力 | 对应小幕场景 | 状态与说明 |
|---|---|---|
| `ui.ShapeText` / `ui.TextLayout` + `p.TextLayout` | `cmd/xiaomu/danmaku.go` 60fps 弹幕渲染 | **已接入**：弹幕入场时排版一次并缓存 `*ui.TextLayout`，后续帧直接 `p.TextLayout` 复绘并按帧清理离场缓存 |
| `github.com/egoist/mygo/ui/netimage` | `cmd/xiaomu/poster.go` 海报加载与缓存 | **已接入**：替换原 330 行手写 worker/LRU/失败表，原生获得跨重启磁盘缓存、可见优先下载与离屏自动取消；保留 `posterRoundTripper` 承接豆瓣 Referer/换域名单次重试与尺寸预算校验 |
| `ui.VideoPlayer.Muted()` / `SetMuted(bool)` | `cmd/xiaomu/player_ui.go` / `keyboard.go` | **已接入**：播放器静音按钮与 `M` 键一键静音 |
| `Window.SetAlwaysOnTop(bool)` / `Window.CapturePage()` | `cmd/xiaomu/player_extra.go` | **已接入**：播放器置顶、置顶迷你小窗与一键截图 |
| `github.com/egoist/mygo/plugins/preferences` | `cmd/xiaomu/preferences.go` | 现有手写实现轻量且需保持既有 `preferences.json` 格式兼容，暂不迁移 |

## 三、MyGo 发新版后的标准升级体检步骤

收到 Dependabot 自动 PR 或 GitHub Release 通知后，按以下 4 步核对：

1. **查看 Release Notes 与真实 API 变更**：不只读发版摘要，重点对比 `mygo`、`ui`、`ui/video`、`ui/netimage`、`plugins/glass`、`plugins/updater`、`netproxy` 的公开符号增删改。
2. **逐条对照上表**：检查“绕路清单”中是否有项已被上游原生支持或修复，以及“待评估能力”是否有新变化。
3. **检查构建与打包链路**：确认 `go tool mygo build` 在 macOS/Windows 的产物文件名、签名更新清单格式及图标/协议关联无隐蔽变化。
4. **验证并更新本文档**：运行 `go test -count=1 ./...`、`go vet ./...` 与关键回归测试；确认后更新 `go.mod` 与本文档中的当前钉扎版本和状态表。
