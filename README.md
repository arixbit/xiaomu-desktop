# 小幕

小幕：一个入口，聚合你的所有片源。简洁好用的桌面影视播放器。

这是 macOS 桌面播放器的原型，用来验证配置读取、drpy2 规则和系统播放器这三件事。程序不内置片源、不内置配置、也不附带任何地址。播什么，由你在运行时提供的配置或直链决定。

## 免责声明

小幕只是播放器。它不提供、不收录、不分发任何影视内容。配置和规则由使用者自己提供，使用后果也由使用者自己承担。

## 原型里有什么

- 配置加载：仓库（`urls` 线路列表）或单份配置；带注释和尾逗号的 JSON；JPEG/PNG 里的 `前缀**base64`；HTTP 失败时改走 HTTPS；把 `api`、`ext`、`spider` 的相对路径解析成绝对地址。jar 只解析，不下载、不执行。
- 站点分类：type 0 XML CMS、type 1 JSON CMS、type 3 的 drpy2（`.js`）与 FongMi class、`csp_XBPQ` 文本规则（原生解释）、其他 `csp_` jar（未适配）、`.py`（不支持）。
- drpy2 宿主：QuickJS（`modernc.org/quickjs`，无 cgo）。按 ES 模块加载引擎和 `ext` 规则，并能加载 `assets://js/lib/` 下的 cheerio、crypto-js、jsencrypt、gbk、模板.js。宿主提供 `req`、`joinUrl`、`local.get/set/delete`，以及用 Go 实现的 `pdfh` / `pdfa` / `pd`。
- `xiaomu-probe`：给定配置地址和可选线路名，对前 N 个站点跑 首页 → 分类 → 详情 → 播放，打印通过/失败和耗时。
- 原生片库窗口：首次启动显示空片库，从片库内添加自己的配置；点选线路和站点、浏览分类与海报、搜索片名、翻页、查看详情、选择播放线路与分集。已导入的配置地址和本地脚本目录保存在本机用户数据目录，重启后恢复。可执行 drpy2、type 0/1 CMS 和使用基础 catvod 接口的 FongMi class；通用 XBPQ 规则直接在 Mac 解释，专用 Android jar 尚未适配。网络与脚本在后台执行，可取消加载。
- 直链播放入口：粘贴 mp4/m3u8，可选 Referer 和 User-Agent。片库解析出的请求头也会经本机 `127.0.0.1` 代理注入，再交给 MyGo `ui.Video` 播放。
- 网页取流：识别 CMS 分享页、HTML5 播放器、MacCMS 播放配置与 iframe，支持配置中的 type 0 网页解析和 type 1 JSON 解析。动态网页使用临时 WKWebView 会话，采集媒体请求和所需 UA / Referer / Cookie，不向页面开放 Go 方法。
- 独立播放页：MyGo 原生视频画面、Glass 控件、进度、播放速度、音量、全屏、线路与选集，以及可关闭的播完自动下一集。
- IPTV 直播：M3U/M3U8 频道列表、TXT 分组列表、TVBox `lives` 内嵌频道或外部列表；支持频道搜索、分组、上一/下一频道和备用线路自动切换。列表和频道中的 User-Agent、Referer、Cookie 等请求头经本地代理传递；HLS 分片不会被当成频道。

上游 drpy2 引擎在运行时从你的配置地址加载。`assets://js/lib/` 依赖优先读取 `-assets` 指定的本地文件，否则从所选引擎所在目录加载同名依赖；加载失败会明确报错。仓库里的小型占位模块仅由离线测试显式使用，不再嵌入应用或作为运行时后备。上游库（drpy2 为 LGPL-3.0，cheerio、crypto-js、jsencrypt 等另有各自许可证）不随本仓库分发。

FongMi class 目前覆盖基础文本/JSON 请求与常见分类、详情、播放方法。依赖 Android 宿主、二进制响应或额外加密函数的脚本会显示缺失能力；这些站点仍需单独适配。

## 环境

- Go 1.27.3 或更新。`go.mod` 的 `go 1.27.3` 即工具链要求；Go 1.27 的 `go mod tidy` 会去掉与这一行相同的 `toolchain` 指令。
- 播放窗口：macOS 12 及以上。MyGo 钉在 `v0.8.4-0.20261111083354-3f52033d99bb`（`video.OpenURL` / AVPlayer 在 v0.8.3 发布之后进入主线）。界面只用包 `ui`，没有网页前端。
- 探针在 Linux 和 macOS 上都能编译运行。

## 探针

```sh
go run ./cmd/xiaomu-probe -config '你的配置地址' -line '线路名' -n 3
```

`-line` 可省略，省略时用仓库的第一条。引擎目录缺少依赖或无法访问时，可用 `-assets` 指向 cheerio 等真实库的本地目录（路径形如 `js/lib/cheerio.min.js`，也支持直接放在该目录下）。不要把配置地址写进仓库。引擎自己的 `console.log` 会打在表格前面。

搜索型站点可加 `-query '片名'`，探针会测试搜索列表，不再用首个分类判断。探针只验证站点能否解析出 HTTP 播放地址；是否真能播出，还要在窗口里由 MyGo 播放器确认。

远程 `import` 被拦截或失效时，可以在 `-assets` 目录下放 `modules.json`，把完整模块 URL 映射到该目录内的本地 JS 文件。例如：

```json
{
  "https://example.com/lib/helper.js": "js/lib/helper.js"
}
```

映射只匹配完整 URL，优先读取本地文件；未映射的模块照常联网。文件内的相对导入仍按原始 URL 解析。映射文件和第三方脚本保留在仓库外，不随项目提交。HTTP 失败或返回 HTML 等非脚本内容时，报错会包含失败模块的 URL、状态码和 Content-Type。配置、脚本、海报和播放代理使用 MyGo 的 `netproxy.Transport()`：有代理环境变量时优先使用环境变量，否则读取系统代理设置（macOS 上包括 PAC）。

## 播放窗口

在 macOS 上：

```sh
go tool mygo dev
```

打包：

```sh
go tool mygo build
```

窗口默认打开片库。首次启动时片库为空：

1. 点击片库里的“添加片源”，在“配置地址”粘贴自己的配置 URL，再点击“加载配置”。仓库配置会显示线路列表，点击所需线路后显示站点。
2. 如果脚本需要本地依赖，在“脚本目录（可选）”填写与探针 `-assets` 相同的目录；该目录的 `modules.json` 也会生效。地址与目录只保存在本机用户数据目录，不进入仓库。
3. 点选站点，再选分类或搜索片名；点击影片，选择分集即可播放。播放页会先提取媒体地址，动态网页则交由隔离的 WKWebView 嗅探；取流失败时显示原因。

加载成功后，下次打开会恢复已导入的配置。加载失败后可以重试、取消或换站点。想测试视频直链，切到“直链播放”，填写 http(s) mp4/m3u8 地址和可选的 Referer、User-Agent，再点“播放”。HEVC 能否播出取决于这台 Mac 的系统解码器。

看 IPTV 时，点击左侧“直播”→“添加直播源”，导入自己的 M3U、TXT 或含 `lives` 的 TVBox 配置地址。选择来源、分组和频道即可播放；播放页可切频道及备用线路。单独导入的直播地址会保存，不替换已导入的点播配置。“添加片源”也能直接识别直播列表；TVBox 配置自带的直播在“直播”页选择。

当前直播播放支持 AVPlayer 能解码的 HTTP/HTTPS 视频流。已用用户提供列表中的多个 HLS 频道验证原生连续画面；可用性仍受来源、网络与编码影响。UDP/RTP 组播需由用户的 udpxy 等服务转换为 HTTP；RTSP/RTMP、节目单和回看尚未实现。HTTP-FLV 暂未转换为 AVPlayer 可读格式。失败或 15 秒未启动的频道会自动尝试备用线路，每条只试一次。

## 测试

```sh
go test ./...
go vet ./...
```

测试只用本地夹具，不访问网络，也不包含真实片源地址。

macOS 上可额外验证真实 AVPlayer 解码。用已安装的 FFmpeg 在仓库外生成测试流，再运行选择性测试（会启动 macOS 事件循环，静音播放）：

```sh
mkdir -p /tmp/xiaomu-local-hls
ffmpeg -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 6 -c:v libx264 -pix_fmt yuv420p -g 10 -c:a aac -hls_time 1 -hls_list_size 0 -hls_flags independent_segments -hls_segment_filename /tmp/xiaomu-local-hls/segment-%03d.ts /tmp/xiaomu-local-hls/stream.m3u8
XIAOMU_NATIVE_MEDIA_DIR=/tmp/xiaomu-local-hls go test ./cmd/xiaomu -run '^TestNativeIPTVHLS$' -v -count=1 -timeout=45s
```

该测试验证 IPTV 导入、请求头转发、HLS 分片获取、真实解码画面变化及播放时间推进。普通 `go test ./...` 会跳过这项需要本机媒体环境的检查。外部直播地址的可达性、音频实际听感和其他编码仍需单独验证。

## 在 Mac 上确认播放

Linux 上可以编译播放器，但不能跑 AVPlayer。请在 Mac 上：

1. 安装 Go 1.27.3+，克隆本分支。
2. `go test ./...`，确认探针和界面测试通过。
3. `go tool mygo dev` 打开窗口。
4. 切到“直链播放”，粘贴一条你自己的 mp4 直链（可先不填请求头），点播放，确认有画面和声音。
5. 再试一条需要 Referer 或 User-Agent 的 m3u8，填上后播放。代理会改写播放列表，分片也走同一组请求头。
6. 若有 HEVC 片源，用同样步骤看系统是否能解。
7. `go run ./cmd/xiaomu-probe -config '你的配置地址' -n 1`，看 drpy2 站点是否出现 PASS。
8. 返回片库，用同一份配置和脚本目录验证选站、分类、搜索、分集及播放。
