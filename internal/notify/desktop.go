package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// desktopRunner 便于测试替换（默认真的发系统通知）
var desktopRunner = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// DesktopSupported 当前系统是否支持本地系统通知。
func DesktopSupported() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	}
	return false
}

// NotifyDesktop 发一条本机系统通知（macOS 通知中心 / Linux notify-send）。
// 不依赖浏览器，服务端在跑就能弹；macOS 用 sound 指定提示音（空则无声）。
func NotifyDesktop(title, body, sound string) error {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return fmt.Errorf("标题为空")
	}
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s", osaQuote(body), osaQuote(title))
		if strings.TrimSpace(sound) != "" {
			script += fmt.Sprintf(" sound name %s", osaQuote(sound))
		}
		return desktopRunner("osascript", "-e", script)
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return fmt.Errorf("缺少 notify-send")
		}
		return desktopRunner("notify-send", title, body)
	}
	return fmt.Errorf("当前系统 %s 不支持系统通知", runtime.GOOS)
}

// osaQuote 转义 AppleScript 字符串字面量
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
