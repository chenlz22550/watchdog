package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lxn/win"
	"github.com/sciter-sdk/go-sciter"
	"github.com/sciter-sdk/go-sciter/window"
)

// toValue 把 Go 结构体经 JSON 转成 Sciter 原生 map/array
func toValue(v interface{}) *sciter.Value {
	b, err := json.Marshal(v)
	if err != nil {
		return sciter.NewValue()
	}
	val := sciter.NewValue()
	val.ConvertFromString(string(b), sciter.CVT_JSON_LITERAL)
	return val
}

func result(msg string, err error) *sciter.Value {
	if err != nil {
		return toValue(map[string]interface{}{"ok": false, "msg": err.Error()})
	}
	return toValue(map[string]interface{}{"ok": true, "msg": msg})
}

func argStr(args []*sciter.Value, i int) string {
	if i >= len(args) || args[i] == nil || args[i].IsUndefined() || args[i].IsNull() {
		return ""
	}
	return args[i].String()
}

func argBool(args []*sciter.Value, i int) bool {
	if i >= len(args) || args[i] == nil {
		return false
	}
	if args[i].IsBool() || args[i].IsInt() {
		return args[i].Bool()
	}
	s := strings.ToLower(argStr(args, i))
	return s == "true" || s == "1"
}

func argInt(args []*sciter.Value, i int) int {
	if i < len(args) && args[i] != nil && args[i].IsInt() {
		return args[i].Int()
	}
	n, _ := strconv.Atoi(strings.TrimSpace(argStr(args, i)))
	return n
}

// fileURLToPath Sciter 的文件对话框返回 file:// URL
func fileURLToPath(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), "file://") {
		return s
	}
	s = s[len("file://"):]
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	if len(s) >= 3 && s[0] == '/' && s[2] == ':' { // /C:/xxx
		s = s[1:]
	} else if len(s) >= 2 && s[1] != ':' && !strings.HasPrefix(s, "/") { // server/share
		s = "//" + s
	}
	return filepath.FromSlash(s)
}

func bindUI(w *window.Window, m *Manager, tray *Tray) {
	w.DefineFunction("getState", func(args ...*sciter.Value) *sciter.Value {
		since := -1
		if len(args) > 0 {
			since = argInt(args, 0)
		}
		snap := m.Snapshot(since)
		snap.Autorun = getAutorun()
		return toValue(snap)
	})

	// saveApp(id, name, path, args, workDir, ports, checkHang, capture, autoStart, grace, interval, threshold, restartMode, deps)
	w.DefineFunction("saveApp", func(args ...*sciter.Value) *sciter.Value {
		a := AppConfig{
			ID:          argStr(args, 0),
			Name:        argStr(args, 1),
			Path:        argStr(args, 2),
			Args:        argStr(args, 3),
			WorkDir:     argStr(args, 4),
			Ports:       argStr(args, 5),
			CheckHang:   argBool(args, 6),
			Capture:     argBool(args, 7),
			AutoStart:   argBool(args, 8),
			Grace:       argInt(args, 9),
			Interval:    argInt(args, 10),
			Threshold:   argInt(args, 11),
			RestartMode: argStr(args, 12),
			Deps:        argStr(args, 13),
		}
		id, msg, err := m.SaveApp(a)
		if err != nil {
			return result("", err)
		}
		return toValue(map[string]interface{}{"ok": true, "msg": msg, "id": id})
	})

	w.DefineFunction("scanApps", func(args ...*sciter.Value) *sciter.Value {
		list, hidden, err := m.ScanApps()
		if err != nil {
			return result("", err)
		}
		if list == nil {
			list = []Candidate{}
		}
		return toValue(map[string]interface{}{"ok": true, "list": list, "hidden": hidden, "user": currentUserName()})
	})
	// importApps(以换行分隔的 key, restartMode, withPorts)
	w.DefineFunction("importApps", func(args ...*sciter.Value) *sciter.Value {
		keys := strings.Fields(argStr(args, 0))
		n, errs := m.ImportApps(keys, argStr(args, 1), argBool(args, 2))
		if n == 0 && len(errs) > 0 {
			return result("", errors.New(strings.Join(errs, "；")))
		}
		msg := fmt.Sprintf("已导入 %d 个程序", n)
		if len(errs) > 0 {
			msg += "，失败：" + strings.Join(errs, "；")
		}
		return result(msg, nil)
	})

	w.DefineFunction("removeApp", func(args ...*sciter.Value) *sciter.Value {
		m.RemoveApp(argStr(args, 0))
		return result("已删除", nil)
	})
	w.DefineFunction("startApp", func(args ...*sciter.Value) *sciter.Value {
		m.StartApp(argStr(args, 0))
		return result("正在启动", nil)
	})
	w.DefineFunction("stopApp", func(args ...*sciter.Value) *sciter.Value {
		m.StopApp(argStr(args, 0))
		return result("正在停止", nil)
	})
	w.DefineFunction("restartApp", func(args ...*sciter.Value) *sciter.Value {
		m.RestartApp(argStr(args, 0))
		return result("正在重启", nil)
	})
	w.DefineFunction("startAll", func(args ...*sciter.Value) *sciter.Value {
		m.StartAll()
		return result("正在启动全部程序", nil)
	})

	// saveSettings(webhook, machine, autorun, cpuLimit, memLimit, hostSustain, connLimit)
	w.DefineFunction("saveSettings", func(args ...*sciter.Value) *sciter.Value {
		st := Settings{
			Webhook: argStr(args, 0), Machine: argStr(args, 1),
			CPULimit: argInt(args, 3), MemLimit: argInt(args, 4),
			HostSustain: argInt(args, 5), ConnLimit: argInt(args, 6),
		}
		if err := m.SaveSettings(st); err != nil {
			return result("", err)
		}
		on := argBool(args, 2)
		if on != getAutorun() {
			if err := setAutorun(on); err != nil {
				return result("", err)
			}
		}
		return result("设置已保存", nil)
	})

	w.DefineFunction("testPush", func(args ...*sciter.Value) *sciter.Value {
		m.TestPush()
		return result("测试消息已发送，结果见事件日志", nil)
	})

	w.DefineFunction("toPath", func(args ...*sciter.Value) *sciter.Value {
		return sciter.NewValue(fileURLToPath(argStr(args, 0)))
	})
	w.DefineFunction("baseName", func(args ...*sciter.Value) *sciter.Value {
		p := argStr(args, 0)
		return sciter.NewValue(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
	})
	w.DefineFunction("dirName", func(args ...*sciter.Value) *sciter.Value {
		p := argStr(args, 0)
		if p == "" {
			return sciter.NewValue("")
		}
		return sciter.NewValue(filepath.Dir(p))
	})

	w.DefineFunction("openFolder", func(args ...*sciter.Value) *sciter.Value {
		if p := m.appPath(argStr(args, 0)); p != "" {
			openInExplorer(p)
		}
		return result("", nil)
	})
	w.DefineFunction("openLogs", func(args ...*sciter.Value) *sciter.Value {
		os.MkdirAll(m.logDir, 0755)
		openInExplorer(m.logDir)
		return result("", nil)
	})
	// winCtl("min" | "max")：自绘标题栏的最小化 / 最大化（还原）
	w.DefineFunction("winCtl", func(args ...*sciter.Value) *sciter.Value {
		switch argStr(args, 0) {
		case "min":
			win.ShowWindow(tray.hwnd, win.SW_MINIMIZE)
		case "max":
			if win.IsZoomed(tray.hwnd) {
				win.ShowWindow(tray.hwnd, win.SW_RESTORE)
			} else {
				win.ShowWindow(tray.hwnd, win.SW_MAXIMIZE)
			}
		}
		return sciter.NewValue(win.IsZoomed(tray.hwnd))
	})
	w.DefineFunction("isMaximized", func(args ...*sciter.Value) *sciter.Value {
		return sciter.NewValue(win.IsZoomed(tray.hwnd))
	})
	w.DefineFunction("hideWindow", func(args ...*sciter.Value) *sciter.Value {
		tray.HideMain()
		return result("", nil)
	})
	w.DefineFunction("quitApp", func(args ...*sciter.Value) *sciter.Value {
		tray.Quit()
		return result("", nil)
	})
}
