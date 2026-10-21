package main

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	createNoWindow   = 0x08000000
	createNewConsole = 0x00000010
	stillActive      = 259
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsHungAppWindow          = user32.NewProc("IsHungAppWindow")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
	procSetProcessDPIAware       = user32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")

	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10) // MB_ICONERROR
}

func setDPIAware() { procSetProcessDPIAware.Call() }

func screenSize() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	h, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int(w), int(h)
}

// ---------- 假死检测 ----------

var (
	enumMu   sync.Mutex
	enumPID  uint32
	enumWins []uintptr
	// 回调只创建一次（syscall.NewCallback 数量有上限）
	enumCallback = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == enumPID {
			if v, _, _ := procIsWindowVisible.Call(hwnd); v != 0 {
				enumWins = append(enumWins, hwnd)
			}
		}
		return 1
	})
)

// windowsHung 返回进程的可见顶层窗口数和其中“未响应”的窗口数
// IsHungAppWindow：窗口 5 秒未处理消息即视为未响应（与任务管理器判断一致）
func windowsHung(pid int) (total, hung int) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumPID = uint32(pid)
	enumWins = enumWins[:0]
	procEnumWindows.Call(enumCallback, 0)
	for _, h := range enumWins {
		total++
		if r, _, _ := procIsHungAppWindow.Call(h); r != 0 {
			hung++
		}
	}
	return
}

// ---------- 进程信息 ----------

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// processMemory 返回工作集与私有提交内存（字节）
func processMemory(pid int) (workingSet, private uint64, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		// Win7 上部分情况下需要完整的 QUERY_INFORMATION 权限
		h, err = windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
		if err != nil {
			return 0, 0, false
		}
	}
	defer windows.CloseHandle(h)
	var c processMemoryCounters
	c.CB = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB))
	if r == 0 {
		return 0, 0, false
	}
	return uint64(c.WorkingSetSize), uint64(c.PagefileUsage), true
}

func memInfoString(pid int) string {
	ws, pv, ok := processMemory(pid)
	if !ok {
		return ""
	}
	return fmt.Sprintf("工作集 %.1f MB，私有 %.1f MB", float64(ws)/1048576, float64(pv)/1048576)
}

func imagePathOf(h windows.Handle) (string, error) {
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ---------- 进程控制 ----------

type proc struct {
	pid       int
	startAt   time.Time // 进程创建时间
	watchFrom time.Time // 开始受监控的时间（宽限期从此算起）
	adopted   bool
	done      chan struct{}
	exitCode  uint32
	hasCode   bool
}

func (p *proc) waitExit(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// buildCommand 返回实际可执行文件和原样拼接的命令行（参数不做二次转义，所见即所得）
func buildCommand(path, args string) (exe, cmdline string) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".bat" || ext == ".cmd" {
		exe = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		inner := syscall.EscapeArg(path)
		if args != "" {
			inner += " " + args
		}
		return exe, `cmd.exe /c "` + inner + `"`
	}
	cmdline = syscall.EscapeArg(path)
	if args != "" {
		cmdline += " " + args
	}
	return path, cmdline
}

func spawn(cfg AppConfig, out *lineRing) (*proc, error) {
	if _, err := os.Stat(cfg.Path); err != nil {
		return nil, fmt.Errorf("程序文件不存在：%s", cfg.Path)
	}
	wd := cfg.EffectiveWorkDir()
	if st, err := os.Stat(wd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("工作目录不存在：%s", wd)
	}
	exe, cmdline := buildCommand(cfg.Path, cfg.Args)
	cmd := exec.Command(exe)
	cmd.Dir = wd
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdline}
	if cfg.Capture {
		cmd.SysProcAttr.HideWindow = true
		cmd.SysProcAttr.CreationFlags = createNoWindow
		out.openLog()
		cmd.Stdout = out
		cmd.Stderr = out
		cmd.WaitDelay = 3 * time.Second // 子进程把管道传给孙进程时避免 Wait 卡住
	} else {
		cmd.SysProcAttr.CreationFlags = createNewConsole
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动失败：%v", err)
	}
	now := time.Now()
	p := &proc{pid: cmd.Process.Pid, startAt: now, watchFrom: now, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		if cmd.ProcessState != nil {
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
				p.exitCode, p.hasCode = ws.ExitCode, true
			}
		}
		out.flush()
		close(p.done)
	}()
	return p, nil
}

// adoptProcess 接管一个已在运行的进程（监控器重启后继续监控上次启动的程序）
func adoptProcess(pid int, wantPath string) (*proc, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		windows.CloseHandle(h)
		return nil, errors.New("进程已退出")
	}
	if img, err := imagePathOf(h); err != nil || !samePath(img, wantPath) {
		windows.CloseHandle(h)
		return nil, errors.New("PID 已被其他程序占用")
	}
	p := &proc{pid: pid, watchFrom: time.Now(), adopted: true, done: make(chan struct{})}
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) == nil {
		p.startAt = time.Unix(0, c.Nanoseconds())
	} else {
		p.startAt = p.watchFrom
	}
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		var code uint32
		if windows.GetExitCodeProcess(h, &code) == nil {
			p.exitCode, p.hasCode = code, true
		}
		windows.CloseHandle(h)
		close(p.done)
	}()
	return p, nil
}

func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// killTree 结束进程及其子进程
func killTree(pid int) {
	taskkill := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
	if err := hiddenCommand(taskkill, "/PID", strconv.Itoa(pid), "/T", "/F").Run(); err == nil {
		return
	}
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

// ---------- 崩溃信息（Windows 事件日志） ----------

type winEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID int `xml:"EventID"`
	} `xml:"System"`
	Data []string `xml:"EventData>Data"`
}

// findCrashEvent 查询最近 5 分钟的 Application Error(1000) / .NET Runtime(1026) 事件
func findCrashEvent(pid int, exeName string) string {
	wevtutil := filepath.Join(os.Getenv("SystemRoot"), "System32", "wevtutil.exe")
	q := "/q:*[System[(EventID=1000 or EventID=1026) and TimeCreated[timediff(@SystemTime) <= 300000]]]"
	raw, err := hiddenCommand(wevtutil, "qe", "Application", q, "/f:xml", "/rd:true", "/c:20").Output()
	if err != nil || len(raw) == 0 {
		return ""
	}
	dec := xml.NewDecoder(strings.NewReader(decodeText(raw)))
	for {
		var ev winEvent
		if err := dec.Decode(&ev); err != nil { // io.EOF 或解析失败
			return ""
		}
		switch ev.System.EventID {
		case 1000:
			// 0 程序名 3 模块名 4 模块版本 6 异常码 7 偏移 8 PID(十六进制)
			if len(ev.Data) < 9 {
				continue
			}
			p, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ev.Data[8])), "0x"), 16, 32)
			if err != nil || int(p) != pid {
				continue
			}
			code := strings.TrimPrefix(strings.ToLower(ev.Data[6]), "0x")
			return fmt.Sprintf("故障模块 %s（%s），异常码 0x%s，偏移 %s", ev.Data[3], ev.Data[4], strings.ToUpper(code), ev.Data[7])
		case 1026:
			if len(ev.Data) == 0 || !strings.Contains(strings.ToLower(ev.Data[0]), strings.ToLower(exeName)) {
				continue
			}
			lines := strings.Split(strings.ReplaceAll(ev.Data[0], "\r", ""), "\n")
			var keep []string
			for _, l := range lines {
				if l = strings.TrimSpace(l); l != "" {
					keep = append(keep, l)
				}
				if len(keep) >= 6 {
					break
				}
			}
			return ".NET 异常：" + strings.Join(keep, " ｜ ")
		}
	}
}

// decodeText 非 UTF-8 内容按 GBK 解码（中文系统控制台默认代码页）
func decodeText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(s)
	}
	return string(b)
}

// ---------- 单实例 / 开机自启 ----------

func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString("Local\\SciterWatchdog_9C1E7A")
	_, err := windows.CreateMutex(nil, false, name)
	return err != windows.ERROR_ALREADY_EXISTS
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "SciterWatchdog"

func getAutorun() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if err != nil {
		return false
	}
	exe, _ := os.Executable()
	if strings.HasPrefix(v, `"`) { // "C:\path\watchdog.exe" --tray
		if i := strings.Index(v[1:], `"`); i >= 0 {
			v = v[1 : i+1]
		}
	}
	return samePath(v, exe)
}

func setAutorun(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// --tray：开机后直接最小化到托盘
	return k.SetStringValue(runValue, `"`+exe+`" --tray`)
}

func openInExplorer(path string) {
	explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	exec.Command(explorer, "/select,", path).Start()
}

// ---------- 主机连接数（GetExtendedTcpTable，Win7 可用） ----------

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet6            = 23
	tcpTableOwnerPID   = 5 // TCP_TABLE_OWNER_PID_ALL
	udpTableOwnerPID   = 1 // UDP_TABLE_OWNER_PID
	tcpRowV4Size       = 24
	tcpRowV6Size       = 56
	tcpRowV6StateOff   = 48
	tcpRowV6PIDOff     = 52
	errInsufficientBuf = 122
)

var tcpStateNames = map[uint32]string{
	1: "CLOSED", 2: "LISTEN", 3: "SYN_SENT", 4: "SYN_RCVD", 5: "ESTABLISHED", 6: "FIN_WAIT1",
	7: "FIN_WAIT2", 8: "CLOSE_WAIT", 9: "CLOSING", 10: "LAST_ACK", 11: "TIME_WAIT", 12: "DELETE_TCB",
}

func extTable(proc *windows.LazyProc, af, class uint32) ([]byte, error) {
	size := uint32(256 << 10)
	for i := 0; i < 6; i++ {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), uintptr(class), 0)
		if r == 0 {
			return buf, nil
		}
		if r != errInsufficientBuf {
			return nil, syscall.Errno(r)
		}
		size += 64 << 10 // 两次调用之间连接数可能继续增长
	}
	return nil, errors.New("连接表缓冲区不足")
}

// tcpRows 返回每条 TCP 连接的状态和所属 PID（IPv4 + IPv6）
func tcpRows(fn func(state uint32, pid uint32)) error {
	parse := func(af uint32, stride, stateOff, pidOff int) error {
		buf, err := extTable(procGetExtendedTcpTable, af, tcpTableOwnerPID)
		if err != nil {
			return err
		}
		n := int(binary.LittleEndian.Uint32(buf))
		for i := 0; i < n; i++ {
			off := 4 + i*stride
			if off+stride > len(buf) {
				break
			}
			row := buf[off : off+stride]
			fn(binary.LittleEndian.Uint32(row[stateOff:]), binary.LittleEndian.Uint32(row[pidOff:]))
		}
		return nil
	}
	if err := parse(windows.AF_INET, tcpRowV4Size, 0, 20); err != nil {
		return err
	}
	parse(afInet6, tcpRowV6Size, tcpRowV6StateOff, tcpRowV6PIDOff) // 未启用 IPv6 时忽略错误
	return nil
}

func udpCount() int {
	total := 0
	for _, af := range []uint32{windows.AF_INET, afInet6} {
		if buf, err := extTable(procGetExtendedUdpTable, af, udpTableOwnerPID); err == nil {
			total += int(binary.LittleEndian.Uint32(buf))
		}
	}
	return total
}

func processName(pid uint32) string {
	if pid == 0 {
		return "System Idle"
	}
	if pid == 4 {
		return "System"
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Sprintf("PID %d", pid)
	}
	defer windows.CloseHandle(h)
	p, err := imagePathOf(h)
	if err != nil {
		return fmt.Sprintf("PID %d", pid)
	}
	return filepath.Base(p)
}

// ---------- 主机 CPU / 内存 ----------

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func ftU64(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

// systemTimes 返回系统累计的空闲 / 总 CPU 时间（100ns），两次取差计算使用率
func systemTimes() (idle, total uint64, ok bool) {
	var i, k, u windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if r == 0 {
		return 0, 0, false
	}
	return ftU64(i), ftU64(k) + ftU64(u), true // 内核时间包含空闲时间
}

// memoryStatus 返回物理内存总量与可用量（字节）
func memoryStatus() (total, avail uint64, ok bool) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return 0, 0, false
	}
	return ms.TotalPhys, ms.AvailPhys, true
}

type procEntry struct {
	PID  uint32
	Name string
}

func listProcesses() []procEntry {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []procEntry
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, procEntry{PID: e.ProcessID, Name: windows.UTF16ToString(e.ExeFile[:])})
	}
	return out
}

func processCPUTime(pid uint32) (uint64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0, false
	}
	return ftU64(k) + ftU64(u), true
}
