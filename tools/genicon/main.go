// genicon 生成 exe 图标所需的 PNG（供 go-winres 打包进资源）
package main

import (
	"image/png"
	"log"
	"os"
	"path/filepath"

	"watchdog/internal/icon"
)

func main() {
	out := "winres"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	os.MkdirAll(out, 0755)
	f, err := os.Create(filepath.Join(out, "icon.png"))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, icon.Render(256, icon.Idle)); err != nil {
		log.Fatal(err)
	}
}
