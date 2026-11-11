# 给编码助手

小幕（Xiaomu）是 macOS 原生、TVBox 风格的多源影视播放器外壳。它只提供工具：绝不内置片源、默认配置或任何真实源地址。用户的配置 URL 绝不能提交进仓库，包括测试、fixture 和文档。示例只用 `example.com` 这类保留域名或本地夹具。

## 技术栈（已定，不要换）

Go 1.27.3+。MyGo native 模板，只用 `ui` 包：无 Web 前端、无 Bun。无 cgo。JS 引擎 `modernc.org/quickjs`。播放用 `ui.Video`（AVPlayer）；`video.OpenURL` 不能设请求头，所以经 `127.0.0.1` 本地代理注入 Referer / UA。WebView 只留给以后的嗅探。目标 macOS 12+，darwin/universal。MyGo 版本以 `go.mod` 里的钉扎为准。

## 目录

- `cmd/xiaomu`：原生窗口。粘贴 mp4/m3u8，可选 Referer 和 User-Agent，经本地代理交给 `ui.Video`。
- `cmd/xiaomu-probe`：命令行探针，调用 `internal/probe`。
- `internal/config`：拉配置。仓库 `urls` 或单份配置；JSON 注释和尾逗号；JPEG/PNG 里的 `前缀**base64`；HTTP 失败改 HTTPS；解析 `api` / `ext` / `spider`。jar 只解析，不下载、不执行。
- `internal/pdfh`：`pdfh` / `pdfa` / `pd`（`&&`、`Text`、`Html`、属性、`:eq(n)`、`||`）。
- `internal/drpy`：QuickJS 宿主。加载引擎和 `ext`，提供 `req`、`joinUrl`、`local`。`assets://` 先找 `-assets` 目录，否则用内置占位模块（不是上游库）。
- `internal/probe`：对前 N 个站点跑首页 → 分类 → 详情 → 播放。目前只执行 drpy2。
- `internal/proxy`：回环代理，注入请求头并改写 m3u8。
- `internal/urljoin`：相对地址拼接。

## 怎么跑

```sh
go test ./...
go vet ./...
go tool mygo dev
go run ./cmd/xiaomu-probe -config '<配置URL>' -line '<线路名>' -n 3 -assets <dir>
```

`-line` 可省略（用第一条）。`-assets` 覆盖 `assets://`。从 `https://raw.githubusercontent.com/hjdhnx/dr_py/main/libs/` 下载 `cheerio.min.js`、`crypto-js.js`、`jsencrypt.js`、`gbk.js` 到 `<dir>/js/lib/`。这些文件不要提交。

## 里程碑

- M1：配置加载、QuickJS、drpy2、本地代理（本分支）。
- M2：CMS（type 0 / 1）。
- M3：FongMi JS。
- M4：直播、嗅探、签名公证、HEVC。

`csp_` jar 爬虫是 Dex，Mac 上不支持。

## 进度与已知问题（2026-11-11，用户 Mac 实测）

窗口能启动。`mygo.json` 的 `main` 要写成 `./cmd/xiaomu`；不带 `./` 会被当成标准库路径（用户在本地改，助手不要抢着改这个文件）。`-line` 必须用带 emoji 的全名，应支持去掉 emoji 或模糊匹配。部分线路的 `drpy2.min.js` 会 `import` 远程依赖（如 `https://down.nigx.cn/qu.ax/*.js`）；某些网络下 Cloudflare 403 返回 HTML，引擎报 `SyntaxError: '<'`。要查代理和 UA，并对远程模块响应做内容校验，给出清楚报错。AVPlayer 实际播放和 HEVC 尚未验证。

## 工作规矩

改动小而简单，先删后加，不加没要的功能。不往 `main` 推，走 PR。交付前跑 `go test ./...` 和 `go vet ./...`，并给出证据。没验证就写明没验证。
