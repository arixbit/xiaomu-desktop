# 小幕与 Android TVBox 的兼容性核查

核查日期：2026-11-11。本文是实现路线评估，未实现 Android 运行环境，也未验证用户原配置在 Mac 上实播。

## 产品目标与结论

用户的目标是：同一份在 Android 设备上可用的 TVBox 配置，导入小幕后也能选站、浏览、搜索、选集和播放。读取配置成功、显示站点列表、获得一个 URL，都不能单独作为完成标准。

参考 Android 实现是可行的。需要复现的是配置、爬虫运行、解析、网页嗅探、媒体请求和解码之间的契约。现有小幕缺少的主要是中间运行能力，不能把这些失败全部归因于配置或片源。

如果目标是广泛兼容现有第三方 Android jar，建议优先验证“Mac 原生界面 + 独立 Android 运行环境”。这需要实际的 Android 系统和运行时，不能把安装普通 JVM 或增加一个 Go 接口当成等价方案。运行环境在 Mac 本机启动时，不必长期依赖另一台 Android 设备，但体积、内存、启动时间和两种 Mac 架构的支持都必须实测。

这是一条待验证的实现路线，不是已经可用的轻量兼容库。当前原生技术栈和界面可以保留；增加运行环境、打包和生命周期管理会扩大交付范围，应先完成最小实播验证，再决定正式集成。

## 本地 APK 的证据

检查对象：根目录的 `影视仓-手机平板-64位-3.3.8_opt.apk`。

- 大小：55,264,794 字节。
- SHA-256：`f4c044082146df78522876d8b7ba2f6914653a8932bd2737edc736d7a4ba957c`。
- ZIP 中有 7 个 DEX 文件；本地库目录仅发现 `lib/arm64-v8a/`。
- DEX 方法表包含 `com.github.catvod.crawler.Spider` 的 `homeContent`、`categoryContent`、`detailContent`、`searchContent`、`playerContent` 和 `proxyLocal` 引用。
- 包含 `dalvik.system.DexClassLoader` 引用，以及 `csp_`、`spider`、`jar`、`parses`、`playUrl`、`jx` 等字符串。
- 包含 QuickJS、Python、IJK/FFmpeg 相关本地库，以及 WebView 请求拦截相关方法引用。
- `assets/js/lib/` 带有 drpy、cheerio、crypto-js、jsencrypt 等脚本。`assets/parse.html` 会将解析前缀与目标 URL 拼接，放入 iframe。
- 主 DEX 包含 `com.dex2c.shell` 壳相关类。此次是 ZIP、DEX 表和可读资源的静态检查，不是完整反编译或运行时跟踪。

这些证据能说明 APK 带有多种执行和解析组件，但不能证明某一个站点实际走了哪条路径。具体站点的实现还可能来自配置加载的外部 jar；仅分析 APK 不足以还原所有站点。未将 APK 内容、第三方脚本或用户配置地址加入本文或测试夹具。

## 与公开 Android 实现的对照

参照 FongMi/TV 的公开源码，固定到提交 `5b41d9da52e1573ab9b5d399d656d11eb9fe8557`。它用于解释同类 TVBox 接口，不能认定为此 APK 的精确源码版本。

| 部分 | 可确认的行为 | 对小幕的意义 |
| --- | --- | --- |
| [BaseLoader](https://github.com/FongMi/TV/blob/5b41d9da52e1573ab9b5d399d656d11eb9fe8557/app/src/main/java/com/fongmi/android/tv/api/loader/BaseLoader.java) | 分别派发 JS、Python、`csp_` 等站点；保留爬虫代理调用 | 配置兼容不等于执行环境兼容 |
| [JarLoader](https://github.com/FongMi/TV/blob/5b41d9da52e1573ab9b5d399d656d11eb9fe8557/app/src/main/java/com/fongmi/android/tv/api/loader/JarLoader.java) | 下载或加载 jar，创建 `DexClassLoader`，实例化 `com.github.catvod.spider.*`，以 Android `Context` 初始化；支持代理和扩展解析方法 | jar 不是普通规则 JSON，也不能交给 QuickJS 执行；只做 DEX 转 Java 仍可能缺 Android API 和本地库 |
| [ParseJob](https://github.com/FongMi/TV/blob/5b41d9da52e1573ab9b5d399d656d11eb9fe8557/app/src/main/java/com/fongmi/android/tv/player/parse/ParseJob.java) | 处理 `playUrl`、配置解析器、JSON 解析、jar 扩展解析和网页解析 | `parse`/`jx` 表示后续处理需求，不能统一当成终止条件 |
| [ParseWebView](https://github.com/FongMi/TV/blob/5b41d9da52e1573ab9b5d399d656d11eb9fe8557/app/src/main/java/com/fongmi/android/tv/ui/custom/ParseWebView.java) | 加载网页、监听资源请求、判断媒体 URL，保留 UA、Cookie 和请求头，并管理超时、子页面和释放 | 嗅探需要真实页面执行及其会话，不能仅从 HTML 中匹配 `.m3u8` 字符串 |

## 后续实现状态（2026-11-11）

后续已加入网页媒体提取、配置中的网页/JSON 解析、隔离 WKWebView 跨域 iframe 嗅探，以及 XBPQ 文本规则解释器；Playback 已保留后续解析标志。HLS 代理已修复重定向基址和局部响应改写。用户提供配置里的多个 CMS 分集和 IPTV 频道已验证 AVPlayer 连续画面，直播自动备用线路也已实测。XBPQ 已通过本地分类、搜索、分集与取流回归，但抽测的远程规则存在站点失效或页面变化，尚不能将这些站点计为实播通过。专用 Android jar 与 HTTP-FLV 转换仍未实现。

以下断点及方案描述的是此次实现之前的调查结果，保留作技术背景。

## 实现前的断点

1. `cmd/xiaomu/browser.go` 的 `openSite` 在 `Kind.Unsupported()` 为真时立即返回。`internal/config/config.go` 把 `KindCSP` 归入该集合。目前没有尝试执行这些站点。
2. `internal/config/config.go` 只保留部分配置字段，没有将 `parses`、`flags`、`rules` 等解析相关信息传入播放流程。
3. `internal/catalog/catalog.go` 与 `internal/fongmi/fongmi.go` 在播放结果需要解析或嗅探时返回错误。`Playback` 仅表示直接媒体 URL 和请求头，无法表达完整的后续解析任务。
4. `internal/proxy` 能为普通 HTTP 媒体注入请求头并改写 HLS；没有执行爬虫 `proxyLocal` / jar 代理的能力。两种代理的职责不同。
5. `internal/probe` 的 `PASS` 目前表示走完方法调用并获得地址，没有检查 AVPlayer 是否解码、是否出现画面和声音，也没有验证连续分片及拖动播放。

截图中“Android jar 无法运行”和“此分集需要网页解析或嗅探”对应两个不同阶段。补嗅探不会同时解决 jar；执行 jar 成功也不代表 AVPlayer 支持它最终返回的媒体格式。

当前钉扎的 MyGo 版本已有原生界面内嵌 WebView、页面预加载脚本、子框架注入、UA 和 Cookie 会话能力，可作为原生嗅探实现的基础。后续已验证跨域 iframe 的请求捕获及请求头传递；尚未验证与 Android WebView 在所有站点行为上的等价性。

## 实现路线比较

| 路线 | 原配置兼容性 | 实际代价与限制 |
| --- | --- | --- |
| 纯 Go/原生补齐 JS、CMS、解析与嗅探 | 可以改善这些协议覆盖的站点 | Android jar 仍需另行解决；逐站改写无法跟随第三方 jar 的全部更新 |
| DEX 转换后在 JVM 上运行并补 Android API | 部分依赖简单的爬虫可能可用 | Android Context、WebView、JNI、本地库和动态加载都可能继续缺失；不能预先承诺通用兼容 |
| 本机 Android 运行环境承载爬虫与解析，MyGo 承载界面 | 最接近 Android 原有执行条件，建议优先做可行性实测 | 需要系统镜像、可用的 WebView、宿主接口及进程通信；包体、资源占用和兼容性待测。此 APK 只有 arm64 本地库，不能据此宣称支持 Intel Mac |

建议的边界是：MyGo 负责用户交互；兼容进程负责爬虫生命周期、解析、网页会话与爬虫代理；媒体地址和所需请求头交给 Mac 本地代理与 AVPlayer。若某个地址只能在 Android 环境访问，或依赖爬虫代理，媒体字节也必须通过兼容进程转发，不能只把 Android 的 `127.0.0.1` 地址交给 Mac。

Android 解出的媒体仍需单独验证 AVPlayer 支持。IJK/FFmpeg 可播并不证明 AVPlayer 可播；出现不支持的封装、编码或传输协议时，应记录实际媒体能力缺口，再决定是否需要额外转换。

## 最小验证与验收顺序

1. 用用户当前可用的同一份配置，选定一个 Android jar 点播站和一个返回网页地址的站点。在 Android 对照端记录首页、分类/搜索、详情、分集和解析结果；地址与结果只留在本机私有目录。
2. 在隔离的 Android 兼容环境运行相同 jar 及其所需宿主接口，走完相同调用。验证动态加载、网络、Cookie、解析器和爬虫代理。这个阶段失败时先定位运行环境缺口，不把测试结果包装成支持。
3. 将解析结果及必要的媒体转发接入小幕现有播放器，验证首帧、声音、连续播放、切集、拖动和取消。点播与直播分别验证。
4. 用配置包含的不同引擎做回归，并记录适用的 Mac 架构、启动耗时和资源占用。达到实播标准后再做自动启动、退出清理、打包及原生嗅探的后续优化。

本机 Android 环境尚未准备，以上端到端验证未执行。无须为本次核查安装或运行该 APK；静态检查也不能替代这些验收步骤。

## 本次验证结果

- `go test ./...`：通过。
- `go vet ./...`：通过。
- 首次检查受沙箱对 Go 构建缓存目录的访问限制；允许在沙箱外运行后，两项检查均成功，组合命令退出码为 0。
- 这些检查验证当前项目的现有自动化测试和静态检查，不代表用户原配置已经在 Mac 上播放成功。
- 本次仅新增此核查文档，保留既有工作区修改和 `mygo.json`。没有修改播放行为、安装运行环境、提交或发布代码。
