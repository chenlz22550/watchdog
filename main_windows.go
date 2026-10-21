package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/lxn/win"
	"github.com/sciter-sdk/go-sciter"
	"github.com/sciter-sdk/go-sciter/window"
	"golang.org/x/sys/windows"
)

// version 发布时由 build.sh 用 -X main.version 注入
var version = "v1.0.0"

//go:embed ui/index.html
var indexHTML string

func main() {
	exe, _ := os.Executable()
	base := filepath.Dir(exe)

	startHidden := false
	for _, a := range os.Args[1:] {
		if a == "--tray" || a == "-tray" {
			startHidden = true
		}
	}

	if !acquireSingleInstance() {
		showExisting()
		return
	}
	dll := filepath.Join(base, "sciter.dll")
	if _, err := os.Stat(dll); err != nil {
		messageBox("进程守护", "缺少 sciter.dll，请将其放到程序同目录：\n"+base)
		return
	}
	if _, err := windows.LoadDLL(dll); err != nil {
		messageBox("进程守护", "sciter.dll 加载失败（请确认 32/64 位与程序一致）：\n"+err.Error())
		return
	}
	setDPIAware()

	m, err := newManager(base, version)
	if err != nil {
		messageBox("进程守护", "加载配置失败：\n"+err.Error())
		return
	}

	sciter.SetOption(sciter.SCITER_SET_SCRIPT_RUNTIME_FEATURES,
		sciter.ALLOW_FILE_IO|sciter.ALLOW_SOCKET_IO|sciter.ALLOW_EVAL|sciter.ALLOW_SYSINFO)

	// 按系统 DPI 计算窗口尺寸并居中
	hdc := win.GetDC(0)
	dpi := int(win.GetDeviceCaps(hdc, win.LOGPIXELSX))
	win.ReleaseDC(0, hdc)
	if dpi <= 0 {
		dpi = 96
	}
	sw, sh := screenSize()
	ww, wh := 1180*dpi/96, 760*dpi/96
	if ww > sw-40 {
		ww = sw - 40
	}
	if wh > sh-80 {
		wh = sh - 80
	}
	rect := sciter.NewRect((sh-wh)/2, (sw-ww)/2, ww, wh)

	w, err := window.New(sciter.SW_TITLEBAR|sciter.SW_RESIZEABLE|sciter.SW_CONTROLS|sciter.SW_MAIN, rect)
	if err != nil {
		messageBox("进程守护", "创建窗口失败：\n"+err.Error())
		return
	}
	hwnd := win.HWND(unsafe.Pointer(w.GetHwnd()))
	tray := newTray(hwnd, m)
	m.onAlert = tray.Balloon

	bindUI(w, m, tray)
	if err := w.LoadHtml(indexHTML, ""); err != nil {
		messageBox("进程守护", "界面加载失败：\n"+err.Error())
		return
	}
	w.SetTitle("进程守护 Watchdog")

	m.Boot()
	if !startHidden {
		w.Show()
	}
	w.Run()

	tray.Remove()
	m.Shutdown()
}

// showExisting 已有实例运行时，把它的窗口调到前台
func showExisting() {
	title, _ := windows.UTF16PtrFromString("进程守护 Watchdog")
	hwnd := win.FindWindow(nil, title)
	if hwnd == 0 {
		messageBox("进程守护", "进程守护已在运行，请查看系统托盘。")
		return
	}
	win.ShowWindow(hwnd, win.SW_SHOW)
	if win.IsIconic(hwnd) {
		win.ShowWindow(hwnd, win.SW_RESTORE)
	}
	win.SetForegroundWindow(hwnd)
}
