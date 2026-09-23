package notify

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"
)

// desktopRunner 便于测试替换（默认真的发系统通知）
var desktopRunner = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// windowsPowerShell 找一个能跑 WinRT 的 PowerShell：
// 优先 Windows PowerShell 5.1（`powershell`，WinRT 互操作现成可用），
// 其次 pwsh（装了 Microsoft.Windows.SDK.NET 才行，找不到就当没有）。
func windowsPowerShell() string {
	if p, err := exec.LookPath("powershell"); err == nil {
		return p
	}
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	return ""
}

// DesktopSupported 当前系统是否支持本地系统通知。
func DesktopSupported() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	case "windows":
		return windowsPowerShell() != ""
	}
	return false
}

// NotifyDesktop 发一条本机系统通知
// （macOS 通知中心 / Linux notify-send / Windows 操作中心 Toast）。
// 不依赖浏览器，服务端在跑就能弹；sound 为空则静音。
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
	case "windows":
		ps := windowsPowerShell()
		if ps == "" {
			return fmt.Errorf("缺少 powershell")
		}
		// 用 -EncodedCommand 传脚本：省掉 cmd/引号两层转义，
		// 标题正文也不直接进脚本，而是 base64 进 XML，任何字符都不会破坏语法。
		return desktopRunner(ps,
			"-NoProfile", "-NonInteractive", "-EncodedCommand",
			encodePowerShell(windowsToastScript(title, body, sound)))
	}
	return fmt.Errorf("当前系统 %s 不支持系统通知", runtime.GOOS)
}

// 运行时 WinRT Toast 需要知道「是谁在弹」。空 AppID（CreateToastNotifier()）
// 在没注册过的进程上会直接失败，所以用 Windows 自带的 PowerShell 快捷方式
// AppID —— 它在系统里已注册，这是各工具（win10toast 等）通用的做法。
const windowsToastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// windowsToastScript 生成发一条 Toast 的 PowerShell 脚本。
func windowsToastScript(title, body, sound string) string {
	xmlB64 := base64.StdEncoding.EncodeToString([]byte(toastXML(title, body, sound)))
	return `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + xmlB64 + `')))
$toast = New-Object Windows.UI.Notifications.ToastNotification -ArgumentList $xml
$appId = '` + windowsToastAppID + `'
try { [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId).Show($toast) }
catch { [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier().Show($toast) }
`
}

// toastXML Toast 内容：标题 + 正文 + 有无提示音。
func toastXML(title, body, sound string) string {
	audio := `<audio silent="true"/>`
	if strings.TrimSpace(sound) != "" {
		audio = `<audio src="ms-winsoundevent:Notification.Default"/>`
	}
	texts := `<text>` + xmlEscape(title) + `</text>`
	if body != "" {
		texts += `<text>` + xmlEscape(body) + `</text>`
	}
	return `<toast><visual><binding template="ToastGeneric">` + texts +
		`</binding></visual>` + audio + `</toast>`
}

// encodePowerShell PowerShell -EncodedCommand 要求 UTF-16LE 的 base64。
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		buf[i*2] = byte(u)
		buf[i*2+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}

// osaQuote 转义 AppleScript 字符串字面量
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
