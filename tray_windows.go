package main

import (
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"

	"watchdog/internal/icon"
)

const (
	wmTray       = win.WM_APP + 1
	menuShow     = 1
	menuTestPush = 2
	menuAutorun  = 3
	menuStartAll = 4
	menuQuit     = 9
)

var (
	procAppendMenuW = user32.NewProc("AppendMenuW")
)

// Tray 系统托盘：通过子类化 Sciter 主窗口接收托盘消息，所有 UI 操作都在主线程完成
type Tray struct {
	hwnd           win.HWND
	m              *Manager
	origProc       uintptr
	taskbarCreated uint32
	quitting       bool
	hintShown      bool

	mu          sync.Mutex
	icons       map[icon.State]win.HICON
	state       icon.State
	tip         string
	lastBalloon time.Time
}

var theTray *Tray // 窗口过程回调需要全局访问

func newTray(hwnd win.HWND, m *Manager) *Tray {
	t := &Tray{hwnd: hwnd, m: m, icons: map[icon.State]win.HICON{}, state: icon.Idle}
	small := int(win.GetSystemMetrics(win.SM_CXSMICON))
	if small <= 0 {
		small = 16
	}
	for _, st := range []icon.State{icon.Normal, icon.Alert, icon.Idle} {
		t.icons[st] = makeHIcon(icon.Render(small, st))
	}
	// 窗口图标（任务栏 / 标题栏）
	big := int(win.GetSystemMetrics(win.SM_CXICON))
	if big <= 0 {
		big = 32
	}
	win.SendMessage(hwnd, win.WM_SETICON, 1, uintptr(makeHIcon(icon.Render(big, icon.Idle))))
	win.SendMessage(hwnd, win.WM_SETICON, 0, uintptr(t.icons[icon.Idle]))

	name, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	t.taskbarCreated = win.RegisterWindowMessage(name)

	theTray = t
	t.origProc = win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, syscall.NewCallback(trayWndProc))
	t.add()
	go t.watch()
	return t
}

func (t *Tray) data() *win.NOTIFYICONDATA {
	var nid win.NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = t.hwnd
	nid.UID = 1
	return &nid
}

func (t *Tray) add() {
	t.mu.Lock()
	defer t.mu.Unlock()
	nid := t.data()
	nid.UFlags = win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP
	nid.UCallbackMessage = wmTray
	nid.HIcon = t.icons[t.state]
	copyUTF16(nid.SzTip[:], t.tipText())
	win.Shell_NotifyIcon(win.NIM_ADD, nid)
}

func (t *Tray) tipText() string {
	if t.tip == "" {
		return "进程守护"
	}
	return t.tip
}

func (t *Tray) Remove() {
	win.Shell_NotifyIcon(win.NIM_DELETE, t.data())
}

// update 刷新托盘图标与提示（可在任意协程调用）
func (t *Tray) update(st icon.State, tip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st == t.state && tip == t.tip {
		return
	}
	t.state, t.tip = st, tip
	nid := t.data()
	nid.UFlags = win.NIF_ICON | win.NIF_TIP
	nid.HIcon = t.icons[st]
	copyUTF16(nid.SzTip[:], t.tipText())
	win.Shell_NotifyIcon(win.NIM_MODIFY, nid)
}

// Balloon 托盘气泡提示，10 秒内最多一次，避免刷屏
func (t *Tray) Balloon(title, text string, isErr bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.lastBalloon) < 10*time.Second {
		return
	}
	t.lastBalloon = time.Now()
	nid := t.data()
	nid.UFlags = win.NIF_INFO
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], text)
	nid.DwInfoFlags = win.NIIF_INFO
	if isErr {
		nid.DwInfoFlags = win.NIIF_WARNING
	}
	win.Shell_NotifyIcon(win.NIM_MODIFY, nid)
}

func (t *Tray) watch() {
	for {
		total, running, abnormal := t.m.Counts()
		st := icon.Normal
		switch {
		case abnormal > 0:
			st = icon.Alert
		case running == 0:
			st = icon.Idle
		}
		tip := fmt.Sprintf("进程守护\n监控 %d · 运行 %d · 异常 %d", total, running, abnormal)
		t.update(st, tip)
		time.Sleep(2 * time.Second)
	}
}

func (t *Tray) ShowMain() {
	if win.IsIconic(t.hwnd) {
		win.ShowWindow(t.hwnd, win.SW_RESTORE)
	} else {
		win.ShowWindow(t.hwnd, win.SW_SHOW)
	}
	win.SetForegroundWindow(t.hwnd)
}

func (t *Tray) HideMain() {
	win.ShowWindow(t.hwnd, win.SW_HIDE)
	if !t.hintShown {
		t.hintShown = true
		t.mu.Lock()
		t.lastBalloon = time.Time{}
		t.mu.Unlock()
		t.Balloon("进程守护仍在后台运行", "双击托盘图标打开主界面，右键可退出", false)
	}
}

// Quit 真正退出程序
func (t *Tray) Quit() {
	t.quitting = true
	win.PostMessage(t.hwnd, win.WM_CLOSE, 0, 0)
}

func (t *Tray) showMenu() {
	menu := win.CreatePopupMenu()
	defer win.DestroyMenu(menu)
	appendMenu(menu, 0, menuShow, "显示主界面")
	appendMenu(menu, win.MF_SEPARATOR, 0, "")
	appendMenu(menu, 0, menuStartAll, "启动全部程序")
	appendMenu(menu, 0, menuTestPush, "发送测试推送")
	var autorunFlag uint32
	if getAutorun() {
		autorunFlag = win.MF_CHECKED
	}
	appendMenu(menu, autorunFlag, menuAutorun, "开机自动启动")
	appendMenu(menu, win.MF_SEPARATOR, 0, "")
	appendMenu(menu, 0, menuQuit, "退出进程守护")

	var pt win.POINT
	win.GetCursorPos(&pt)
	win.SetForegroundWindow(t.hwnd)
	cmd := win.TrackPopupMenuEx(menu, win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON, pt.X, pt.Y, t.hwnd, nil)
	win.PostMessage(t.hwnd, win.WM_NULL, 0, 0)

	switch cmd {
	case menuShow:
		t.ShowMain()
	case menuStartAll:
		t.m.StartAll()
	case menuTestPush:
		t.m.TestPush()
	case menuAutorun:
		on := !getAutorun()
		if err := setAutorun(on); err != nil {
			t.m.event("error", "监控器", "设置开机自启失败："+err.Error())
		} else if on {
			t.m.event("ok", "监控器", "已开启开机自启")
		} else {
			t.m.event("info", "监控器", "已关闭开机自启")
		}
	case menuQuit:
		t.Quit()
	}
}

func trayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	t := theTray
	switch uint32(msg) {
	case win.WM_CLOSE:
		if !t.quitting {
			t.HideMain()
			return 0
		}
	case wmTray:
		switch uint32(lParam & 0xFFFF) {
		case win.WM_LBUTTONUP, win.WM_LBUTTONDBLCLK:
			t.ShowMain()
		case win.WM_RBUTTONUP:
			t.showMenu()
		}
		return 0
	case t.taskbarCreated: // 资源管理器重启后重新添加托盘图标
		t.add()
	case win.WM_GETMINMAXINFO:
		// 无边框窗口最大化时限制在工作区内，不遮住任务栏
		r := win.CallWindowProc(t.origProc, win.HWND(hwnd), uint32(msg), wParam, lParam)
		mi := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
		if win.GetMonitorInfo(win.MonitorFromWindow(win.HWND(hwnd), win.MONITOR_DEFAULTTONEAREST), &mi) {
			mmi := (*win.MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMaxPosition = win.POINT{X: mi.RcWork.Left - mi.RcMonitor.Left, Y: mi.RcWork.Top - mi.RcMonitor.Top}
			mmi.PtMaxSize = win.POINT{X: mi.RcWork.Right - mi.RcWork.Left, Y: mi.RcWork.Bottom - mi.RcWork.Top}
			mmi.PtMinTrackSize = win.POINT{X: 960, Y: 600}
		}
		return r
	}
	return win.CallWindowProc(t.origProc, win.HWND(hwnd), uint32(msg), wParam, lParam)
}

func appendMenu(menu win.HMENU, flags uint32, id uintptr, text string) {
	var p *uint16
	if text != "" {
		p, _ = syscall.UTF16PtrFromString(text)
	}
	procAppendMenuW.Call(uintptr(menu), uintptr(flags), id, uintptr(unsafe.Pointer(p)))
}

func copyUTF16(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// makeHIcon 用 32 位 DIB 创建带 alpha 通道的图标（Win7 兼容）
func makeHIcon(img *image.RGBA) win.HICON {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	bi := win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       int32(w),
		BiHeight:      -int32(h), // 自上而下
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
	}
	hdc := win.GetDC(0)
	var bits unsafe.Pointer
	color := win.CreateDIBSection(hdc, &bi, win.DIB_RGB_COLORS, &bits, 0, 0)
	win.ReleaseDC(0, hdc)
	if color == 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < w*h; i++ {
		dst[i*4+0] = img.Pix[i*4+2]
		dst[i*4+1] = img.Pix[i*4+1]
		dst[i*4+2] = img.Pix[i*4+0]
		dst[i*4+3] = img.Pix[i*4+3]
	}
	mask := win.CreateBitmap(int32(w), int32(h), 1, 1, nil)
	ii := win.ICONINFO{FIcon: win.TRUE, HbmMask: mask, HbmColor: color}
	hicon := win.CreateIconIndirect(&ii)
	win.DeleteObject(win.HGDIOBJ(color))
	win.DeleteObject(win.HGDIOBJ(mask))
	return hicon
}
