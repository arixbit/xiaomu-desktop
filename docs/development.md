# 开发与验证

## 环境与构建

- 使用 Go 1.27.3 或更新版本，MyGo 的钉扎版本以 [go.mod](../go.mod) 为准。
- 原生窗口和 AVPlayer 验证需要 macOS 13 或更新版本。界面使用 MyGo 的 `ui` 包和 Glass 插件。命令行探针也可在 Linux 上构建运行。

在 macOS 上启动开发窗口。

```sh
go tool mygo dev
```

构建适用于 Apple Silicon 和 Intel 的 Universal 应用包。

```sh
go tool mygo build -platform darwin/universal
```

正式发布使用 `sh scripts/build-release.sh`。脚本读取更新签名密钥并生成在线更新附件。开发模式不会自动检查更新。 [发布维护说明](releasing.md)

正式应用包使用 `xiaomu-icon/macos/AppIcon.icns`，开发运行使用 `resources/icon.png`。深色及 Icon Composer 素材保留在 `xiaomu-icon/` 供设计使用。

## 命令行探针

把自己的配置地址传给 `xiaomu-probe`。探针会对前 N 个站点依次检查首页、分类、详情和播放地址，并输出结果与耗时。

```sh
go run ./cmd/xiaomu-probe -config '你的配置地址' -line '线路名' -n 3
```

省略 `-line` 时选择仓库的第一条线路。指定线路时填写完整名称，包括其中的 emoji。搜索型站点可加 `-query '片名'`，改用搜索结果检查。引擎的 `console.log` 会出现在结果表格前。探针的 PASS 表示解析出了 HTTP 播放地址，画面和声音需在 Mac 上用播放器确认。

## 配置与脚本依赖

- 配置可使用 `urls` 仓库列表或单份配置，支持带注释和尾逗号的 JSON，以及 JPEG/PNG 里的 `前缀**base64`。HTTP 失败时会尝试 HTTPS。配置里的 `api`、`ext` 与 `spider` 相对路径会解析成绝对地址，jar 字段仅供识别。
- drpy2 使用无 cgo 的 QuickJS（`modernc.org/quickjs`），按 ES 模块加载配置中的引擎与规则。宿主提供 `req`、`joinUrl`、`local.get/set/delete` 和用 Go 实现的 `pdfh` / `pdfa` / `pd`。
- 基础 FongMi JS 支持文本及 JSON 请求与常见分类、详情和播放方法。脚本依赖 Android 宿主、二进制响应或额外加密函数时，会显示缺失能力。

`assets://js/lib/` 依赖优先从指定脚本目录读取，随后尝试引擎所在目录。引擎缺少依赖或无法访问时，可在探针中用 `-assets`，或在应用的「片源 → 添加片源」中填写「脚本目录（可选）」。例如将 cheerio 放在该目录的 `js/lib/cheerio.min.js`，也可直接放在目录下。仓库中的占位模块供离线测试显式使用。drpy2（LGPL-3.0）及 cheerio、crypto-js、jsencrypt 等上游库各有许可证，由用户配置或脚本目录提供。

如远程 `import` 失效，可在脚本目录创建 `modules.json`，将完整模块 URL 映射到本地 JS 文件。例如用保留域名表示一条映射。

```json
{
  "https://example.com/lib/helper.js": "js/lib/helper.js"
}
```

映射精确匹配完整 URL，优先读取本地文件。未映射的模块照常联网，本地文件的相对导入仍按原始 URL 解析。HTTP 失败或返回 HTML 等非脚本内容时，错误会报告模块地址、状态码和 Content-Type。真实配置地址、映射文件和第三方脚本放在仓库外。

## 搜索与媒体请求

- CMS 搜索和详情直接请求各自的接口。搜索结果按来源、关键词与页码在内存中缓存 10 分钟，失败站点冷却 30 秒。更换片库或点击「重新加载」会清空缓存和冷却状态，停止搜索会保留已经返回的结果。
- 配置、脚本、海报和播放代理使用 MyGo 的 `netproxy.Transport()`，优先读取代理环境变量，再读取系统代理设置（macOS 上包括 PAC）。
- 取流支持 CMS 分享页、HTML5 播放器、MacCMS 播放配置、iframe 与配置中的网页和 JSON 解析器。动态网页使用隔离的 WKWebView 会话采集媒体请求和所需请求头。视频与 HLS 分片通过本机回环代理转发请求头，由 MyGo `ui.Video` 交给 AVPlayer 播放。
- IPTV 使用 AVPlayer 可解码的 HTTP 或 HTTPS 流。UDP/RTP 组播需通过用户自己的服务转成 HTTP。RTSP、RTMP、节目单和回看暂未支持，HTTP-FLV 暂无转封装。

## 自动化测试

```sh
go test ./...
go vet ./...
```

默认测试使用本地夹具和回环 HTTP 服务。测试数据使用本地示例，不依赖外部片源。

## 在 Mac 上验证播放

1. 运行 `go tool mygo dev`。在侧边栏点「打开链接」，用自己的 MP4 直链确认画面与声音。
2. 再用一条需要 Referer 或 User-Agent 的 M3U8 确认视频和分片均可读取。有 HEVC 视频时，可用同一入口确认这台 Mac 的解码情况。
3. 用自己的 TVBox 配置检查首页推荐、全库搜索、详情、分集和播放。在「片源」页展开线路可单独验证站点分类。可用探针对照配置与地址解析结果。

有 FFmpeg 时，可生成本地 HLS 流验证 IPTV。测试会启动 macOS 事件循环并静音播放。

```sh
mkdir -p /tmp/xiaomu-local-hls
ffmpeg -f lavfi -i testsrc2=size=160x90:rate=10 \
  -f lavfi -i sine=frequency=440:sample_rate=48000 -t 6 \
  -c:v libx264 -pix_fmt yuv420p -g 10 -c:a aac \
  -hls_time 1 -hls_list_size 0 -hls_flags independent_segments \
  -hls_segment_filename /tmp/xiaomu-local-hls/segment-%03d.ts \
  /tmp/xiaomu-local-hls/stream.m3u8
XIAOMU_NATIVE_MEDIA_DIR=/tmp/xiaomu-local-hls \
  go test ./cmd/xiaomu -run '^TestNativeIPTVHLS$' -v -count=1 -timeout=45s
```

这项测试检查 IPTV 导入、请求头转发、HLS 分片获取、解码画面变化和播放时间推进。常规 `go test ./...` 会跳过需要自行准备媒体的检查。

v0.7.0 已在 Apple Silicon Mac 上用本地生成的 H.264 和 HEVC 视频验证 AVPlayer 连续解码与续播。Intel 实机、外部片源和音频听感仍需按实际环境验证。 [v0.7.0 验证记录](releases/v0.7.0.md#验证)
