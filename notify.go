package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// 企业微信 markdown 内容上限 4096 字节
const maxMarkdownBytes = 4000

type notifyJob struct {
	title   string // 用于事件日志
	content string
	result  chan error // 可为 nil
}

// Notifier 企业微信机器人推送，单协程串行发送，保证消息顺序
type Notifier struct {
	webhook func() string
	onDone  func(title string, err error)
	queue   chan notifyJob
	client  *http.Client
	idle    chan struct{}
}

func newNotifier(webhook func() string, onDone func(string, error)) *Notifier {
	n := &Notifier{
		webhook: webhook,
		onDone:  onDone,
		queue:   make(chan notifyJob, 256),
		client:  &http.Client{Timeout: 15 * time.Second},
		idle:    make(chan struct{}, 1),
	}
	go n.run()
	return n
}

// Send 异步推送
func (n *Notifier) Send(title, content string) {
	select {
	case n.queue <- notifyJob{title: title, content: content}:
	default:
		n.onDone(title, errors.New("推送队列已满，消息被丢弃"))
	}
}

// SendWait 推送并等待结果
func (n *Notifier) SendWait(title, content string) error {
	ch := make(chan error, 1)
	n.queue <- notifyJob{title: title, content: content, result: ch}
	return <-ch
}

// Flush 等待队列中的消息发完（最多 timeout）
func (n *Notifier) Flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(n.queue) == 0 {
			select {
			case n.idle <- struct{}{}: // 发送协程空闲时才能拿到这个令牌
				<-n.idle
				return
			default:
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (n *Notifier) run() {
	for job := range n.queue {
		n.idle <- struct{}{} // 标记忙碌
		var err error
		for i := 0; i < 3; i++ {
			var retryAfter time.Duration
			err, retryAfter = n.post(job.content)
			if err == nil {
				break
			}
			if retryAfter == 0 {
				retryAfter = time.Duration(i+1) * 3 * time.Second
			}
			time.Sleep(retryAfter)
		}
		<-n.idle
		n.onDone(job.title, err)
		if job.result != nil {
			job.result <- err
		}
	}
}

func (n *Notifier) post(content string) (error, time.Duration) {
	url := strings.TrimSpace(n.webhook())
	if url == "" {
		return errors.New("未配置 Webhook 地址"), time.Second
	}
	body, _ := json.Marshal(map[string]interface{}{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": truncateBytes(content, maxMarkdownBytes)},
	})
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("网络错误：%v", err), 0
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateBytes(string(raw), 200)), 0
	}
	if r.ErrCode == 45009 { // 频率超限（每个机器人 20 条/分钟）
		return fmt.Errorf("频率超限：%s", r.ErrMsg), 30 * time.Second
	}
	if r.ErrCode != 0 {
		return fmt.Errorf("errcode=%d %s", r.ErrCode, r.ErrMsg), 0
	}
	return nil, 0
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len("\n…(已截断)")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…(已截断)"
}

// ---------- 消息模板 ----------

type msgBuilder struct{ b strings.Builder }

func (m *msgBuilder) line(format string, a ...interface{}) {
	fmt.Fprintf(&m.b, format, a...)
	m.b.WriteByte('\n')
}

func (m *msgBuilder) kv(k, v string) {
	if v != "" {
		m.line("> %s：%s", k, mdEscape(v))
	}
}

func (m *msgBuilder) String() string { return m.b.String() }

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "<", "＜")
	s = strings.ReplaceAll(s, ">", "＞")
	return strings.TrimSpace(s)
}

func colored(color, s string) string {
	return fmt.Sprintf(`<font color="%s">%s</font>`, color, s)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	day := d / (24 * time.Hour)
	d -= day * 24 * time.Hour
	h := d / time.Hour
	d -= h * time.Hour
	mi := d / time.Minute
	s := (d - mi*time.Minute) / time.Second
	switch {
	case day > 0:
		return fmt.Sprintf("%d天%d小时%d分", day, h, mi)
	case h > 0:
		return fmt.Sprintf("%d小时%d分%d秒", h, mi, s)
	case mi > 0:
		return fmt.Sprintf("%d分%d秒", mi, s)
	}
	return fmt.Sprintf("%d秒", s)
}

// buildDeathMsg 程序异常通知；plan 为后续处理说明
func buildDeathMsg(host string, cfg AppConfig, d *Death, plan string, giveUp bool) string {
	var m msgBuilder
	title := colored("warning", "【程序异常】")
	if d.RestartNo > 0 {
		title = colored("warning", fmt.Sprintf("【第%d次重启失败】", d.RestartNo))
	}
	m.line("### %s %s", title, mdEscape(cfg.Name))
	m.kv("机器", host)
	m.line("> 异常类型：%s", colored("warning", d.Kind))
	m.kv("异常详情", d.Detail)
	m.kv("发生时间", fmtTime(d.At))
	if d.PID > 0 {
		m.kv("PID", fmt.Sprint(d.PID))
	}
	if !d.StartAt.IsZero() {
		m.kv("启动时间", fmtTime(d.StartAt))
		m.kv("运行时长", fmtDuration(d.At.Sub(d.StartAt)))
	}
	if d.HasCode {
		m.kv("退出码", describeExitCode(d.ExitCode))
	}
	if d.MemInfo != "" {
		m.kv("内存", d.MemInfo)
	}
	if d.Crash != "" {
		m.kv("崩溃信息", d.Crash)
	}
	m.kv("程序路径", cfg.Path)
	if cfg.Args != "" {
		m.kv("启动参数", cfg.Args)
	}
	if giveUp {
		m.line("> 处理：%s", colored("warning", plan))
	} else {
		m.line("> 处理：%s", colored("info", plan))
	}
	if len(d.Output) > 0 {
		m.line("")
		m.line("**最后输出：**")
		for _, l := range d.Output {
			m.line(`<font color="comment">%s</font>`, mdEscape(truncateBytes(l, 300)))
		}
	}
	return m.String()
}

func buildRestartOKMsg(host string, cfg AppConfig, no, pid int) string {
	var m msgBuilder
	m.line("### %s %s", colored("info", fmt.Sprintf("【第%d次重启成功】", no)), mdEscape(cfg.Name))
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	m.kv("新 PID", fmt.Sprint(pid))
	if cfg.RestartMode == ModeImmediate {
		m.kv("重启策略", fmt.Sprintf("立即重启（连续第 %d 次）", no))
	} else {
		m.kv("重启策略", fmt.Sprintf("退避重启（%d/%d）", no, len(restartDelays)))
	}
	m.kv("程序路径", cfg.Path)
	return m.String()
}

func buildTestMsg(host string, apps int, version string) string {
	var m msgBuilder
	m.line("### %s 进程守护推送测试", colored("info", "【测试】"))
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	m.kv("监控程序数", fmt.Sprint(apps))
	m.kv("版本", version)
	m.line("> 收到此消息说明机器人配置正确 ✅")
	return m.String()
}

func buildMonitorMsg(host, event string, names []string) string {
	var m msgBuilder
	m.line("### %s 进程守护%s", colored("comment", "【监控器】"), event)
	m.kv("机器", host)
	m.kv("时间", fmtTime(time.Now()))
	if len(names) > 0 {
		m.kv("涉及程序", strings.Join(names, "、"))
	}
	return m.String()
}

// describeExitCode 常见退出码 / NTSTATUS 说明
func describeExitCode(code uint32) string {
	known := map[uint32]string{
		0:          "正常退出",
		1:          "一般错误（也可能是被强制结束）",
		0x40010004: "被调试器或任务管理器终止",
		0xC0000005: "访问冲突（非法内存访问）",
		0xC000001D: "非法指令",
		0xC0000094: "整数除零",
		0xC00000FD: "栈溢出",
		0xC0000135: "找不到依赖的 DLL",
		0xC0000139: "找不到 DLL 入口点",
		0xC0000142: "DLL 初始化失败",
		0xC000013A: "被 Ctrl+C 中断",
		0xC0000017: "内存不足",
		0xC0000374: "堆损坏",
		0xC0000409: "栈缓冲区溢出 / 快速失败",
		0xC0000417: "CRT 无效参数",
		0xE0434352: ".NET 未处理异常",
		0xE06D7363: "C++ 未处理异常",
		0xCFFFFFFF: "应用程序挂起后被终止",
	}
	desc, ok := known[code]
	if !ok {
		if code >= 0xC0000000 {
			desc = "系统异常（NTSTATUS）"
		} else {
			desc = "程序自定义退出码"
		}
	}
	if code < 0x10000 {
		return fmt.Sprintf("%d（%s）", code, desc)
	}
	return fmt.Sprintf("0x%08X（%s）", code, desc)
}
