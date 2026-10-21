package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	hostInterval   = 5 * time.Second
	hostRemind     = 30 * time.Minute // 持续超限时的重复提醒间隔
	hostHistoryLen = 72               // 约 6 分钟走势
	defaultCPUMax  = 90
	defaultMemMax  = 90
	defaultSustain = 30
)

// HostStats 宿主机资源快照
type HostStats struct {
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	MemUsed  string  `json:"memUsed"`
	MemTotal string  `json:"memTotal"`
	CPUHist  []int   `json:"cpuHist"`
	MemHist  []int   `json:"memHist"`
	CPULimit int     `json:"cpuLimit"`
	MemLimit int     `json:"memLimit"`
	CPUOver  bool    `json:"cpuOver"`
	MemOver  bool    `json:"memOver"`
	Cores    int     `json:"cores"`

	Conns     int          `json:"conns"`
	UDP       int          `json:"udp"`
	States    []stateCount `json:"states"`
	ConnLimit int          `json:"connLimit"`
	ConnOver  bool         `json:"connOver"`
	ConnPeak  int          `json:"connPeak"`
	ConnHist  []int        `json:"connHist"`

	Err string `json:"err"`
	At  string `json:"at"`
}

type hostLimits struct {
	cpu, mem, conn int
	sustain        int // 需要连续超限的采样次数
}

func (m *Manager) hostLimits() hostLimits {
	m.mu.Lock()
	c := m.cfg
	l := hostLimits{cpu: c.CPULimit, mem: c.MemLimit, conn: c.ConnLimit}
	sustain := c.HostSustain
	m.mu.Unlock()
	if l.cpu <= 0 {
		l.cpu = defaultCPUMax
	}
	if l.mem <= 0 {
		l.mem = defaultMemMax
	}
	if l.conn <= 0 {
		l.conn = defaultConnMax
	}
	if sustain <= 0 {
		sustain = defaultSustain
	}
	l.sustain = (sustain + int(hostInterval/time.Second) - 1) / int(hostInterval/time.Second)
	if l.sustain < 1 {
		l.sustain = 1
	}
	return l
}

// gauge 阈值判定：连续超限 N 次触发，回落到阈值的 95% 以下恢复，持续超限每 30 分钟提醒一次
type gauge struct {
	over      bool
	streak    int
	lastAlert time.Time
}

type gaugeResult int

const (
	gaugeNone gaugeResult = iota
	gaugeFire
	gaugeRemind
	gaugeRecover
)

func (g *gauge) eval(val, limit float64, sustain int) gaugeResult {
	if val > limit {
		g.streak++
		if !g.over && g.streak >= sustain {
			g.over, g.lastAlert = true, time.Now()
			return gaugeFire
		}
		if g.over && time.Since(g.lastAlert) >= hostRemind {
			g.lastAlert = time.Now()
			return gaugeRemind
		}
		return gaugeNone
	}
	g.streak = 0
	if g.over && val < limit*0.95 {
		g.over = false
		return gaugeRecover
	}
	return gaugeNone
}

func pushHist(h []int, v int) []int {
	h = append(h, v)
	if len(h) > hostHistoryLen {
		h = append(h[:0:0], h[len(h)-hostHistoryLen:]...)
	}
	return h
}

// hostLoop 每 5 秒采集一次 CPU、内存、TCP 连接数
func (m *Manager) hostLoop() {
	var cpuG, memG, connG gauge
	lastIdle, lastTotal, _ := systemTimes()
	for {
		time.Sleep(hostInterval)
		lim := m.hostLimits()

		var cpu, mem float64
		if idle, total, ok := systemTimes(); ok && total > lastTotal {
			cpu = 100 * (1 - float64(idle-lastIdle)/float64(total-lastTotal))
			if cpu < 0 {
				cpu = 0
			}
			lastIdle, lastTotal = idle, total
		}
		memTotal, memAvail, memOK := memoryStatus()
		if memOK && memTotal > 0 {
			mem = 100 * float64(memTotal-memAvail) / float64(memTotal)
		}
		conns, states, byPID, connErr := collectConns()
		udp := udpCount()

		m.mu.Lock()
		h := &m.hostStats
		h.CPU, h.Mem, h.Cores = round1(cpu), round1(mem), runtime.NumCPU()
		h.MemUsed = fmt.Sprintf("%.1f", float64(memTotal-memAvail)/(1<<30))
		h.MemTotal = fmt.Sprintf("%.1f", float64(memTotal)/(1<<30))
		h.CPUHist = pushHist(h.CPUHist, int(cpu+0.5))
		h.MemHist = pushHist(h.MemHist, int(mem+0.5))
		h.CPULimit, h.MemLimit, h.ConnLimit = lim.cpu, lim.mem, lim.conn
		h.At = time.Now().Format("15:04:05")
		if connErr != nil {
			h.Err = "读取连接表失败：" + connErr.Error()
		} else {
			h.Err = ""
			h.Conns, h.UDP, h.States = conns, udp, orderedStates(states)
			if conns > h.ConnPeak {
				h.ConnPeak = conns
			}
			h.ConnHist = pushHist(h.ConnHist, conns)
		}
		m.mu.Unlock()

		host := m.host()
		sustainSec := lim.sustain * int(hostInterval/time.Second)

		switch cpuG.eval(cpu, float64(lim.cpu), lim.sustain) {
		case gaugeFire, gaugeRemind:
			top := topCPU(5)
			m.hostAlert("CPU 占用过高", fmt.Sprintf("CPU %.1f%% > %d%%", cpu, lim.cpu),
				buildHostMsg(host, "CPU 占用过高", "CPU 使用率", fmt.Sprintf("%.1f%%", cpu), fmt.Sprintf("%d%%（持续 %d 秒）", lim.cpu, sustainSec), top, "CPU", false))
		case gaugeRecover:
			m.hostRecover("CPU 已恢复", fmt.Sprintf("CPU %.1f%%", cpu),
				buildHostMsg(host, "CPU 已恢复", "CPU 使用率", fmt.Sprintf("%.1f%%", cpu), fmt.Sprintf("%d%%", lim.cpu), nil, "", true))
		}
		switch memG.eval(mem, float64(lim.mem), lim.sustain) {
		case gaugeFire, gaugeRemind:
			top := topMem(5)
			used := fmt.Sprintf("%.1f%%（%s / %s GB）", mem, fmtGB(memTotal-memAvail), fmtGB(memTotal))
			m.hostAlert("内存占用过高", fmt.Sprintf("内存 %.1f%% > %d%%", mem, lim.mem),
				buildHostMsg(host, "内存占用过高", "内存使用率", used, fmt.Sprintf("%d%%（持续 %d 秒）", lim.mem, sustainSec), top, "内存", false))
		case gaugeRecover:
			m.hostRecover("内存已恢复", fmt.Sprintf("内存 %.1f%%", mem),
				buildHostMsg(host, "内存已恢复", "内存使用率", fmt.Sprintf("%.1f%%", mem), fmt.Sprintf("%d%%", lim.mem), nil, "", true))
		}
		if connErr == nil {
			// 连接数超过阈值立即告警（一次采样即触发）
			switch connG.eval(float64(conns), float64(lim.conn), 1) {
			case gaugeFire, gaugeRemind:
				title := "连接数超限"
				m.hostAlert(title, fmt.Sprintf("TCP 连接 %d > %d", conns, lim.conn),
					buildConnMsg(host, title, conns, lim.conn, orderedStates(states), topProcesses(byPID, 5)))
			case gaugeRecover:
				m.hostRecover("连接数恢复", fmt.Sprintf("TCP 连接 %d", conns),
					buildConnMsg(host, "连接数恢复", conns, lim.conn, orderedStates(states), nil))
			}
		}
		m.mu.Lock()
		m.hostStats.CPUOver, m.hostStats.MemOver, m.hostStats.ConnOver = cpuG.over, memG.over, connG.over
		m.mu.Unlock()
	}
}

func (m *Manager) hostAlert(title, short, content string) {
	m.event("error", "主机", title+"："+short)
	m.balloon("进程守护："+title, short, true)
	m.push(title, content)
}

func (m *Manager) hostRecover(title, short, content string) {
	m.event("ok", "主机", title+"："+short)
	m.push(title, content)
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func fmtGB(b uint64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<30)) }

type procUsage struct {
	Name  string
	PID   uint32
	Value string
}

// topCPU 采样 1 秒，返回 CPU 占用最高的进程
func topCPU(n int) []procUsage {
	procs := listProcesses()
	before := map[uint32]uint64{}
	for _, p := range procs {
		if t, ok := processCPUTime(p.PID); ok {
			before[p.PID] = t
		}
	}
	start := time.Now()
	time.Sleep(time.Second)
	elapsed := float64(time.Since(start).Nanoseconds()/100) * float64(runtime.NumCPU())
	type item struct {
		p   procEntry
		pct float64
	}
	var list []item
	for _, p := range procs {
		t0, ok0 := before[p.PID]
		t1, ok1 := processCPUTime(p.PID)
		if ok0 && ok1 && t1 > t0 && p.PID != 0 {
			list = append(list, item{p, 100 * float64(t1-t0) / elapsed})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].pct > list[j].pct })
	var out []procUsage
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, procUsage{list[i].p.Name, list[i].p.PID, fmt.Sprintf("%.1f%%", list[i].pct)})
	}
	return out
}

// topMem 返回工作集最大的进程
func topMem(n int) []procUsage {
	type item struct {
		p  procEntry
		ws uint64
	}
	var list []item
	for _, p := range listProcesses() {
		if ws, _, ok := processMemory(int(p.PID)); ok {
			list = append(list, item{p, ws})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ws > list[j].ws })
	var out []procUsage
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, procUsage{list[i].p.Name, list[i].p.PID, fmt.Sprintf("%.0f MB", float64(list[i].ws)/(1<<20))})
	}
	return out
}

func buildHostMsg(host, title, metric, value, limit string, top []procUsage, topLabel string, recovered bool) string {
	var m msgBuilder
	color := "warning"
	if recovered {
		color = "info"
	}
	m.line("### %s 宿主机资源", colored(color, "【"+title+"】"))
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	m.line("> %s：%s", metric, colored(color, value))
	m.kv("阈值", limit)
	if len(top) > 0 {
		m.line("")
		m.line("**%s占用最高的进程：**", topLabel)
		var lines []string
		for _, p := range top {
			lines = append(lines, fmt.Sprintf(`<font color="comment">%s（PID %d）：%s</font>`, mdEscape(p.Name), p.PID, p.Value))
		}
		m.line("%s", strings.Join(lines, "\n"))
	}
	return m.String()
}
