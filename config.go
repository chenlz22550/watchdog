package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultWebhook 首次运行时写入配置的默认机器人地址，可在编译时注入：
// go build -ldflags "-X main.defaultWebhook=https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
var defaultWebhook = ""

// AppConfig 单个被监控程序的配置
type AppConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Args      string `json:"args"`
	WorkDir   string `json:"workDir"`   // 为空时使用程序所在目录
	Ports     string `json:"ports"`     // 例："8080, 127.0.0.1:9000"
	CheckHang bool   `json:"checkHang"` // 检测窗口假死
	Capture   bool   `json:"capture"`   // 隐藏窗口并捕获控制台输出
	AutoStart bool   `json:"autoStart"` // 监控器启动时自动启动/接管
	Grace     int    `json:"grace"`     // 启动宽限期（秒），期间不做健康检查
	Interval  int    `json:"interval"`  // 检测间隔（秒）
	Threshold int    `json:"threshold"` // 连续失败多少次判定异常
	// 重启策略：backoff 按 1s/10s/100s 退避最多 3 次；immediate 立即重启、不限次数
	RestartMode string `json:"restartMode"`
	// 依赖的目标地址（如数据库 10.0.0.5:1433），不通时只通知、不重启
	Deps string `json:"deps"`
}

const (
	ModeBackoff   = "backoff"
	ModeImmediate = "immediate"
)

// Config 全局配置
type Config struct {
	Webhook string       `json:"webhook"`
	Machine string       `json:"machine"` // 通知中显示的机器标识，为空则用主机名
	Apps    []*AppConfig `json:"apps"`

	// 宿主机告警阈值
	CPULimit    int `json:"cpuLimit"`    // CPU 使用率 %
	MemLimit    int `json:"memLimit"`    // 内存使用率 %
	HostSustain int `json:"hostSustain"` // CPU/内存持续超限多少秒才告警
	ConnLimit   int `json:"connLimit"`   // TCP 连接总数
}

func (a *AppConfig) normalize() {
	a.Name = strings.TrimSpace(a.Name)
	a.Path = strings.TrimSpace(strings.Trim(strings.TrimSpace(a.Path), `"`))
	a.Args = strings.TrimSpace(a.Args)
	a.WorkDir = strings.TrimSpace(strings.Trim(strings.TrimSpace(a.WorkDir), `"`))
	a.Ports = strings.TrimSpace(a.Ports)
	a.Deps = strings.TrimSpace(a.Deps)
	if a.Name == "" && a.Path != "" {
		a.Name = strings.TrimSuffix(filepath.Base(a.Path), filepath.Ext(a.Path))
	}
	if a.Grace <= 0 {
		a.Grace = 30
	}
	if a.Interval <= 0 {
		a.Interval = 10
	}
	if a.Threshold <= 0 {
		a.Threshold = 3
	}
	if a.RestartMode != ModeImmediate {
		a.RestartMode = ModeBackoff
	}
}

func (a *AppConfig) validate() error {
	if a.Path == "" {
		return errors.New("请填写程序路径")
	}
	if !filepath.IsAbs(a.Path) {
		return errors.New("程序路径必须是绝对路径")
	}
	if st, err := os.Stat(a.Path); err != nil || st.IsDir() {
		return fmt.Errorf("程序不存在：%s", a.Path)
	}
	if a.WorkDir != "" {
		if st, err := os.Stat(a.WorkDir); err != nil || !st.IsDir() {
			return fmt.Errorf("工作目录不存在：%s", a.WorkDir)
		}
	}
	if _, err := parsePorts(a.Ports); err != nil {
		return err
	}
	if _, err := parsePorts(a.Deps); err != nil {
		return fmt.Errorf("依赖端口：%v", err)
	}
	return nil
}

// EffectiveWorkDir 未设置工作目录时默认为程序所在目录
func (a *AppConfig) EffectiveWorkDir() string {
	if a.WorkDir != "" {
		return a.WorkDir
	}
	return filepath.Dir(a.Path)
}

// parsePorts 解析 "8080, 127.0.0.1:9000" 形式的端口列表，纯数字默认 127.0.0.1
func parsePorts(s string) ([]string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var out []string
	for _, f := range fields {
		addr := f
		if !strings.Contains(f, ":") {
			addr = "127.0.0.1:" + f
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("端口格式错误：%s", f)
		}
		if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("端口号无效：%s", f)
		}
		if host == "" {
			host = "127.0.0.1"
		}
		out = append(out, net.JoinHostPort(host, port))
	}
	return out, nil
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{Webhook: defaultWebhook, CPULimit: 90, MemLimit: 90, HostSustain: 30, ConnLimit: 10000}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件解析失败：%v", err)
	}
	for _, a := range cfg.Apps {
		a.normalize()
	}
	return cfg, nil
}

// writeJSONFile 先写临时文件再替换，避免写一半断电导致配置损坏
func writeJSONFile(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
