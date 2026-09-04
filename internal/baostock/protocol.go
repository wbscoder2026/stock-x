package baostock

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"
	"unicode"
)

const (
	DefaultAddr = "public-api.baostock.com:10030"

	clientVersion = "00.9.30"
	fieldSep      = "\x01"
	headerLen     = 21
	bodyLenWidth  = 10
	perPageCount  = 2000
	cdataSuffix   = "<![CDATA[]]>\n"

	msgLoginReq  = "00"
	msgLogoutReq = "02"
	msgBasicReq  = "45"
	msgKPlusReq  = "95"

	defaultUser = "anonymous"
	defaultPass = "123456"
	defaultOpt  = "0"
	kFields     = "date,open,high,low,close,volume,amount,turn"
)

func isCompressedType(msgType string) bool {
	switch msgType {
	case "96", "99", "9B", "9D":
		return true
	default:
		return false
	}
}

// encodeHeader 生成 21 字节消息头：版本 + 分隔 + 类型(2位) + 分隔 + 体长(10 位左补零)。
func encodeHeader(msgType string, bodyLen int) string {
	return clientVersion + fieldSep + msgType + fieldSep + fmt.Sprintf("%0*d", bodyLenWidth, bodyLen)
}

// encodeFrame 整帧：header+body + 分隔 + CRC32(IEEE，十进制) + 换行。
func encodeFrame(msgType, body string) []byte {
	headBody := encodeHeader(msgType, len(body)) + body
	sum := crc32.ChecksumIEEE([]byte(headBody))
	return []byte(headBody + fieldSep + strconv.FormatUint(uint64(sum), 10) + "\n")
}

func nextPageBody(reqBody string) (string, bool) {
	parts := strings.Split(reqBody, fieldSep)
	if len(parts) < 3 {
		return "", false
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", false
	}
	parts[2] = strconv.Itoa(page + 1)
	return strings.Join(parts, fieldSep), true
}

func stripCDATA(s string) string {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "<![CDATA[]]>")
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimRightFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	return s
}

func splitBody(s string) []string {
	return strings.Split(stripCDATA(s), fieldSep)
}

func parseFrame(raw []byte) (msgType string, body []string, err error) {
	if len(raw) < headerLen {
		return "", nil, fmt.Errorf("baostock: 响应过短")
	}
	header := string(raw[:headerLen])
	hp := strings.Split(header, fieldSep)
	if len(hp) < 3 {
		return "", nil, fmt.Errorf("baostock: 响应头无效")
	}
	msgType = hp[1]
	if isCompressedType(msgType) {
		n, convErr := strconv.Atoi(hp[2])
		if convErr != nil || n < 0 {
			return "", nil, fmt.Errorf("baostock: 压缩体长度无效")
		}
		end := headerLen + n
		if end > len(raw) {
			return "", nil, fmt.Errorf("baostock: 压缩体不完整")
		}
		zr, zerr := zlib.NewReader(bytes.NewReader(raw[headerLen:end]))
		if zerr != nil {
			return "", nil, fmt.Errorf("baostock: 解压失败: %w", zerr)
		}
		plain, rerr := io.ReadAll(zr)
		_ = zr.Close()
		if rerr != nil {
			return "", nil, fmt.Errorf("baostock: 解压失败: %w", rerr)
		}
		return msgType, splitBody(string(plain)), nil
	}
	return msgType, splitBody(string(raw[headerLen:])), nil
}

func compactJSON(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), "")
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// ToBSCode 把纯数字代码转为 baostock 代码：6/9→sh，4/8→bj，其余 sz。
func ToBSCode(symbol string) string {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return s
	}
	switch s[0] {
	case '6', '9':
		return "sh." + s
	case '4', '8':
		return "bj." + s
	default:
		return "sz." + s
	}
}

// SymbolFromBS 从 baostock 代码取出数字部分，如 sh.600000 → 600000。
func SymbolFromBS(code string) string {
	s := strings.TrimSpace(code)
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}
