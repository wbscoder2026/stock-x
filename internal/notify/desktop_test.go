package notify

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSendCard(t *testing.T) {
	if err := SendCard("", "标题", []string{"a"}, ""); err != nil {
		t.Fatalf("空 webhook 应视为未配置：%v", err)
	}

	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type=%s", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	lines := []string{"📈 **焦煤 JM** 向上突破 Donchian高(20根)　现价 1523.0 / 关键位 1516.0", "📈 **螺纹钢 RB** 向上突破 ORB高(开盘30分钟)"}
	if err := SendCard(srv.URL, "⚡ 期货突破 2 条", lines, "red"); err != nil {
		t.Fatal(err)
	}
	if payload["msg_type"] != "interactive" {
		t.Fatalf("msg_type=%v", payload["msg_type"])
	}
	card, _ := payload["card"].(map[string]any)
	header, _ := card["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	if title["content"] != "⚡ 期货突破 2 条" || header["template"] != "red" {
		t.Fatalf("卡片头不对：%+v", header)
	}
	elements, _ := card["elements"].([]any)
	first, _ := elements[0].(map[string]any)
	text, _ := first["text"].(map[string]any)
	content, _ := text["content"].(string)
	if !strings.Contains(content, "焦煤") || !strings.Contains(content, "螺纹钢") || !strings.Contains(content, "\n") {
		t.Fatalf("内容不对：%q", content)
	}
	if text["tag"] != "lark_md" {
		t.Fatalf("text.tag=%v", text["tag"])
	}
}

func TestSendCardErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":9499,"msg":"hook 失效"}`))
	}))
	t.Cleanup(srv.Close)
	if err := SendCard(srv.URL, "t", nil, ""); err == nil || !strings.Contains(err.Error(), "9499") {
		t.Fatalf("错误码应报错：%v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	if err := SendCard(bad.URL, "t", nil, ""); err == nil {
		t.Fatal("HTTP 500 应报错")
	}
}

func TestNotifyDesktopCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AppleScript 断言只在 macOS 有意义")
	}
	var gotName string
	var gotArgs []string
	orig := desktopRunner
	desktopRunner = func(name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	}
	t.Cleanup(func() { desktopRunner = orig })

	if err := NotifyDesktop(`焦"煤`, "向上突破 1523.0", "Glass"); err != nil {
		t.Fatal(err)
	}
	if gotName != "osascript" || len(gotArgs) != 2 {
		t.Fatalf("命令不对：%s %v", gotName, gotArgs)
	}
	script := gotArgs[1]
	if !strings.Contains(script, `sound name "Glass"`) {
		t.Fatalf("没带提示音：%s", script)
	}
	if !strings.Contains(script, `title "焦\"煤"`) {
		t.Fatalf("引号没转义：%s", script)
	}
	if !strings.Contains(script, `display notification "向上突破 1523.0"`) {
		t.Fatalf("正文不对：%s", script)
	}

	// 无声音时不该带 sound；空标题要报错
	if err := NotifyDesktop("t", "b", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotArgs[1], "sound name") {
		t.Fatalf("不该带提示音：%s", gotArgs[1])
	}
	if err := NotifyDesktop("", "b", ""); err == nil {
		t.Fatal("空标题应报错")
	}
}

func TestDesktopSupported(t *testing.T) {
	// 只要求不 panic 且与当前系统一致（CI 上可能是 linux 无 notify-send）
	_ = DesktopSupported()
}

// decodePowerShell 还原 -EncodedCommand 的内容（UTF-16LE 的 base64）。
func decodePowerShell(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("不是合法 base64：%v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE 长度应为偶数：%d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	return string(utf16.Decode(units))
}

// innerXML 从生成的脚本里解出真正喂给 WinRT 的 Toast XML。
func innerXML(t *testing.T, script string) string {
	t.Helper()
	const open = `FromBase64String('`
	i := strings.Index(script, open)
	if i < 0 {
		t.Fatalf("脚本里没有 base64 的 XML：%s", script)
	}
	rest := script[i+len(open):]
	j := strings.Index(rest, `')`)
	if j < 0 {
		t.Fatalf("base64 没闭合：%s", script)
	}
	raw, err := base64.StdEncoding.DecodeString(rest[:j])
	if err != nil {
		t.Fatalf("XML base64 解不开：%v", err)
	}
	return string(raw)
}

func TestWindowsToastScript(t *testing.T) {
	script := windowsToastScript(`焦"煤 & <测试>`, `向上突破 <1523.0>`, "Glass")
	if !strings.Contains(script, "CreateToastNotifier") {
		t.Fatalf("没调 CreateToastNotifier：%s", script)
	}
	xml := innerXML(t, script)
	if !strings.Contains(xml, `<text>焦&quot;煤 &amp; &lt;测试&gt;</text>`) {
		t.Fatalf("标题没做 XML 转义：%s", xml)
	}
	if !strings.Contains(xml, `向上突破 &lt;1523.0&gt;`) {
		t.Fatalf("正文没做 XML 转义：%s", xml)
	}
	if !strings.Contains(xml, `template="ToastGeneric"`) {
		t.Fatalf("模板不对：%s", xml)
	}
	if !strings.Contains(xml, "ms-winsoundevent:Notification.Default") {
		t.Fatalf("给了 sound 就该带提示音：%s", xml)
	}
}

func TestWindowsToastSilentWithoutSound(t *testing.T) {
	xml := innerXML(t, windowsToastScript("标题", "正文", ""))
	if !strings.Contains(xml, `<audio silent="true"/>`) {
		t.Fatalf("sound 为空应静音：%s", xml)
	}
	if strings.Contains(xml, "ms-winsoundevent") {
		t.Fatalf("不该带提示音：%s", xml)
	}
}

func TestEncodePowerShellRoundTrip(t *testing.T) {
	if got := decodePowerShell(t, encodePowerShell("Write-Host '焦煤'")); got != "Write-Host '焦煤'" {
		t.Fatalf("UTF-16LE 往返不一致：%q", got)
	}
}

func TestNotifyDesktopWindowsCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell 断言只在 Windows 有意义")
	}
	var gotName string
	var gotArgs []string
	orig := desktopRunner
	desktopRunner = func(name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	}
	t.Cleanup(func() { desktopRunner = orig })

	if err := NotifyDesktop("焦煤 JM", "向上突破 1523.0", "Glass"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(gotName), "powershell") {
		t.Fatalf("应该用 powershell：%s", gotName)
	}
	if len(gotArgs) != 4 || gotArgs[2] != "-EncodedCommand" {
		t.Fatalf("参数不对：%v", gotArgs)
	}
	script := decodePowerShell(t, gotArgs[3])
	if !strings.Contains(script, "ToastNotificationManager") {
		t.Fatalf("脚本里没有 Toast：%s", script)
	}
	if xml := innerXML(t, script); !strings.Contains(xml, "焦煤 JM") {
		t.Fatalf("标题没进 XML：%s", xml)
	}
	if err := NotifyDesktop("", "b", ""); err == nil {
		t.Fatal("空标题应报错")
	}
}
