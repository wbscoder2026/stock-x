package baostock

import (
	"hash/crc32"
	"strconv"
	"strings"
	"testing"
)

func TestEncodeHeaderLen21(t *testing.T) {
	h := encodeHeader("00", 0)
	if len(h) != 21 {
		t.Fatalf("len=%d want 21 %q", len(h), h)
	}
	h = encodeHeader("95", 123456)
	if len(h) != 21 {
		t.Fatalf("len=%d want 21 %q", len(h), h)
	}
}

func TestEncodeFrameHasCRC(t *testing.T) {
	body := "login" + fieldSep + defaultUser
	frame := encodeFrame(msgLoginReq, body)
	headBody := encodeHeader(msgLoginReq, len(body)) + body
	sum := crc32.ChecksumIEEE([]byte(headBody))
	crc := strconv.FormatUint(uint64(sum), 10)
	if !strings.Contains(string(frame), crc) {
		t.Fatalf("frame 未含 CRC %s: %q", crc, frame)
	}
	if !strings.HasSuffix(string(frame), fieldSep+crc+"\n") {
		t.Fatalf("CRC 位置不对: %q", frame)
	}
}

func TestToBSCode(t *testing.T) {
	if got := ToBSCode("600000"); got != "sh.600000" {
		t.Fatalf("ToBSCode(600000)=%s", got)
	}
	if got := ToBSCode("000001"); got != "sz.000001" {
		t.Fatalf("ToBSCode(000001)=%s", got)
	}
}

func TestParseFrameUncompressed(t *testing.T) {
	parts := []string{"0", "success", "login", "uid42"}
	body := strings.Join(parts, fieldSep) + cdataSuffix
	raw := []byte(encodeHeader("01", len(body)) + body)
	mt, got, err := parseFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mt != "01" {
		t.Fatalf("msgType=%s", mt)
	}
	if len(got) < 4 || got[0] != "0" || got[3] != "uid42" {
		t.Fatalf("body=%v", got)
	}
}
