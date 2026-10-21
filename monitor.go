package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 退避重启：依次等待 1s、10s、100s，最多 3 次
var restartDelays = []time.Duration{1 * time.Second, 10 * time.Second, 100 * time.Second}

// 立即重启模式下，程序无法启动时的重试间隔
const immediateRetryDelay = 5 * time.Second

// 程序稳定运行超过该时长后，重启计数清零
const stableReset = 10 * time.Minute

const (
	StStopped  = "stopped"
	StStarting = "starting"
	StRunning  = "running"
	StWarning  = "warning" // 运行中但检测到异常（未达阈值）
	StBackoff  = "backoff"
	StFailed   = "failed"
)

const (
	KindExit      = "进程退出"
	KindHang      = "程序假死"
	KindPort      = "端口无响应"
	KindStartFail = "启动失败"
)

// Death 一次异常的现场信息
type Death struct {
	Kind      string
	Detail    string
	At        time.Time
	PID       int
	StartAt   time.Time
	ExitCode  uint32
	HasCode   bool
	MemInfo   string
	Crash     string
	Output    []string
	RestartNo int // >0 表示这是第 N 次重启的启动失败
}

type portResult struct {
	Addr string `json:"addr"`
	OK   bool   `json:"ok"`
	Err  string `json:"err"`
}

// Supervisor 守护单个程序
type Supervisor struct {
	m  *Manager
	id string

	mu       sync.Mutex
	cfg      AppConfig
	status   string
	note     string // 状态补充说明
	proc     *proc
	attempt  int // 本轮已执行的重启次数
	restarts int // 累计重启次数
	nextAt   time.Time
	nextDur  time.Duration
	ports    []portResult
	deps     []portResult
	depFail  int
	depDown  bool // 依赖不通告警已发出，等待恢复
	hung     bool
	active   bool
	stopping bool
	kill     bool // 停止时是否结束进程
	stopCh   chan struct{}
	doneCh   chan struct{}
	out      *lineRing
}

func newSupervisor(m *Manager, cfg AppConfig) *Supervisor {
	return &Supervisor{
		m:      m,
		id:     cfg.ID,
		cfg:    cfg,
		status: StStopped,
		out:    newLineRing(40, filepath.Join(m.logDir, cfg.ID+".out.log")),
	}
}

func (s *Supervisor) config() AppConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Supervisor) setConfig(c AppConfig) {
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
}

func (s *Supervisor) set(status, note string) {
	s.mu.Lock()
	s.status, s.note = status, note
	if status != StRunning && status != StWarning {
		s.ports, s.hung = nil, false
	}
	if status == StStopped || status == StFailed {
		s.deps = nil
	}
	s.mu.Unlock()
}

func (s *Supervisor) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Start 启动守护；adoptPID>0 时优先接管该进程
func (s *Supervisor) Start(adoptPID int) {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active, s.stopping, s.kill = true, false, true
	s.attempt = 0
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.mu.Unlock()
	go s.loop(adoptPID)
}

// Stop 停止守护并等待完成；kill=false 时只停止监控，不结束进程
func (s *Supervisor) Stop(kill bool) {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	if !s.stopping {
		s.stopping, s.kill = true, kill
		close(s.stopCh)
	}
	done := s.doneCh
	s.mu.Unlock()
	<-done
}

func (s *Supervisor) finish() {
	s.mu.Lock()
	s.active, s.stopping, s.proc = false, false, nil
	done := s.doneCh
	s.mu.Unlock()
	close(done)
}

func (s *Supervisor) loop(adoptPID int) {
	defer s.finish()
	first := true
	for {
		cfg := s.config()
		restartNo := 0
		if !first {
			s.mu.Lock()
			restartNo = s.attempt
			s.mu.Unlock()
		}

		var p *proc
		var err error
		if first && adoptPID > 0 {
			if p, err = adoptProcess(adoptPID, cfg.Path); err == nil {
				s.m.event("info", cfg.Name, fmt.Sprintf("已接管运行中的进程 PID %d", p.pid))
			}
		}
		first = false
		if p == nil {
			s.set(StStarting, "正在启动")
			s.out.reset()
			p, err = spawn(cfg, s.out)
		}

		var d *Death
		if err != nil {
			d = &Death{Kind: KindStartFail, Detail: err.Error(), At: time.Now(), RestartNo: restartNo}
			s.m.event("error", cfg.Name, err.Error())
		} else {
			s.mu.Lock()
			s.proc = p
			s.mu.Unlock()
			s.set(StRunning, "")
			s.m.savePID(s.id, p.pid)
			if restartNo > 0 {
				s.m.event("ok", cfg.Name, fmt.Sprintf("第 %d 次重启成功，PID %d", restartNo, p.pid))
				s.m.push(cfg.Name+" 重启成功", buildRestartOKMsg(s.m.host(), cfg, restartNo, p.pid))
			} else if !p.adopted {
				s.m.event("ok", cfg.Name, fmt.Sprintf("已启动，PID %d", p.pid))
			}

			var stopped bool
			d, stopped = s.watch(p)
			s.mu.Lock()
			s.proc = nil
			s.mu.Unlock()
			if stopped {
				return
			}
			s.m.savePID(s.id, 0)
			if d.At.Sub(p.startAt) >= stableReset {
				s.mu.Lock()
				s.attempt = 0
				s.mu.Unlock()
			}
			s.m.event("error", cfg.Name, d.Kind+"："+d.Detail)
		}

		cfg = s.config()
		s.mu.Lock()
		attempt := s.attempt
		s.mu.Unlock()
		immediate := cfg.RestartMode == ModeImmediate
		if !immediate && attempt >= len(restartDelays) {
			plan := fmt.Sprintf("已连续重启 %d 次仍失败，停止自动重启，请人工处理", len(restartDelays))
			s.m.push(cfg.Name+" 已放弃重启", buildDeathMsg(s.m.host(), cfg, d, plan, true))
			s.m.event("error", cfg.Name, plan)
			s.m.balloon("进程守护："+cfg.Name, plan, true)
			s.set(StFailed, "已放弃自动重启")
			return
		}
		var delay time.Duration
		var plan string
		if immediate {
			// 立即重启不限次数；启动失败（如文件被删）时间隔 5 秒，避免空转刷屏
			if d.Kind == KindStartFail {
				delay = immediateRetryDelay
			}
			attempt++
			if delay > 0 {
				plan = fmt.Sprintf("立即重启模式：%s后进行第 %d 次重启", fmtDuration(delay), attempt)
			} else {
				plan = fmt.Sprintf("立即重启模式：正在进行第 %d 次重启", attempt)
			}
		} else {
			delay = restartDelays[attempt]
			attempt++
			plan = fmt.Sprintf("将在%s后进行第 %d/%d 次重启", fmtDuration(delay), attempt, len(restartDelays))
		}
		s.mu.Lock()
		s.attempt = attempt
		s.restarts++
		s.nextAt = time.Now().Add(delay)
		s.nextDur = delay
		s.mu.Unlock()
		s.m.push(cfg.Name+" "+d.Kind, buildDeathMsg(s.m.host(), cfg, d, plan, false))
		s.m.balloon("进程守护："+cfg.Name, d.Kind+"，"+plan, true)
		s.set(StBackoff, plan)

		select {
		case <-time.After(delay):
		case <-s.stopCh:
			s.set(StStopped, "")
			return
		}
	}
}

// watch 监控进程直到其死亡或收到停止信号
func (s *Supervisor) watch(p *proc) (*Death, bool) {
	cfg := s.config()
	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()
	hangCnt, portCnt := 0, 0

	for {
		select {
		case <-s.stopCh:
			s.mu.Lock()
			kill := s.kill
			s.mu.Unlock()
			if kill {
				s.set(StStarting, "正在停止")
				killTree(p.pid)
				p.waitExit(10 * time.Second)
				s.m.savePID(s.id, 0)
				s.m.event("info", cfg.Name, "已停止")
			}
			s.set(StStopped, "")
			return nil, true

		case <-p.done:
			d := s.newDeath(p, KindExit)
			d.ExitCode, d.HasCode = p.exitCode, p.hasCode
			if p.hasCode {
				d.Detail = "进程已退出，退出码 " + describeExitCode(p.exitCode)
				if p.exitCode != 0 {
					time.Sleep(2 * time.Second) // 等 WER 写入事件日志
					d.Crash = findCrashEvent(p.pid, filepath.Base(cfg.Path))
				}
			} else {
				d.Detail = "进程已退出"
			}
			return d, false

		case <-ticker.C:
			cfg = s.config() // 检测参数修改后即时生效
			s.checkDeps(cfg) // 依赖端口与程序启动无关，不受宽限期限制
			if time.Since(p.watchFrom) < time.Duration(cfg.Grace)*time.Second {
				continue
			}
			var problems []string

			if cfg.CheckHang {
				total, hung := windowsHung(p.pid)
				s.mu.Lock()
				s.hung = hung > 0
				s.mu.Unlock()
				if hung > 0 {
					hangCnt++
					problems = append(problems, fmt.Sprintf("窗口未响应（%d/%d）", hangCnt, cfg.Threshold))
					if hangCnt >= cfg.Threshold {
						d := s.newDeath(p, KindHang)
						d.Detail = fmt.Sprintf("%d 个窗口中有 %d 个未响应，已连续 %d 次检测（间隔 %d 秒）", total, hung, hangCnt, cfg.Interval)
						d.MemInfo = memInfoString(p.pid)
						s.killForRestart(p, cfg.Name, d.Kind)
						return d, false
					}
				} else {
					hangCnt = 0
				}
			}

			if addrs, _ := parsePorts(cfg.Ports); len(addrs) > 0 {
				res := checkPorts(addrs)
				s.mu.Lock()
				s.ports = res
				s.mu.Unlock()
				var bad []string
				for _, r := range res {
					if !r.OK {
						bad = append(bad, r.Addr+"（"+r.Err+"）")
					}
				}
				if len(bad) > 0 {
					portCnt++
					problems = append(problems, fmt.Sprintf("端口无响应（%d/%d）", portCnt, cfg.Threshold))
					if portCnt >= cfg.Threshold {
						d := s.newDeath(p, KindPort)
						d.Detail = fmt.Sprintf("%s，已连续 %d 次检测失败", strings.Join(bad, "；"), portCnt)
						d.MemInfo = memInfoString(p.pid)
						s.killForRestart(p, cfg.Name, d.Kind)
						return d, false
					}
				} else {
					portCnt = 0
				}
			}

			if len(problems) > 0 {
				s.set(StWarning, strings.Join(problems, "，"))
			} else {
				s.set(StRunning, "")
			}
		}
	}
}

// checkDeps 检测依赖的目标端口：连续失败达到阈值推送告警，全部恢复后推送恢复通知
func (s *Supervisor) checkDeps(cfg AppConfig) {
	addrs, _ := parsePorts(cfg.Deps)
	if len(addrs) == 0 {
		s.mu.Lock()
		s.deps, s.depFail = nil, 0
		s.mu.Unlock()
		return
	}
	res := checkPorts(addrs)
	var bad []string
	for _, r := range res {
		if !r.OK {
			bad = append(bad, r.Addr+"（"+r.Err+"）")
		}
	}
	s.mu.Lock()
	s.deps = res
	if len(bad) > 0 {
		s.depFail++
	} else {
		s.depFail = 0
	}
	fire := len(bad) > 0 && !s.depDown && s.depFail >= cfg.Threshold
	recovered := len(bad) == 0 && s.depDown
	if fire {
		s.depDown = true
	}
	if recovered {
		s.depDown = false
	}
	s.mu.Unlock()

	if fire {
		s.m.event("warn", cfg.Name, "依赖端口不通："+strings.Join(bad, "；"))
		s.m.balloon("进程守护："+cfg.Name, "依赖端口不通："+strings.Join(bad, "；"), true)
		s.m.push(cfg.Name+" 依赖端口不通", buildDepMsg(s.m.host(), cfg, bad, false))
	}
	if recovered {
		s.m.event("ok", cfg.Name, "依赖端口已恢复")
		s.m.push(cfg.Name+" 依赖恢复", buildDepMsg(s.m.host(), cfg, nil, true))
	}
}

func (s *Supervisor) newDeath(p *proc, kind string) *Death {
	return &Death{Kind: kind, At: time.Now(), PID: p.pid, StartAt: p.startAt, Output: s.out.tail(15)}
}

func (s *Supervisor) killForRestart(p *proc, name, why string) {
	s.m.event("warn", name, fmt.Sprintf("%s，结束进程 PID %d", why, p.pid))
	killTree(p.pid)
	p.waitExit(10 * time.Second)
}

func checkPorts(addrs []string) []portResult {
	res := make([]portResult, len(addrs))
	var wg sync.WaitGroup
	for i, a := range addrs {
		wg.Add(1)
		go func(i int, a string) {
			defer wg.Done()
			res[i].Addr = a
			c, err := net.DialTimeout("tcp", a, 3*time.Second)
			if err != nil {
				res[i].Err = shortNetErr(err)
				return
			}
			c.Close()
			res[i].OK = true
		}(i, a)
	}
	wg.Wait()
	return res
}

func shortNetErr(err error) string {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "连接超时"
	}
	msg := err.Error()
	if strings.Contains(msg, "refused") || strings.Contains(msg, "10061") {
		return "连接被拒绝"
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}

// ---------- Manager ----------

type Event struct {
	Seq   int    `json:"seq"`
	Time  string `json:"time"`
	Level string `json:"level"` // info / ok / warn / error
	App   string `json:"app"`
	Text  string `json:"text"`
}

type Manager struct {
	baseDir string
	logDir  string
	version string

	mu     sync.Mutex
	cfg    *Config
	sups   map[string]*Supervisor
	pids   map[string]int
	events []Event
	seq    int

	hostStats HostStats
	scanned   map[string]Candidate // 自动导入最近一次扫描结果

	notifier *Notifier
	logMu    sync.Mutex
	onAlert  func(title, text string, isErr bool) // 托盘气泡
}

func newManager(baseDir, version string) (*Manager, error) {
	m := &Manager{
		baseDir: baseDir,
		logDir:  filepath.Join(baseDir, "logs"),
		version: version,
		sups:    map[string]*Supervisor{},
		pids:    map[string]int{},
	}
	os.MkdirAll(m.logDir, 0755)
	cfg, err := loadConfig(filepath.Join(baseDir, "config.json"))
	if err != nil {
		return nil, err
	}
	m.cfg = cfg
	if data, err := os.ReadFile(filepath.Join(baseDir, "state.json")); err == nil {
		json.Unmarshal(data, &m.pids)
	}
	for _, a := range cfg.Apps {
		m.sups[a.ID] = newSupervisor(m, *a)
	}
	m.notifier = newNotifier(m.webhook, func(title string, err error) {
		if err != nil {
			m.event("error", "企业微信", "推送失败（"+title+"）："+err.Error())
			m.alertLog(title, "【推送结果】失败："+err.Error())
		} else {
			m.event("info", "企业微信", "已推送："+title)
			m.alertLog(title, "【推送结果】成功")
		}
	})
	return m, nil
}

func (m *Manager) webhook() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Webhook
}

func (m *Manager) host() string {
	m.mu.Lock()
	name := m.cfg.Machine
	m.mu.Unlock()
	if name == "" {
		name, _ = os.Hostname()
	}
	if ip := localIP(); ip != "" {
		name += "（" + ip + "）"
	}
	return name
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	return ""
}

// push 推送企业微信，同时把完整告警内容写入本地告警日志
func (m *Manager) push(title, content string) {
	m.alertLog(title, "【告警内容】\r\n"+plainText(content))
	m.notifier.Send(title, content)
}

// alertLog 追加写入 logs/alerts-日期.log
func (m *Manager) alertLog(title, body string) {
	now := time.Now()
	m.logMu.Lock()
	defer m.logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(m.logDir, "alerts-"+now.Format("20060102")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "==== %s  %s ====\r\n%s\r\n\r\n", now.Format("2006-01-02 15:04:05"), title, strings.ReplaceAll(body, "\n", "\r\n"))
}

// plainText 去掉企业微信 markdown 标记，便于在日志中阅读
func plainText(md string) string {
	md = fontTag.ReplaceAllString(md, "")
	var out []string
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimPrefix(l, "### ")
		l = strings.TrimPrefix(l, "> ")
		l = strings.ReplaceAll(l, "**", "")
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

var fontTag = regexp.MustCompile(`</?font[^>]*>`)

func (m *Manager) balloon(title, text string, isErr bool) {
	if m.onAlert != nil {
		m.onAlert(title, text, isErr)
	}
}

func (m *Manager) event(level, app, text string) {
	m.mu.Lock()
	m.seq++
	now := time.Now()
	ev := Event{Seq: m.seq, Time: now.Format("01-02 15:04:05"), Level: level, App: app, Text: text}
	m.events = append(m.events, ev)
	if len(m.events) > 300 {
		m.events = append(m.events[:0:0], m.events[len(m.events)-300:]...)
	}
	m.mu.Unlock()

	m.logMu.Lock()
	defer m.logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(m.logDir, "watchdog-"+now.Format("20060102")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		fmt.Fprintf(f, "%s [%s] %s %s\r\n", now.Format("2006-01-02 15:04:05"), level, app, text)
		f.Close()
	}
}

func (m *Manager) saveConfigLocked() error {
	return writeJSONFile(filepath.Join(m.baseDir, "config.json"), m.cfg)
}

func (m *Manager) savePID(id string, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pid == 0 {
		delete(m.pids, id)
	} else {
		m.pids[id] = pid
	}
	writeJSONFile(filepath.Join(m.baseDir, "state.json"), m.pids)
}

// Boot 启动所有“自动启动”的程序，上次由本监控器启动且仍在运行的进程直接接管
func (m *Manager) Boot() {
	m.event("info", "监控器", "进程守护已启动 "+m.version)
	go m.hostLoop()
	m.mu.Lock()
	var list []*Supervisor
	var adopt []int
	for _, a := range m.cfg.Apps {
		if a.AutoStart {
			list = append(list, m.sups[a.ID])
			adopt = append(adopt, m.pids[a.ID])
		}
	}
	m.mu.Unlock()
	for i, s := range list {
		s.Start(adopt[i])
	}
}

// Shutdown 退出监控器：只停止监控，不结束被监控进程（下次启动时自动接管）
func (m *Manager) Shutdown() {
	m.mu.Lock()
	var names []string
	var sups []*Supervisor
	for _, a := range m.cfg.Apps {
		if s := m.sups[a.ID]; s.isActive() {
			names = append(names, a.Name)
			sups = append(sups, s)
		}
	}
	m.mu.Unlock()
	if len(sups) > 0 {
		m.push("监控器已退出", buildMonitorMsg(m.host(), "已退出，以下程序不再受监控", names))
	}
	var wg sync.WaitGroup
	for _, s := range sups {
		wg.Add(1)
		go func(s *Supervisor) { defer wg.Done(); s.Stop(false) }(s)
	}
	wg.Wait()
	m.event("info", "监控器", "进程守护已退出")
	m.notifier.Flush(8 * time.Second)
}

func (m *Manager) sup(id string) *Supervisor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sups[id]
}

// StartApp 手动启动；若上次启动的进程仍在运行则直接接管，避免重复启动
func (m *Manager) StartApp(id string) {
	if s := m.sup(id); s != nil {
		m.mu.Lock()
		pid := m.pids[id]
		m.mu.Unlock()
		s.Start(pid)
	}
}

func (m *Manager) StopApp(id string) {
	if s := m.sup(id); s != nil {
		go s.Stop(true)
	}
}

func (m *Manager) RestartApp(id string) {
	if s := m.sup(id); s != nil {
		go func() {
			s.Stop(true)
			s.Start(0)
		}()
	}
}

// SaveApp 新增或修改程序配置，返回程序 ID 和提示信息
func (m *Manager) SaveApp(a AppConfig) (string, string, error) {
	a.normalize()
	if err := a.validate(); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		a.ID = fmt.Sprintf("app%x", time.Now().UnixNano())
		c := a
		m.cfg.Apps = append(m.cfg.Apps, &c)
		m.sups[a.ID] = newSupervisor(m, a)
		if err := m.saveConfigLocked(); err != nil {
			return "", "", err
		}
		return a.ID, "已添加 " + a.Name, nil
	}
	for i, old := range m.cfg.Apps {
		if old.ID != a.ID {
			continue
		}
		launchChanged := old.Path != a.Path || old.Args != a.Args || old.WorkDir != a.WorkDir || old.Capture != a.Capture
		c := a
		m.cfg.Apps[i] = &c
		s := m.sups[a.ID]
		s.setConfig(a)
		if err := m.saveConfigLocked(); err != nil {
			return "", "", err
		}
		if launchChanged && s.isActive() {
			return a.ID, "已保存，启动相关配置将在下次重启后生效", nil
		}
		return a.ID, "已保存", nil
	}
	return "", "", fmt.Errorf("程序不存在")
}

// RemoveApp 删除配置并停止监控（不结束进程）
func (m *Manager) RemoveApp(id string) {
	m.mu.Lock()
	s := m.sups[id]
	delete(m.sups, id)
	delete(m.pids, id)
	for i, a := range m.cfg.Apps {
		if a.ID == id {
			m.cfg.Apps = append(m.cfg.Apps[:i], m.cfg.Apps[i+1:]...)
			break
		}
	}
	m.saveConfigLocked()
	writeJSONFile(filepath.Join(m.baseDir, "state.json"), m.pids)
	m.mu.Unlock()
	if s != nil {
		go s.Stop(false)
	}
}

type Settings struct {
	Webhook, Machine                           string
	CPULimit, MemLimit, HostSustain, ConnLimit int
}

func (m *Manager) SaveSettings(st Settings) error {
	if st.CPULimit < 1 || st.CPULimit > 100 || st.MemLimit < 1 || st.MemLimit > 100 {
		return fmt.Errorf("CPU / 内存阈值应在 1～100 之间")
	}
	if st.HostSustain < 0 || st.ConnLimit < 1 {
		return fmt.Errorf("持续时间不能为负，连接数阈值必须大于 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Webhook = strings.TrimSpace(st.Webhook)
	m.cfg.Machine = strings.TrimSpace(st.Machine)
	m.cfg.CPULimit, m.cfg.MemLimit = st.CPULimit, st.MemLimit
	m.cfg.HostSustain, m.cfg.ConnLimit = st.HostSustain, st.ConnLimit
	return m.saveConfigLocked()
}

func (m *Manager) TestPush() {
	m.mu.Lock()
	n := len(m.cfg.Apps)
	m.mu.Unlock()
	m.event("info", "企业微信", "正在发送测试消息…")
	m.push("测试消息", buildTestMsg(m.host(), n, m.version))
}

// ---------- 界面快照 ----------

type AppView struct {
	AppConfig
	Status    string       `json:"status"`
	Note      string       `json:"note"`
	PID       int          `json:"pid"`
	Uptime    string       `json:"uptime"`
	Restarts  int          `json:"restarts"`
	Attempt   int          `json:"attempt"`
	NextIn    int          `json:"nextIn"`
	NextTotal int          `json:"nextTotal"`
	Mem       string       `json:"mem"`
	PortState []portResult `json:"portState"`
	DepState  []portResult `json:"depState"`
	DepDown   bool         `json:"depDown"`
	Hung      bool         `json:"hung"`
	EffDir    string       `json:"effDir"`
	Active    bool         `json:"active"`
}

type Snapshot struct {
	Apps     []AppView `json:"apps"`
	Events   []Event   `json:"events"`
	EventSeq int       `json:"eventSeq"`
	Webhook  string    `json:"webhook"`
	Machine  string    `json:"machine"`
	Hostname string    `json:"hostname"`
	Autorun  bool      `json:"autorun"`
	Version  string    `json:"version"`
	Total    int       `json:"total"`
	Running  int       `json:"running"`
	Abnormal int       `json:"abnormal"`
	Host     HostStats `json:"host"`
	Settings struct {
		CPULimit    int `json:"cpuLimit"`
		MemLimit    int `json:"memLimit"`
		HostSustain int `json:"hostSustain"`
		ConnLimit   int `json:"connLimit"`
	} `json:"settings"`
}

func (m *Manager) Snapshot(sinceSeq int) Snapshot {
	m.mu.Lock()
	snap := Snapshot{Webhook: m.cfg.Webhook, Machine: m.cfg.Machine, Version: m.version, EventSeq: m.seq}
	snap.Host = m.hostStats
	snap.Host.CPUHist = append([]int(nil), m.hostStats.CPUHist...)
	snap.Host.MemHist = append([]int(nil), m.hostStats.MemHist...)
	snap.Host.ConnHist = append([]int(nil), m.hostStats.ConnHist...)
	snap.Settings.CPULimit, snap.Settings.MemLimit = m.cfg.CPULimit, m.cfg.MemLimit
	snap.Settings.HostSustain, snap.Settings.ConnLimit = m.cfg.HostSustain, m.cfg.ConnLimit
	var sups []*Supervisor
	for _, a := range m.cfg.Apps {
		sups = append(sups, m.sups[a.ID])
	}
	if sinceSeq != m.seq {
		for i := len(m.events) - 1; i >= 0 && len(snap.Events) < 150; i-- {
			snap.Events = append(snap.Events, m.events[i])
		}
	}
	m.mu.Unlock()
	snap.Hostname, _ = os.Hostname()

	for _, s := range sups {
		s.mu.Lock()
		v := AppView{
			AppConfig: s.cfg, Status: s.status, Note: s.note, Restarts: s.restarts,
			Attempt: s.attempt, Hung: s.hung, Active: s.active,
			PortState: append([]portResult(nil), s.ports...), EffDir: s.cfg.EffectiveWorkDir(),
			DepState: append([]portResult(nil), s.deps...), DepDown: s.depDown,
		}
		if s.proc != nil {
			v.PID = s.proc.pid
			v.Uptime = fmtDuration(time.Since(s.proc.startAt))
		}
		if s.status == StBackoff {
			v.NextIn = int(time.Until(s.nextAt).Seconds() + 0.999)
			v.NextTotal = int(s.nextDur.Seconds())
		}
		s.mu.Unlock()
		if v.PID > 0 {
			if ws, _, ok := processMemory(v.PID); ok {
				v.Mem = fmt.Sprintf("%.1f MB", float64(ws)/1048576)
			}
		}
		snap.Total++
		switch v.Status {
		case StRunning:
			snap.Running++
		case StWarning:
			snap.Running++
			snap.Abnormal++
		case StBackoff, StFailed:
			snap.Abnormal++
		}
		if v.DepDown && v.Status != StWarning {
			snap.Abnormal++
		}
		snap.Apps = append(snap.Apps, v)
	}
	if snap.Host.CPUOver || snap.Host.MemOver || snap.Host.ConnOver {
		snap.Abnormal++
	}
	return snap
}

func (m *Manager) appPath(id string) string {
	if s := m.sup(id); s != nil {
		return s.config().Path
	}
	return ""
}

// Counts 供托盘使用的轻量统计
func (m *Manager) Counts() (total, running, abnormal int) {
	m.mu.Lock()
	var sups []*Supervisor
	for _, a := range m.cfg.Apps {
		sups = append(sups, m.sups[a.ID])
	}
	m.mu.Unlock()
	for _, s := range sups {
		s.mu.Lock()
		st, depDown := s.status, s.depDown
		s.mu.Unlock()
		total++
		switch st {
		case StRunning:
			running++
		case StWarning:
			running++
			abnormal++
		case StBackoff, StFailed:
			abnormal++
		}
		if depDown && st != StWarning {
			abnormal++
		}
	}
	m.mu.Lock()
	h := m.hostStats
	m.mu.Unlock()
	if h.CPUOver || h.MemOver || h.ConnOver {
		abnormal++
	}
	return
}

// StartAll 启动所有未在监控中的程序
func (m *Manager) StartAll() {
	m.mu.Lock()
	var ids []string
	for _, a := range m.cfg.Apps {
		ids = append(ids, a.ID)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.StartApp(id)
	}
}
