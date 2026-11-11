# 小幕

小幕：一个入口，聚合你的所有片源。简洁好用的桌面影视播放器。

这是 macOS 桌面播放器的原型，用来验证配置读取、drpy2 规则和系统播放器这三件事。程序不内置片源、不内置配置、也不附带任何地址。播什么，由你在运行时提供的配置或直链决定。

## 免责声明

小幕只是播放器。它不提供、不收录、不分发任何影视内容。配置和规则由使用者自己提供，使用后果也由使用者自己承担。

## 原型里有什么

- 配置加载：仓库（`urls` 线路列表）或单份配置；带注释和尾逗号的 JSON；JPEG/PNG 里的 `前缀**base64`；HTTP 失败时改走 HTTPS；把 `api`、`ext`、`spider` 的相对路径解析成绝对地址。jar 只解析，不下载、不执行。
- 站点分类：type 0 XML CMS、type 1 JSON CMS、type 3 的 drpy2（`.js`）与 FongMi class、`csp_` jar（不支持）、`.py`（不支持）。
- drpy2 宿主：QuickJS（`modernc.org/quickjs`，无 cgo）。按 ES 模块加载引擎和 `ext` 规则，并能加载 `assets://js/lib/` 下的 cheerio、crypto-js、jsencrypt、gbk、模板.js。宿主提供 `req`、`joinUrl`、`local.get/set/delete`，以及用 Go 实现的 `pdfh` / `pdfa` / `pd`。
- `xiaomu-probe`：给定配置地址和可选线路名，对前 N 个站点跑 首页 → 分类 → 详情 → 播放，打印通过/失败和耗时。
- 一个原生窗口：粘贴 mp4/m3u8 直链，可选 Referer 和 User-Agent，经本机 `127.0.0.1` 代理注入请求头后，用 MyGo `ui.Video` 播放。

仓库里的 `assets://` 文件是很小的占位模块，只为了让测试和导入能离线通过，并不是上游库。上游 drpy2 引擎在运行时从你的配置地址加载。这些库（drpy2 为 LGPL-3.0，cheerio、crypto-js、jsencrypt 等另有各自许可证）不随本仓库分发。跑官方引擎时，用 `-assets` 指向一份本地副本。

## 环境

- Go 1.27.3 或更新。`go.mod` 的 `go 1.27.3` 即工具链要求；Go 1.27 的 `go mod tidy` 会去掉与这一行相同的 `toolchain` 指令。
- 播放窗口：macOS 12 及以上。MyGo 钉在 `v0.8.4-0.20261111083354-3f52033d99bb`（`video.OpenURL` / AVPlayer 在 v0.8.3 发布之后进入主线）。界面只用包 `ui`，没有网页前端。
- 探针在 Linux 和 macOS 上都能编译运行。

## 探针

```sh
go run ./cmd/xiaomu-probe -config '你的配置地址' -line '线路名' -n 3
```

`-line` 可省略，省略时用仓库的第一条。`-assets` 指向 cheerio 等文件的目录（路径形如 `js/lib/cheerio.min.js`）。不要把配置地址写进仓库。引擎自己的 `console.log` 会打在表格前面。

## 播放窗口

在 macOS 上：

```sh
go tool mygo dev
```

打包：

```sh
go tool mygo build
```

窗口里粘贴一条 http(s) 的 mp4 或 m3u8，需要的话填 Referer 和 User-Agent，再点播放。HEVC 能否播出取决于这台 Mac 的系统解码器。

## 测试

```sh
go test ./...
go vet ./...
```

测试只用本地夹具，不访问网络，也不包含真实片源地址。

## 在 Mac 上确认播放

Linux 上可以编译播放器，但不能跑 AVPlayer。请在 Mac 上：

1. 安装 Go 1.27.3+，克隆本分支。
2. `go test ./...`，确认探针和界面测试通过。
3. `go tool mygo dev` 打开窗口。
4. 粘贴一条你自己的 mp4 直链（可先不填请求头），点播放，确认有画面和声音。
5. 再试一条需要 Referer 或 User-Agent 的 m3u8，填上后播放。代理会改写播放列表，分片也走同一组请求头。
6. 若有 HEVC 片源，用同样步骤看系统是否能解。
7. `go run ./cmd/xiaomu-probe -config '你的配置地址' -n 1`，看 drpy2 站点是否出现 PASS。
