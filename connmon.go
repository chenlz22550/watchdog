package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const defaultConnMax = 10000

// 展示顺序
var connStateOrder = []string{"ESTABLISHED", "TIME_WAIT", "CLOSE_WAIT", "LISTEN", "SYN_SENT", "SYN_RCVD",
	"FIN_WAIT1", "FIN_WAIT2", "LAST_ACK", "CLOSING", "CLOSED", "DELETE_TCB"}

type stateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type procCount struct {
	Name  string
	PID   uint32
	Count int
}

func collectConns() (total int, states map[string]int, byPID map[uint32]int, err error) {
	states = map[string]int{}
	byPID = map[uint32]int{}
	err = tcpRows(func(st, pid uint32) {
		total++
		name := tcpStateNames[st]
		if name == "" {
			name = fmt.Sprintf("STATE_%d", st)
		}
		states[name]++
		byPID[pid]++
	})
	return
}

func topProcesses(byPID map[uint32]int, n int) []procCount {
	var list []procCount
	for pid, c := range byPID {
		list = append(list, procCount{PID: pid, Count: c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Count > list[j].Count })
	if len(list) > n {
		list = list[:n]
	}
	for i := range list {
		list[i].Name = processName(list[i].PID)
	}
	return list
}

func orderedStates(states map[string]int) []stateCount {
	var out []stateCount
	seen := map[string]bool{}
	for _, s := range connStateOrder {
		if c := states[s]; c > 0 {
			out = append(out, stateCount{s, c})
		}
		seen[s] = true
	}
	for s, c := range states {
		if !seen[s] && c > 0 {
			out = append(out, stateCount{s, c})
		}
	}
	return out
}

func buildConnMsg(host, title string, total, limit int, states []stateCount, top []procCount) string {
	var m msgBuilder
	color := "warning"
	if top == nil {
		color = "info"
	}
	m.line("### %s 主机连接数", colored(color, "【"+title+"】"))
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	m.line("> 当前 TCP 连接：%s（阈值 %d）", colored(color, fmt.Sprint(total)), limit)
	var parts []string
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%s %d", s.State, s.Count))
	}
	m.kv("状态分布", strings.Join(parts, "，"))
	if len(top) > 0 {
		m.line("")
		m.line("**连接数最多的进程：**")
		for _, p := range top {
			m.line(`<font color="comment">%s（PID %d）：%d</font>`, mdEscape(p.Name), p.PID, p.Count)
		}
	}
	return m.String()
}

// buildDepMsg 依赖端口（目标地址）不通 / 恢复
func buildDepMsg(host string, cfg AppConfig, failed []string, recovered bool) string {
	var m msgBuilder
	if recovered {
		m.line("### %s %s", colored("info", "【依赖恢复】"), mdEscape(cfg.Name))
	} else {
		m.line("### %s %s", colored("warning", "【依赖端口不通】"), mdEscape(cfg.Name))
	}
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	if recovered {
		m.kv("目标地址", cfg.Deps)
		m.line("> 状态：%s", colored("info", "全部目标端口已恢复连通"))
	} else {
		m.line("> 不通的目标：%s", colored("warning", mdEscape(strings.Join(failed, "；"))))
		m.kv("检测规则", fmt.Sprintf("每 %d 秒检测，连续 %d 次失败", cfg.Interval, cfg.Threshold))
		m.line("> 说明：仅通知，不会重启程序")
	}
	m.kv("程序路径", cfg.Path)
	return m.String()
}
