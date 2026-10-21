package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Candidate 自动导入时可选的运行中程序（命令行相同的多个进程合并为一项）
type Candidate struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	Path      string `json:"path"`
	Args      string `json:"args"`
	CmdLine   string `json:"cmdline"`
	WorkDir   string `json:"workDir"` // 与程序目录相同时为空
	PID       int    `json:"pid"`     // 接管的主进程
	Count     int    `json:"count"`
	Mem       string `json:"mem"`
	Started   string `json:"started"`
	Listen    string `json:"listen"` // 监听的端口
	Monitored bool   `json:"monitored"`
	Partial   bool   `json:"partial"` // 命令行读取失败，仅有程序路径
	Is32      bool   `json:"is32"`

	startAt time.Time
}

var (
	ntdll                            = windows.NewLazySystemDLL("ntdll.dll")
	procNtWow64QueryInformationProc  = ntdll.NewProc("NtWow64QueryInformationProcess64")
	procNtWow64ReadVirtualMemory64   = ntdll.NewProc("NtWow64ReadVirtualMemory64")
	errCmdLineUnavailable            = errors.New("无法读取命令行")
	selfIsWow64, selfIsWow64Resolved bool
)

func currentIsWow64() bool {
	if !selfIsWow64Resolved {
		windows.IsWow64Process(windows.CurrentProcess(), &selfIsWow64)
		selfIsWow64Resolved = true
	}
	return selfIsWow64
}

// discoverApps 枚举与监控器同一账户运行的程序，跳过系统目录下的进程和监控器自身
func discoverApps(existing []*AppConfig) (list []Candidate, hidden int, err error) {
	me, err := currentTokenUser()
	if err != nil {
		return nil, 0, fmt.Errorf("读取当前账户失败：%v", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, 0, err
	}
	defer windows.CloseHandle(snap)

	sysRoot := strings.ToLower(filepath.Clean(os.Getenv("SystemRoot"))) + `\`
	selfExe, _ := os.Executable()
	selfPID := windows.GetCurrentProcessId()
	listen := listenPorts()

	type found struct {
		c      Candidate
		ppid   uint32
		pid    uint32
		wsSize uint64
	}
	groups := map[string][]found{}
	var order []string

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		pid := e.ProcessID
		if pid == 0 || pid == 4 || pid == selfPID {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue // 其他账户或受保护进程
		}
		owner := processOwnedBy(h, me.User.Sid)
		path, perr := imagePathOf(h)
		var c, x, k, u windows.Filetime
		var started time.Time
		if windows.GetProcessTimes(h, &c, &x, &k, &u) == nil {
			started = time.Unix(0, c.Nanoseconds())
		}
		var wow bool
		windows.IsWow64Process(h, &wow)
		windows.CloseHandle(h)
		if !owner || perr != nil || path == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(path), sysRoot) || (selfExe != "" && samePath(path, selfExe)) {
			hidden++
			continue
		}

		cand := Candidate{Path: path, PID: int(pid), Is32: wow, startAt: started}
		if cmd, cwd, err := readProcessParams(pid, wow); err == nil {
			cand.CmdLine = strings.TrimSpace(cmd)
			cand.Args = argsFromCmdLine(cand.CmdLine)
			cwd = strings.TrimRight(cwd, `\`)
			if cwd != "" && !samePath(cwd, filepath.Dir(path)) {
				cand.WorkDir = cwd
			}
		} else {
			cand.Partial = true
			cand.CmdLine = path
		}
		ws, _, _ := processMemory(int(pid))
		key := strings.ToLower(path) + "\x00" + cand.Args
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], found{cand, e.ParentProcessID, pid, ws})
	}

	for i, key := range order {
		g := groups[key]
		in := map[uint32]bool{}
		for _, f := range g {
			in[f.pid] = true
		}
		// 主进程：父进程不在同组内（如 nginx master），多个时取最早启动的
		root := -1
		var mem uint64
		var ports []string
		for j, f := range g {
			mem += f.wsSize
			ports = append(ports, listen[f.pid]...)
			if in[f.ppid] {
				continue
			}
			if root < 0 || f.c.startAt.Before(g[root].c.startAt) {
				root = j
			}
		}
		if root < 0 {
			root = 0
		}
		c := g[root].c
		c.Key = fmt.Sprintf("c%d", i+1)
		c.Count = len(g)
		c.Name = strings.TrimSuffix(filepath.Base(c.Path), filepath.Ext(c.Path))
		c.Desc = fileDescription(c.Path)
		c.Mem = fmt.Sprintf("%.1f MB", float64(mem)/(1<<20))
		if !c.startAt.IsZero() {
			c.Started = c.startAt.Format("01-02 15:04")
		}
		c.Listen = strings.Join(uniqueStrings(ports), ", ")
		for _, a := range existing {
			if samePath(a.Path, c.Path) && strings.TrimSpace(a.Args) == c.Args {
				c.Monitored = true
			}
		}
		list = append(list, c)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Monitored != list[j].Monitored {
			return !list[i].Monitored
		}
		return strings.ToLower(list[i].Path) < strings.ToLower(list[j].Path)
	})
	return list, hidden, nil
}

// currentTokenUser 当前进程的账户 SID。不能用 GetCurrentProcessToken：
// 它返回的伪句柄 Windows 8 才支持，Win7 / 2008 R2 上会报“句柄无效”
func currentTokenUser() (*windows.Tokenuser, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	return tok.GetTokenUser()
}

func processOwnedBy(h windows.Handle, sid *windows.SID) bool {
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) != nil {
		return false
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	return err == nil && windows.EqualSid(u.User.Sid, sid)
}

// argsFromCmdLine 去掉命令行中的程序本身，剩余部分原样作为启动参数
func argsFromCmdLine(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			return strings.TrimSpace(cmd[i+2:])
		}
		return ""
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return strings.TrimSpace(cmd[i+1:])
	}
	return ""
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// listenPorts 返回每个 PID 监听的 IPv4 地址（0.0.0.0 换成 127.0.0.1，可直接作为监控端口）
func listenPorts() map[uint32][]string {
	out := map[uint32][]string{}
	buf, err := extTable(procGetExtendedTcpTable, windows.AF_INET, tcpTableOwnerPID)
	if err != nil {
		return out
	}
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n; i++ {
		off := 4 + i*tcpRowV4Size
		if off+tcpRowV4Size > len(buf) {
			break
		}
		row := buf[off : off+tcpRowV4Size]
		if binary.LittleEndian.Uint32(row) != 2 { // LISTEN
			continue
		}
		ip := fmt.Sprintf("%d.%d.%d.%d", row[4], row[5], row[6], row[7])
		if ip == "0.0.0.0" {
			ip = "127.0.0.1"
		}
		port := int(row[8])<<8 | int(row[9])
		pid := binary.LittleEndian.Uint32(row[20:])
		out[pid] = append(out[pid], fmt.Sprintf("%s:%d", ip, port))
	}
	return out
}

// readProcessParams 从目标进程的 PEB 读取命令行和当前目录
func readProcessParams(pid uint32, targetWow bool) (cmdline, cwd string, err error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(h)

	ptr64 := unsafe.Sizeof(uintptr(0)) == 8
	var r memReader
	switch {
	case ptr64:
		r = nativeReader{h, true}
	case currentIsWow64() && !targetWow:
		// 32 位监控器读取 64 位进程，需要走 WOW64 专用接口
		r = wow64Reader{h}
	default:
		r = nativeReader{h, false}
	}
	return readParams(r)
}

// memReader 按目标进程的指针宽度解析 PEB → RTL_USER_PROCESS_PARAMETERS
type memReader interface {
	peb() (uint64, error)
	read(addr uint64, n int) ([]byte, error)
	is64() bool
}

type nativeReader struct {
	h   windows.Handle
	x64 bool
}

func (r nativeReader) is64() bool { return r.x64 }

func (r nativeReader) peb() (uint64, error) {
	var pbi windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(r.h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), nil); err != nil {
		return 0, err
	}
	return uint64(uintptr(unsafe.Pointer(pbi.PebBaseAddress))), nil
}

func (r nativeReader) read(addr uint64, n int) ([]byte, error) {
	buf := make([]byte, n)
	var got uintptr
	if err := windows.ReadProcessMemory(r.h, uintptr(addr), &buf[0], uintptr(n), &got); err != nil {
		return nil, err
	}
	return buf, nil
}

type wow64Reader struct{ h windows.Handle }

func (wow64Reader) is64() bool { return true }

func (r wow64Reader) peb() (uint64, error) {
	if procNtWow64QueryInformationProc.Find() != nil {
		return 0, errCmdLineUnavailable
	}
	var pbi [48]byte // PROCESS_BASIC_INFORMATION64
	st, _, _ := procNtWow64QueryInformationProc.Call(uintptr(r.h), 0, uintptr(unsafe.Pointer(&pbi[0])), uintptr(len(pbi)), 0)
	if st != 0 {
		return 0, fmt.Errorf("NTSTATUS 0x%08X", st)
	}
	return binary.LittleEndian.Uint64(pbi[8:]), nil
}

func (r wow64Reader) read(addr uint64, n int) ([]byte, error) {
	if procNtWow64ReadVirtualMemory64.Find() != nil {
		return nil, errCmdLineUnavailable
	}
	buf := make([]byte, n)
	var got uint64
	// 32 位调用约定下 64 位参数拆成低、高两个 32 位
	st, _, _ := procNtWow64ReadVirtualMemory64.Call(uintptr(r.h), uintptr(addr), uintptr(addr>>32),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n), 0, uintptr(unsafe.Pointer(&got)))
	if st != 0 {
		return nil, fmt.Errorf("NTSTATUS 0x%08X", st)
	}
	return buf, nil
}

func ptrAt(b []byte, off int, x64 bool) uint64 {
	if x64 {
		return binary.LittleEndian.Uint64(b[off:])
	}
	return uint64(binary.LittleEndian.Uint32(b[off:]))
}

func readParams(r memReader) (cmdline, cwd string, err error) {
	peb, err := r.peb()
	if err != nil || peb == 0 {
		return "", "", errCmdLineUnavailable
	}
	x64 := r.is64()
	// PEB.ProcessParameters；参数块中 CurrentDirectory.DosPath 与 CommandLine 的偏移
	paramsOff, cwdOff, cmdOff, strSize := 0x10, 0x24, 0x40, 8
	if x64 {
		paramsOff, cwdOff, cmdOff, strSize = 0x20, 0x38, 0x70, 16
	}
	b, err := r.read(peb, paramsOff+strSize)
	if err != nil {
		return "", "", err
	}
	params := ptrAt(b, paramsOff, x64)
	if params == 0 {
		return "", "", errCmdLineUnavailable
	}
	blk, err := r.read(params, cmdOff+strSize)
	if err != nil {
		return "", "", err
	}
	str := func(off int) string {
		n := int(binary.LittleEndian.Uint16(blk[off:]))
		bufOff := 4
		if x64 {
			bufOff = 8
		}
		addr := ptrAt(blk, off+bufOff, x64)
		if n == 0 || addr == 0 || n > 64<<10 {
			return ""
		}
		raw, err := r.read(addr, n)
		if err != nil {
			return ""
		}
		u := make([]uint16, n/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(raw[i*2:])
		}
		return string(utf16.Decode(u))
	}
	cmdline = str(cmdOff)
	if cmdline == "" {
		return "", "", errCmdLineUnavailable
	}
	return cmdline, str(cwdOff), nil
}

// fileDescription 读取 exe 版本信息中的“文件说明”
func fileDescription(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])) != nil {
		return ""
	}
	var p unsafe.Pointer
	var n uint32
	langs := []string{"080404b0", "040904b0", "000004b0", "040904e4"}
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&p), &n) == nil && n >= 4 {
		t := unsafe.Slice((*uint16)(p), 2)
		langs = append([]string{fmt.Sprintf("%04x%04x", t[0], t[1])}, langs...)
	}
	for _, l := range langs {
		if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\`+l+`\FileDescription`, unsafe.Pointer(&p), &n) == nil && n > 1 {
			return strings.TrimSpace(windows.UTF16PtrToString((*uint16)(p)))
		}
	}
	return ""
}

// currentUserName 当前进程所属账户名（如 Administrator）
func currentUserName() string {
	u, err := currentTokenUser()
	if err != nil {
		return ""
	}
	name, _, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return ""
	}
	return name
}

// ScanApps 扫描可导入的程序，结果缓存供 ImportApps 按 key 取用
func (m *Manager) ScanApps() ([]Candidate, int, error) {
	m.mu.Lock()
	existing := append([]*AppConfig(nil), m.cfg.Apps...)
	m.mu.Unlock()
	list, hidden, err := discoverApps(existing)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	m.scanned = map[string]Candidate{}
	for _, c := range list {
		m.scanned[c.Key] = c
	}
	m.mu.Unlock()
	return list, hidden, nil
}

// ImportApps 把选中的运行中程序加入监控，并直接接管现有进程（不会重启它们）
func (m *Manager) ImportApps(keys []string, mode string, withPorts bool) (int, []string) {
	var ok int
	var errs []string
	for _, k := range keys {
		m.mu.Lock()
		c, found := m.scanned[k]
		m.mu.Unlock()
		if !found {
			continue
		}
		a := AppConfig{
			Name: c.Name, Path: c.Path, Args: c.Args, WorkDir: c.WorkDir,
			CheckHang: true, AutoStart: true, RestartMode: mode,
		}
		if withPorts {
			a.Ports = c.Listen
		}
		id, _, err := m.SaveApp(a)
		if err != nil {
			errs = append(errs, c.Name+"："+err.Error())
			continue
		}
		ok++
		m.savePID(id, c.PID)
		if s := m.sup(id); s != nil {
			s.Start(c.PID)
		}
		m.event("ok", c.Name, fmt.Sprintf("已自动导入并接管 PID %d", c.PID))
	}
	return ok, errs
}
