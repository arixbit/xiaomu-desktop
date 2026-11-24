# 给编码助手

小幕（Xiaomu）是原生桌面、TVBox 风格的多源影视播放器外壳（macOS 13+ 与 Windows 10/11 x64）。它只提供工具：绝不内置影视片源、默认影视配置或任何真实影视片源地址。用户的配置 URL 绝不能提交进仓库，包括测试、fixture 和文档。示例只用 `example.com` 这类保留域名或本地夹具。

弹幕服务例外（用户于 2026-11-23 明确授权）：允许内置有公开开源项目、可匿名访问的通用弹幕服务地址。来源和地址记录在 `docs/danmaku.md`；不使用其他应用的私有凭证。弹幕离线测试仍使用保留域名或本地夹具，真实联网验证必须显式启用，不提交用户的片名、配置或观看数据。

在线字幕例外（用户于 2026-11-24 明确授权）：允许内置公开免登录的射手（Shooter）文件哈希字幕匹配接口（`https://www.shooter.cn/api/subapi.php`），其他需鉴权的字幕源（如 ASSRT）仅支持用户自填 Token。来源和隐私边界记录在 `docs/subtitle.md`，常规测试严格离线。

## 技术栈（已定，不要换）

- **Go 1.27.3+**，零 cgo。
- **MyGo 原生桌面框架**（钉扎版本见 `go.mod`）：只用 `ui` 包与 Glass 插件，无 Web 前端、无 Bun。
- **播放引擎**：优先使用 `github.com/arixbit/mygo-player`（`libmpv` LGPL-2.1+，零 cgo；macOS 走 VideoToolbox + Metal 零拷贝，Windows x64 走 D3D11VA + `ui.Bitmap`），支持全格式容器、多音轨、内外挂字幕及原生请求头；其他平台与无头单测平滑回退 MyGo 内置 `ui.VideoPlayer`。HTTP/HLS/HTTP-FLV 播放经 `127.0.0.1` 本地代理注入 Referer / UA / Cookie、改写 m3u8 并统计下载速度；`libmpv` 后端下直播亦支持直连 `rtsp://` / `rtmp://`。
- **脚本引擎**：`modernc.org/quickjs`（纯 Go，无 cgo），用于运行 drpy2 与 FongMi class 脚本；另原生解释 `csp_XBPQ` 规则与 type 0/1 CMS。
- **隔离 WebView**：仅用于动态网页嗅探（`cmd/xiaomu/sniff.go`）和网盘官方网页登录（`cmd/xiaomu/pan_auth.go`），不向页面暴露 Go 方法。
- **目标平台**：macOS 13+（`darwin/universal`，App 声明与 Go 1.27.3 最低要求一致）与 Windows 10/11（`windows/amd64`；`mygo-player v0.2.1` 暂未提供 `windows/arm64` 动态库）。

## 目录

- `cmd/xiaomu`：原生桌面应用（首页/豆瓣热播/继续观看、跨站搜索、详情与选集、播放页与弹幕/音轨/字幕/跳过片头片尾、本地视频续播与同目录连播、IPTV 直播、播放历史、追剧、网盘与 AList 登录、片源测速与失效站点过滤、系统集成与应用内签名更新）。
- `cmd/xiaomu-probe`：命令行探针，调用 `internal/probe` 验证站点链路。
- `internal/config`：拉取并解析单份配置或多仓 `urls`；支持 JSON 注释、尾逗号、图片隐写 `前缀**base64`、HTTP 失败回退 HTTPS 及相对路径解析。jar 只解析，不下载、不执行。
- `internal/source` / `internal/library` / `internal/catalog`：站点识别、并发扫描与聚合索引；支持 type 0/1 CMS、drpy2、基础 FongMi class、`csp_XBPQ`、AList 与网盘站。`csp_` Android Dex 爬虫自动跳过。
- `internal/drpy` / `internal/fongmi` / `internal/pdfh` / `internal/xbpq`：脚本宿主与规则解析。
- `internal/pan` / `internal/alist`：原生夸克 / UC / 阿里云盘 / 百度网盘分享解析、原画直链与 AList 客户端。
- `internal/resolve`：网页取流、分享页/iframe 解析、type 0/1 解析器与隔离 WebView 嗅探桥接。
- `internal/iptv`：M3U/M3U8、TXT 与 TVBox `lives` 频道表解析及请求头合并。
- `internal/danmaku`：弹弹play 兼容接口、国内直连公共弹幕源、过滤屏蔽与高能密度曲线。
- `internal/douban`：豆瓣热播电影/剧集榜单客户端与本机缓存。
- `internal/proxy`：回环 HTTP 代理，注入请求头、改写 m3u8、支持网盘分片预取与流式刷新。
- `internal/probe` / `internal/urljoin` / `internal/fileurl`：站点探针与 URL 工具。

## 怎么跑与验证

```sh
go test ./...
go test -race ./...
go vet ./...
go tool mygo dev
go run ./cmd/xiaomu-probe -config '<配置URL>' -line '<线路名>' -n 3 -assets <dir>
```

- macOS 真机可显式启用选择性原生播放测试（需安装 `ffmpeg`）：
  - `XIAOMU_NATIVE_MPV_TEST=1 go test ./cmd/xiaomu -run '^TestNativeMPV' -v -count=1 -timeout=60s`（验证本地多音轨/字幕 MKV、代理 HEVC MKV、chunked HTTP-FLV 直播）。
  - `XIAOMU_NATIVE_MEDIA_DIR=/tmp/xiaomu-local-hls go test ./cmd/xiaomu -run '^TestNative' -v -count=1 -timeout=45s`（验证本地 HLS 直播、末尾自动切集与防睡眠断言）。
- 第三方脚本依赖（如 `cheerio.min.js`、`crypto-js.js`、`jsencrypt.js`、`gbk.js`）保留在仓库外，由 `-assets` 或「脚本目录」按需读取，绝不提交。

## 工作规矩

改动小而简单，先删后加，不加没要的功能。不往 `main` 推，走 PR。交付前跑 `go test ./...` 和 `go vet ./...`（以及相关回归测试），并给出证据。没验证就写明没验证。
