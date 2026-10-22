[English](README.en.md) | 中文

# 进程守护 Watchdog

[![build](https://github.com/chenlz22550/watchdog/actions/workflows/build.yml/badge.svg)](https://github.com/chenlz22550/watchdog/actions/workflows/build.yml) [![release](https://img.shields.io/github/v/release/chenlz22550/watchdog)](https://github.com/chenlz22550/watchdog/releases/latest)

Windows 程序守护工具：被监控的程序退出、窗口无响应或所配端口连不上时自动重启，并把原因推送到企业微信机器人。

- **最低支持 Windows 7 / Server 2008 R2**：用 Go 1.20（最后一个支持 Win7 的版本）编译，只链接系统自带的 `msvcrt.dll`，不需要装 VC 运行库，也不需要 KB2999226 补丁。
- **界面**：基于 Sciter。浅色界面，CPU / 内存环形仪表和约 6 分钟走势图刷新；其余元素只在状态变化时变色（顶部雷达同样只随状态变色，不转动）。无边框窗口和自绘标题栏，经典主题下使用同一套界面。
- **两个文件即可运行**：`watchdog.exe` + `sciter.dll`，拷到目录里就能用。发布包里已带对应位数的 `sciter.dll`。

<!--
TODO: 录一段短演示放到 docs/demo.gif（主界面、自动导入、一次重启）。文件放进仓库后再删掉本注释，改成图片：
![演示](docs/demo.gif)
-->

<!--
TODO: 截一张企业微信机器人告警图放到 docs/wecom-alert.png。文件放进仓库后再删掉本注释，改成图片：
![企业微信告警](docs/wecom-alert.png)
-->

![主界面](docs/main.png)

## 和 NSSM、WinSW 的差别

NSSM 和 WinSW 把程序安装成 Windows 服务，在服务会话里启动它。本工具不是服务：它跑在已登录用户的会话里（托盘，开机项写在当前用户的 `Run` 键），不注册 Windows 服务。下表只比较列出的几项，不比较“能否作为服务在登录前启动”。

Supervisor 没有放进表里。它的文档写明不会在任何版本的 Windows 上运行。

表中第三方列依据各自公开文档，没有在本仓库里实测这些程序。写“文档未提供”表示下面列出的文档没有这项，不表示读过全部源码。

| | 本工具 | NSSM（官网 2.24 文档） | WinSW 3 |
| --- | --- | --- | --- |
| Windows 7 / Server 2008 R2 | 支持。Go 1.20，链接 `msvcrt.dll`，不需要 VC 运行库和 KB2999226 | 文档写 Windows 2000 及以后，并点名 Windows 7。Server 2008 R2 落在“2000 及以后”里，页面没有单独点名 | .NET Framework 构建：Windows 7 SP1 和 Server 2008 R2 SP1，且本机要有 .NET Framework 4.6.1 或更高。.NET 7 构建不支持这些系统（文档要求 Windows 10 1607 / Server 2012 R2 起） |
| 进程退出后重启 | 支持。退出后按程序选择退避（1 秒、10 秒、100 秒，最多 3 次）或立即重启 | 支持。进程退出后的默认动作是重启；退出过快时会节流 | 需自行配置 `<onfailure action="restart">`。文档写明只处理非零退出码，未配置时没有这条动作 |
| 窗口假死检测 | 支持。可选。`IsHungAppWindow`，连续达到阈值后结束进程并重启 | 文档未提供。文档中的监控是进程退出 | 文档未提供自动检测。`winsw dev kill` 是手动命令，不是周期检查 |
| 端口检查 | 支持。对配置的地址做 TCP 连接，连续失败达到阈值后重启。依赖地址只告警，不重启程序 | 文档未提供 | 文档未提供 |
| 企业微信推送 | 支持。机器人 Webhook，markdown | 文档未提供。运行情况写入 Windows 事件日志 | 文档未提供。输出写入日志文件，服务侧错误写入 Windows 事件日志 |
| 图形界面 | 有。监控和配置窗口 | 有。安装和编辑服务的界面（2.22 起覆盖各项配置）。不是运行状态面板 | 没有。命令行加 XML 文件 |
| 接管已经在跑的进程 | 支持。导入后直接接管，不重启。勾选了自动启动的程序，下次启动会按保存的 PID 再接管 | 文档未提供。服务启动时由 NSSM 拉起配置的程序 | 文档未提供。服务启动时拉起配置里的 `<executable>` |

依据：

- NSSM：[download](https://nssm.cc/download)（Windows 2000 及以后）、[description](https://nssm.cc/description)（退出后重启、事件日志、GUI）、[usage](https://nssm.cc/usage)（退出动作、节流、I/O）。
- WinSW 3：[支持的系统](https://winsw.github.io/v3/)、[XML 配置](https://winsw.github.io/v3/docs/xml-config-file/)（`onfailure`）、[命令行](https://winsw.github.io/v3/docs/cli-commands/)（`dev kill`）、[故障排除](https://winsw.github.io/v3/docs/troubleshooting/)（Windows 7 需要 .NET Framework 4.6.1+）、[日志](https://winsw.github.io/v3/docs/logging-and-error-reporting/)。
- Supervisor：[平台要求](https://supervisord.org/introduction.html)（不在 Windows 上运行）。

## 功能

### 程序守护
- 可添加任意多个程序（.exe / .bat / .cmd），每个程序单独设置启动参数、工作目录（默认为程序所在目录）、检测间隔和失败阈值。
- 三种异常检测：
  - **进程退出**：记录退出码并翻译成说明，如 `0xC0000005` 访问冲突、`0xC0000409` 栈缓冲区溢出；
  - **窗口假死**：`IsHungAppWindow` 检测窗口无响应，连续达到阈值后结束进程树并重启；
  - **端口无响应**：对配置的地址做 TCP 连接（超时 3 秒），连续 N 次失败后结束进程并重启。
- 每个程序可单独选择重启策略：
  - **退避重启**：依次等待 1 秒、10 秒、100 秒，最多 3 次，仍失败则停止并告警；稳定运行 10 分钟后计数清零。
  - **立即重启**：不限次数，立刻拉起；程序文件无法启动时每 5 秒重试一次。
- 可选“隐藏窗口并捕获输出”：控制台程序的输出写入日志，告警里附带最后 15 行（非 UTF-8 时按 GBK 解码）。

### 自动导入
一键扫描当前登录账户下正在运行的程序，多选导入：
- 读取每个进程的完整命令行和启动时的工作目录，自动填好程序路径、启动参数和工作目录；
- 命令行相同的多进程程序（如 nginx 主进程 + 工作进程）合并为一项；
- 导入后直接接管运行中的进程，不会重启它们；系统目录下的进程自动隐藏，已在监控的程序不会重复导入。

![自动导入](docs/import.png)

### 企业微信推送
- 每次异常、每次重启（成功或失败）都会推送，内容包括：异常类型、退出码含义、运行时长、内存占用、Windows 事件日志中的崩溃模块、最后 15 行输出。
- **依赖端口告警**：每个程序可填写它依赖的目标地址（如数据库 `10.0.0.5:1433`），连不通时推送告警、恢复后推送恢复通知（只通知，不重启程序）。
- 设置里有“测试推送”按钮，用来检查机器人配置。
- 推送串行发送，失败自动重试；遇到企业微信频率限制（45009）会等待后再发。

当前只有企业微信这一种推送。钉钉、飞书和通用 Webhook 还没有做，见文末“计划”。

### 主机监控
- 实时显示 CPU、内存使用率（环形仪表 + 约 6 分钟走势）和 TCP 连接数（按 ESTABLISHED、TIME_WAIT、CLOSE_WAIT 等各状态分别统计）。
- 阈值可配置：CPU / 内存持续超限才告警（默认 90%、持续 30 秒），连接数超限立即告警（默认 10000）。
- 告警附带占用最高 / 连接最多的前 5 个进程；回落后推送恢复通知，持续超限每 30 分钟提醒一次。

### 其他
- **系统托盘**：关闭窗口缩到托盘，监控不中断；托盘图标颜色反映整体状态，异常时弹出气泡提示。
- **开机自启**：写入当前用户的 `Run` 键，自启后以 `--tray` 直接在托盘运行。
- **退出不影响被监控程序**：退出监控器只停止监控。下次启动时，勾选了“自动启动”的程序会按保存的 PID 再接管；该 PID 已退出，或已被其他程序占用，则重新启动。
- **本地日志**（`logs` 目录）：
  - `alerts-日期.log`：每条告警的完整内容和推送结果；
  - `watchdog-日期.log`：全部事件；
  - `<程序ID>.out.log`：捕获到的程序输出。下次打开该日志时，若文件已超过 10 MB，会把当前文件改名为同名 `.1`。

## 使用

1. 从 [Releases](https://github.com/chenlz22550/watchdog/releases/latest) 下载对应位数的 zip（已包含 `sciter.dll`）并解压；也可以自行编译，再按系统位数放入对应的 `sciter.dll`（见下文）。
2. 运行 `watchdog.exe`，在“设置”里填写企业微信机器人 Webhook，点“测试推送”确认能收到。
3. 点“自动导入”选择正在运行的程序，或点“添加程序”手动添加。

配置保存在 exe 同目录的 `config.json`，请不要放在需要管理员权限才能写入的目录。

## 编译

需要 Go **1.20.x**（Go 1.21 起不再支持 Win7）和 mingw-w64。下面以 macOS 交叉编译为例：

```bash
brew install mingw-w64
# 默认使用 gvm 安装的 go1.20.14，也可以用 GO=/path/to/go 指定
./build.sh
# 可选：把默认的机器人地址编译进程序
WEBHOOK='https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...' ./build.sh
```

产物在 `dist/x64` 和 `dist/x86`。可选 `VERSION=v1.2.3` 指定编译进程序的版本号。`build.sh` 会检查 exe 是否误依赖了 UCRT（`api-ms-win-crt-*.dll`），避免在未打补丁的 Win7 上无法启动。

**sciter.dll**：使用 Sciter **4.4.x**（项目基于 4.4.8，脚本为 TIScript，不兼容 Sciter 5 / JS 版本）。可从 [sciter-sdk](https://github.com/c-smile/sciter-sdk) 的 4.4.8.x 版本中取 `bin.win/x64/sciter.dll` 或 `bin.win/x32/sciter.dll`。

### 自动发布

GitHub Actions（`.github/workflows/build.yml`）在 Ubuntu 上用 Go 1.20 + mingw-w64 交叉编译：推送到 `main` 会构建并上传产物，推送 `v*` 标签会自动发布 Release（附 x64 / x86 两个 zip 和 SHA256 校验）。

```bash
git tag v1.0.1 && git push origin v1.0.1
```

### 界面预览（macOS）

`tools/uipreview` 用假数据在 macOS 上加载同一份 `ui/index.html`，改界面时不用每次拷到 Windows：

```bash
go build -o uipreview ./tools/uipreview   # 需要同目录放 libsciter.dylib（Sciter 4.4.x）
./uipreview ui/index.html
```

## 目录结构

| 文件 | 说明 |
| --- | --- |
| `monitor.go` | 守护循环、重启策略、事件与告警日志 |
| `discover_windows.go` | 自动导入：枚举进程、读取 PEB 中的命令行 |
| `hostmon.go` / `connmon.go` | 主机 CPU / 内存 / TCP 连接监控 |
| `notify.go` | 企业微信推送与消息模板 |
| `winapi_windows.go` | 进程启动 / 接管 / 结束、假死检测、崩溃事件等 Windows API |
| `tray_windows.go` | 系统托盘、窗口子类化 |
| `ui/index.html` | 全部界面（Sciter + TIScript） |

## 计划

下面几项还没有做，配置里也没有对应开关：

- 钉钉机器人
- 飞书机器人
- 通用 Webhook（不限定某一家的消息格式）

现在的推送只向企业微信机器人发送 markdown（`msgtype: markdown`）。

## 许可证

[MIT](LICENSE)。Sciter 是第三方引擎，其二进制文件遵循 [Sciter 自身的授权条款](https://sciter.com/prices/)，商用前请自行确认。
