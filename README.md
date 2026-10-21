# 进程守护 Watchdog

[![build](https://github.com/chenlz22550/watchdog/actions/workflows/build.yml/badge.svg)](https://github.com/chenlz22550/watchdog/actions/workflows/build.yml) [![release](https://img.shields.io/github/v/release/chenlz22550/watchdog)](https://github.com/chenlz22550/watchdog/releases/latest)

Windows 程序守护工具：被监控的程序**退出、假死或端口无响应**时自动重启，并把死亡原因推送到**企业微信机器人**。

- **最低支持 Windows 7 / Server 2008 R2**：用 Go 1.20（最后一个支持 Win7 的版本）编译，只链接系统自带的 `msvcrt.dll`，不需要装 VC 运行库，也不需要 KB2999226 补丁。
- **界面精美**：基于 Sciter 引擎，浅色科幻风格，CPU / 内存环形仪表和走势图实时刷新，其余元素只在状态变化时变色、不做多余动画；无边框窗口和自绘标题栏，在经典主题的老系统上也一样好看。
- **单文件绿色运行**：`watchdog.exe` + `sciter.dll` 两个文件，约 6 MB 加 Sciter 运行库，拷过去就能用。

![主界面](docs/main.png)

## 功能

### 程序守护
- 可添加任意多个程序（.exe / .bat / .cmd），每个程序单独设置启动参数、工作目录（默认为程序所在目录）、检测间隔和失败阈值。
- 三种异常检测：
  - **进程退出**：记录退出码并翻译成人话，如 `0xC0000005` 访问冲突、`0xC0000409` 栈缓冲区溢出；
  - **窗口假死**：`IsHungAppWindow` 检测窗口无响应；
  - **端口无响应**：程序自己监听的端口连续 N 次连不上。
- 每个程序可单独选择重启策略：
  - **退避重启**：依次等待 1 秒、10 秒、100 秒，最多 3 次，仍失败则停止并告警；稳定运行 10 分钟后计数清零。
  - **立即重启**：不限次数，立刻拉起；程序文件无法启动时每 5 秒重试一次。
- 可选"隐藏窗口并捕获输出"：控制台程序的输出写入日志，崩溃通知里附带最后几十行输出（自动识别 GBK / UTF-8）。

### 自动导入
一键扫描当前登录账户下正在运行的程序，多选导入：
- 读取每个进程的**完整命令行**和启动时的工作目录，自动填好程序路径、启动参数和工作目录；
- 命令行相同的多进程程序（如 nginx 主进程 + 工作进程）合并为一项；
- 导入后**直接接管运行中的进程，不会重启它们**；系统目录下的进程自动隐藏，已在监控的程序不会重复导入。

![自动导入](docs/import.png)

### 企业微信推送
- 每次异常、每次重启（成功或失败）都会推送，内容包括：异常类型、退出码含义、运行时长、内存占用、Windows 事件日志中的崩溃模块、最后输出。
- **依赖端口告警**：每个程序可填写它依赖的目标地址（如数据库 `10.0.0.5:1433`），连不通时推送告警、恢复后推送恢复通知（只通知，不重启程序）。
- 设置里有"测试推送"按钮，一键验证机器人配置。
- 推送串行发送，失败自动重试；遇到企业微信频率限制（45009）会等待后再发。

### 主机监控
- 实时显示 CPU、内存使用率（环形仪表 + 6 分钟走势）和 TCP 连接数（按 ESTABLISHED、TIME_WAIT、CLOSE_WAIT 等各状态分别统计）。
- 阈值可配置：CPU / 内存持续超限才告警（默认 90%、持续 30 秒），连接数超限立即告警（默认 10000）。
- 告警附带占用最高 / 连接最多的前 5 个进程；回落后推送恢复通知，持续超限每 30 分钟提醒一次。

### 其他
- **系统托盘**：关闭窗口缩到托盘，监控不中断；托盘图标颜色反映整体状态，异常时弹出气泡提示。
- **开机自启**：自启后直接在托盘运行。
- **退出不影响被监控程序**：退出监控器只停止监控，下次启动会自动接管上次的进程。
- **本地日志**（`logs` 目录）：
  - `alerts-日期.log`：每条告警的完整内容和推送结果；
  - `watchdog-日期.log`：全部事件；
  - `<程序ID>.out.log`：捕获到的程序输出，10 MB 自动轮转。

## 使用

1. 从 [Releases](https://github.com/chenlz22550/watchdog/releases/latest) 下载对应位数的 zip（已包含 `sciter.dll`）并解压；也可以自行编译，再按系统位数放入对应的 `sciter.dll`（见下文）。
2. 运行 `watchdog.exe`，在"设置"里填写企业微信机器人 Webhook，点"测试推送"确认能收到。
3. 点"自动导入"选择正在运行的程序，或点"添加程序"手动添加。

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

## 许可证

[MIT](LICENSE)。Sciter 是第三方引擎，其二进制文件遵循 [Sciter 自身的授权条款](https://sciter.com/prices/)，商用前请自行确认。
