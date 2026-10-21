package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
)

const maxLogSize = 10 << 20

// lineRing 保存被监控程序最近的输出行，同时追加写入日志文件
type lineRing struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
	logPath string
	file    *os.File
}

func newLineRing(max int, logPath string) *lineRing {
	return &lineRing{max: max, logPath: logPath}
}

func (r *lineRing) openLog() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil || r.logPath == "" {
		return
	}
	if st, err := os.Stat(r.logPath); err == nil && st.Size() > maxLogSize {
		os.Rename(r.logPath, r.logPath+".1")
	}
	r.file, _ = os.OpenFile(r.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
}

func (r *lineRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		r.file.Write(p)
	}
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.push(r.partial[:i])
		r.partial = r.partial[i+1:]
	}
	if len(r.partial) > 4096 { // 超长无换行内容也算一行
		r.push(r.partial)
		r.partial = nil
	}
	return len(p), nil
}

func (r *lineRing) push(b []byte) {
	s := strings.TrimRight(decodeText(b), "\r ")
	if strings.TrimSpace(s) == "" {
		return
	}
	r.lines = append(r.lines, s)
	if len(r.lines) > r.max {
		r.lines = append(r.lines[:0:0], r.lines[len(r.lines)-r.max:]...)
	}
}

func (r *lineRing) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.partial) > 0 {
		r.push(r.partial)
		r.partial = nil
	}
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
}

// tail 取最后 n 行
func (r *lineRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.lines) {
		n = len(r.lines)
	}
	return append([]string(nil), r.lines[len(r.lines)-n:]...)
}

func (r *lineRing) reset() {
	r.mu.Lock()
	r.lines = nil
	r.partial = nil
	r.mu.Unlock()
}
