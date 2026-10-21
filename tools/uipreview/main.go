//go:build darwin

// uipreview 在 macOS 上用假数据加载 ui/index.html，用于检查界面与脚本（不含任何监控逻辑）
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sciter-sdk/go-sciter"
	"github.com/sciter-sdk/go-sciter/window"
)

func toValue(v interface{}) *sciter.Value {
	b, _ := json.Marshal(v)
	val := sciter.NewValue()
	val.ConvertFromString(string(b), sciter.CVT_JSON_LITERAL)
	return val
}

type m = map[string]interface{}

func hist(n int, lo, hi float64) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = int(lo + (hi-lo)*(0.5+0.5*math.Sin(float64(i)/5)))
	}
	return out
}

func fakeState() m {
	apps := []m{
		{"id": "a1", "name": "订单服务", "path": `C:\Services\order\order-server.exe`, "args": "--port 8080 -c config.ini", "workDir": "", "effDir": `C:\Services\order`,
			"ports": "8080, 127.0.0.1:9090", "deps": "10.0.0.5:1433", "restartMode": "immediate",
			"depState": []m{{"addr": "10.0.0.5:1433", "ok": true}}, "checkHang": true, "capture": true, "autoStart": true, "grace": 30, "interval": 10, "threshold": 3,
			"status": "running", "note": "", "pid": 4128, "uptime": "3天4小时12分", "restarts": 2, "attempt": 0, "nextIn": 0, "mem": "182.4 MB",
			"portState": []m{{"addr": "127.0.0.1:8080", "ok": true}, {"addr": "127.0.0.1:9090", "ok": true}}, "hung": false, "active": true},
		{"id": "a2", "name": "数据采集客户端", "path": `D:\Collector\collector.exe`, "args": "", "workDir": "", "effDir": `D:\Collector`,
			"ports": "", "checkHang": true, "capture": false, "autoStart": true, "grace": 30, "interval": 10, "threshold": 3,
			"status": "backoff", "note": "将在 10秒 后进行第 2/3 次重启", "pid": 0, "uptime": "", "restarts": 1, "attempt": 2, "nextIn": 7, "nextTotal": 10, "mem": "",
			"portState": nil, "hung": false, "active": true},
		{"id": "a3", "name": "报表网关", "path": `C:\Gateway\gateway.exe`, "args": "-mode prod", "workDir": `C:\Gateway\bin`, "effDir": `C:\Gateway\bin`,
			"ports": "7001", "deps": "10.0.0.8:3306", "depDown": true,
			"depState": []m{{"addr": "10.0.0.8:3306", "ok": false, "err": "连接超时"}}, "checkHang": false, "capture": true, "autoStart": false, "grace": 60, "interval": 5, "threshold": 2,
			"status": "warning", "note": "端口无响应（1/2）", "pid": 9920, "uptime": "12分8秒", "restarts": 0, "attempt": 0, "nextIn": 0, "mem": "64.0 MB",
			"portState": []m{{"addr": "127.0.0.1:7001", "ok": false, "err": "连接被拒绝"}}, "hung": false, "active": true},
		{"id": "a4", "name": "旧版打印服务", "path": `C:\Print\printsvc.exe`, "args": "", "workDir": "", "effDir": `C:\Print`,
			"ports": "", "checkHang": true, "capture": false, "autoStart": false, "grace": 30, "interval": 10, "threshold": 3,
			"status": "failed", "note": "已放弃自动重启", "pid": 0, "uptime": "", "restarts": 3, "attempt": 3, "nextIn": 0, "mem": "",
			"portState": nil, "hung": false, "active": false},
	}
	events := []m{
		{"seq": 6, "time": "10-21 23:20:11", "level": "error", "app": "数据采集客户端", "text": "进程退出：进程已退出，退出码 0xC0000005（访问冲突（非法内存访问））"},
		{"seq": 5, "time": "10-21 23:20:12", "level": "info", "app": "企业微信", "text": "已推送：数据采集客户端 进程退出"},
		{"seq": 4, "time": "10-21 23:18:40", "level": "warn", "app": "报表网关", "text": "端口无响应，结束进程 PID 9812"},
		{"seq": 3, "time": "10-21 23:18:52", "level": "ok", "app": "报表网关", "text": "第 1 次重启成功，PID 9920"},
		{"seq": 2, "time": "10-21 23:01:03", "level": "ok", "app": "订单服务", "text": "已启动，PID 4128"},
		{"seq": 1, "time": "10-21 23:01:02", "level": "info", "app": "监控器", "text": "进程守护已启动 v1.0.0"},
	}
	return m{"apps": apps, "events": events, "eventSeq": 6, "webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx",
		"machine": "", "hostname": "WIN7-SERVER-01", "autorun": true, "version": "v1.0.0", "total": 4, "running": 2, "abnormal": 3,
		"settings": m{"cpuLimit": 90, "memLimit": 90, "hostSustain": 30, "connLimit": 10000},
		"host": m{"cpu": 37.5 + 20*math.Sin(float64(time.Now().Unix())/7), "mem": 63.2, "memUsed": "5.1", "memTotal": "8.0", "cores": 8,
			"cpuHist": hist(72, 25, 60), "memHist": hist(72, 60, 66), "cpuLimit": 90, "memLimit": 90, "cpuOver": false, "memOver": false,
			"conns": 8432, "udp": 214, "connLimit": 10000, "connPeak": 9120, "connOver": false, "at": time.Now().Format("15:04:05"),
			"states": []m{{"state": "ESTABLISHED", "count": 5210}, {"state": "TIME_WAIT", "count": 2876}, {"state": "CLOSE_WAIT", "count": 143},
				{"state": "LISTEN", "count": 61}, {"state": "SYN_SENT", "count": 12}, {"state": "FIN_WAIT2", "count": 130}}}}
}

func fakeCandidates() []m {
	return []m{
		{"key": "c1", "name": "ScktSrvr", "desc": "【中间层】应用服务器", "path": `F:\中间层\SAG\ScktSrvr.exe`, "args": "-port 211",
			"cmdline": `"F:\中间层\SAG\ScktSrvr.exe" -port 211`, "workDir": "", "pid": 3120, "count": 1, "mem": "9.2 MB",
			"started": "10-21 08:12", "listen": "127.0.0.1:211", "is32": true},
		{"key": "c2", "name": "nginx", "desc": "nginx.exe", "path": `F:\nginx-1.20\nginx.exe`, "args": "",
			"cmdline": `F:\nginx-1.20\nginx.exe`, "workDir": "", "pid": 2200, "count": 2, "mem": "5.6 MB",
			"started": "10-21 08:10", "listen": "127.0.0.1:80, 127.0.0.1:443", "is32": true},
		{"key": "c3", "name": "sync", "desc": "Sync.exe", "path": `F:\同步程序\sync.exe`, "args": "/auto /interval=60",
			"cmdline": `"F:\同步程序\sync.exe" /auto /interval=60`, "workDir": `F:\同步程序\data`, "pid": 4410, "count": 1, "mem": "8.2 MB",
			"started": "10-21 08:15", "is32": true},
		{"key": "c4", "name": "Reload", "desc": "Reload.exe", "path": `F:\同步程序\Reload.exe`, "cmdline": `F:\同步程序\Reload.exe`,
			"pid": 4388, "count": 1, "mem": "1.2 MB", "started": "10-21 08:15", "partial": true},
		{"key": "c5", "name": "StartUp", "desc": "程序监测", "path": `F:\中间层\SAG\StartUp.exe`, "cmdline": `"F:\中间层\SAG\StartUp.exe"`,
			"pid": 3002, "count": 1, "mem": "2.5 MB", "started": "10-21 08:11", "monitored": true},
	}
}

// 自动化检查脚本：依次触发各个命令，把结果通过 view.report 打印出来
const testScript = `
function T(name, v) { view.report(name + " = " + v); }
function step(name, f) { try { f(); } catch(e) { view.report("ERR " + name + ": " + e); } }
function shot(name) {
  step("shot " + name, function() {
    view.report("snap begin " + name);
    var (w, h) = self.box(#dimension);
    var img = new Image(w, h, self);
    view.report("snap image " + img);
    var b = img.toBytes(#png);
    view.report("snap bytes " + b.length);
    view.saveShot(name, b);
  });
}
self.timer(800ms, function() {
  step("toast", function() { toast("测试提示"); });
  step("refresh", function() { refresh(); });
  step("confirm", function() { doCmd("remove", "a3"); doCmd("confirm-ok", ""); });
  step("settings", function() { doCmd("settings", ""); T("webhook", $(#s-webhook).value); T("autorun", isOn($(#s-autorun))); T("cpu limit", $(#s-cpu).value); T("deps a1", (function(){ doCmd("close",""); doCmd("edit","a1"); return $(#f-deps).value + " mode=" + editMode; })()); });
  step("close", function() { doCmd("close", ""); });
  step("stop", function() { doCmd("stop", "a1"); T("stop confirm", $(#dlg-confirm).attributes.hasClass("open")); doCmd("close", ""); });
  shot("main");
  return false;
});
self.timer(2000ms, function() { step("ed", function(){ doCmd("edit", "a1"); }); shot("editor"); return false; });
self.timer(3000ms, function() { step("st", function(){ doCmd("close", ""); doCmd("settings", ""); }); shot("settings"); return false; });
self.timer(4000ms, function() { step("cf", function(){ doCmd("close", ""); doCmd("remove", "a2"); }); shot("confirm"); return false; });
self.timer(5000ms, function() { step("tt", function(){ doCmd("close", ""); toast("测试消息已发送，结果见事件日志"); }); shot("toast"); return false; });
self.timer(6000ms, function() {
  step("imp", function() {
    doCmd("close", ""); doCmd("import", "");
    scanImport();
    T("imp rows", impList.length);
    doCmd("imp-pick", "c1"); doCmd("imp-pick", "c3");
    T("imp picked", impSelected().join(","));
    T("imp btn", $(#imp-go).text);
    doCmd("imp-all", ""); T("imp all", impSelected().length);
    doCmd("imp-none", ""); T("imp none", impSelected().length);
    doCmd("imp-pick", "c2"); doCmd("imode", "immediate");
  });
  return false;
});
self.timer(7000ms, function() { step("imp-go", function(){ doCmd("imp-go", ""); T("imp closed", !$(#dlg-imp).attributes.hasClass("open")); }); return false; });
self.timer(7500ms, function() { step("max", function(){ doCmd("win-max", ""); T("maxed", self.attributes.hasClass("maxed")); doCmd("win-max", ""); T("restored", !self.attributes.hasClass("maxed")); }); return false; });
self.timer(8000ms, function() { view.report("DONE"); return false; });
`

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func main() {
	html, _ := filepath.Abs(os.Args[1])
	// 默认放到外接显示器（主屏左上方），可用 PREVIEW_X / PREVIEW_Y 覆盖
	x, y := envInt("PREVIEW_X", -1480), envInt("PREVIEW_Y", -1000)
	w, err := window.New(sciter.SW_TITLEBAR|sciter.SW_RESIZEABLE|sciter.SW_CONTROLS|sciter.SW_MAIN|sciter.SW_ENABLE_DEBUG, sciter.NewRect(y, x, 1180, 760))
	if err != nil {
		panic(err)
	}
	ok := func(msg string) *sciter.Value { return toValue(m{"ok": true, "msg": msg}) }
	w.DefineFunction("getState", func(a ...*sciter.Value) *sciter.Value { return toValue(fakeState()) })
	w.DefineFunction("saveApp", func(a ...*sciter.Value) *sciter.Value {
		return toValue(m{"ok": false, "msg": fmt.Sprintf("请填写程序路径（收到 %d 个参数，path=%q）", len(a), a[2].String())})
	})
	for _, n := range []string{"removeApp", "startApp", "stopApp", "restartApp", "startAll", "saveSettings", "testPush", "openFolder", "openLogs", "hideWindow", "quitApp"} {
		name := n
		w.DefineFunction(name, func(a ...*sciter.Value) *sciter.Value {
			fmt.Println("CALL", name, len(a))
			return ok(name + " ok")
		})
	}
	w.DefineFunction("scanApps", func(a ...*sciter.Value) *sciter.Value {
		return toValue(m{"ok": true, "user": "Administrator", "hidden": 14, "list": fakeCandidates()})
	})
	w.DefineFunction("importApps", func(a ...*sciter.Value) *sciter.Value {
		fmt.Println("CALL importApps", a[0].String(), a[1].String(), a[2].Bool())
		return ok("已导入 1 个程序")
	})
	maxed := false
	w.DefineFunction("winCtl", func(a ...*sciter.Value) *sciter.Value {
		if a[0].String() == "max" {
			maxed = !maxed
		}
		return sciter.NewValue(maxed)
	})
	w.DefineFunction("isMaximized", func(a ...*sciter.Value) *sciter.Value { return sciter.NewValue(maxed) })
	w.DefineFunction("toPath", func(a ...*sciter.Value) *sciter.Value { return sciter.NewValue(a[0].String()) })
	w.DefineFunction("baseName", func(a ...*sciter.Value) *sciter.Value { return sciter.NewValue("x") })
	w.DefineFunction("saveShot", func(a ...*sciter.Value) *sciter.Value {
		out := filepath.Join(os.Args[2], a[0].String()+".png")
		err := os.WriteFile(out, a[1].Bytes(), 0644)
		fmt.Println("SHOT", out, len(a[1].Bytes()), err)
		return sciter.NewValue()
	})
	w.DefineFunction("report", func(a ...*sciter.Value) *sciter.Value {
		s := a[0].String()
		fmt.Println("REPORT", s)
		if len(s) > 5 && s[:5] == "SHOT " && len(os.Args) > 2 {
			go func(name string) {
				time.Sleep(300 * time.Millisecond)
				out := filepath.Join(os.Args[2], name+".png")
				err := exec.Command("screencapture", "-x", "-R", "0,0,1300,900", out).Run()
				fmt.Println("SHOOT", name, err)
			}(s[5:])
		}
		if s == "DONE" {
			go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
		}
		return sciter.NewValue()
	})
	// macOS 上没有微软雅黑，预览时换成苹方
	src, err := os.ReadFile(html)
	if err != nil {
		panic(err)
	}
	page := strings.Replace(string(src), `font-family: "Microsoft YaHei UI"`, `font-family: "PingFang SC", "Microsoft YaHei UI"`, 1)
	if err := w.LoadHtml(page, "file://"+html); err != nil {
		panic(err)
	}
	if cmd := os.Getenv("PREVIEW_CMD"); cmd != "" { // 例：doCmd("edit","a1")
		w.Eval("self.timer(600ms, function() { " + cmd + "; return false; });")
	}
	if len(os.Args) > 2 { // 传入截图目录时执行自动化检查
		if _, ok := w.Eval(testScript); !ok {
			fmt.Println("EVAL FAILED")
		}
	}
	w.SetTitle("进程守护 Watchdog")
	w.Show()
	w.Run()
}
