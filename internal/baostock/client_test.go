package baostock

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoginAndQueries(t *testing.T) {
	addr := startFake(t, func(msgType string, body []string) []string {
		switch msgType {
		case msgLoginReq:
			return []string{"0", "success", "login", "u1"}
		case msgLogoutReq:
			return []string{"0", "success", "logout"}
		case msgBasicReq:
			rec := [][]string{
				{"sh.600000", "浦发银行", "1999-11-10", "", "1", "1"},
				{"sz.000001", "平安银行", "1991-04-03", "", "1", "1"},
				{"sh.000001", "上证指数", "", "", "2", "1"},
				{"sz.300001", "退市股", "", "", "1", "0"},
			}
			return okJSON(body, rec)
		case msgKPlusReq:
			rec := [][]string{
				{"2024-01-02", "10", "11", "9", "10.5", "1000", "1e4", "1.2"},
				{"2024-01-03", "10", "11", "9", "bad", "1000", "1e4", "1.2"},
				{"2024-01-04", "10", "11", "9", "10.5", "0", "0", "0"},
			}
			return okJSON(body, rec)
		default:
			return []string{"1", "unknown"}
		}
	})
	ctx := context.Background()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if c.currentUser() != "u1" {
		t.Fatalf("user=%s", c.currentUser())
	}
	stocks, err := c.QueryStockBasic(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 2 || stocks[0].Code != "sh.600000" || stocks[1].Code != "sz.000001" {
		t.Fatalf("stocks=%v", stocks)
	}
	bars, err := c.HistoryK(ctx, "600000", "2024-01-01", "2024-01-31", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Date != "2024-01-02" || bars[0].Close != 10.5 || bars[0].Volume != 1000 {
		t.Fatalf("bars=%v", bars)
	}
}

func TestLoginFailure(t *testing.T) {
	addr := startFake(t, func(msgType string, body []string) []string {
		return []string{"10001001", "用户名或密码错误"}
	})
	ctx := context.Background()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Login(ctx)
	if err == nil || !strings.Contains(err.Error(), "用户名或密码错误") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoginDefaultUserID(t *testing.T) {
	addr := startFake(t, func(msgType string, body []string) []string {
		return []string{"0", "ok"}
	})
	ctx := context.Background()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if c.currentUser() != defaultUser {
		t.Fatalf("user=%s", c.currentUser())
	}
}

func TestHistoryKPagination(t *testing.T) {
	addr := startFake(t, func(msgType string, body []string) []string {
		if msgType != msgKPlusReq {
			return []string{"0", "ok", "login", defaultUser}
		}
		page := 1
		if len(body) > 2 {
			page, _ = strconv.Atoi(body[2])
		}
		var rec [][]string
		n := perPageCount
		if page >= 2 {
			n = 3
		}
		for i := 0; i < n; i++ {
			rec = append(rec, []string{"2024-01-02", "1", "1", "1", "1", "10", "10", "1"})
		}
		return okJSON(body, rec)
	})
	ctx := context.Background()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	bars, err := c.HistoryK(ctx, "sz.000001", "2024-01-01", "2024-12-31", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != perPageCount+3 {
		t.Fatalf("len=%d", len(bars))
	}
}

func TestDialCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Dial(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("want error")
	}
}

func TestCloseNilConn(t *testing.T) {
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func okJSON(req []string, rec [][]string) []string {
	b, _ := json.Marshal(struct {
		Record [][]string `json:"record"`
	}{Record: rec})
	page, per := "1", strconv.Itoa(perPageCount)
	if len(req) > 2 {
		page = req[2]
	}
	if len(req) > 3 {
		per = req[3]
	}
	method := ""
	if len(req) > 0 {
		method = req[0]
	}
	return []string{"0", "success", method, defaultUser, page, per, string(b)}
}

func startFake(t *testing.T, handle func(msgType string, body []string) []string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				for {
					mt, body, err := readRequest(c)
					if err != nil {
						return
					}
					fields := handle(mt, body)
					payload := strings.Join(fields, fieldSep) + cdataSuffix
					_, _ = c.Write([]byte(encodeHeader("01", len(payload)) + payload))
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func readRequest(r io.Reader) (msgType string, body []string, err error) {
	header := make([]byte, headerLen)
	if _, err = io.ReadFull(r, header); err != nil {
		return "", nil, err
	}
	hp := strings.Split(string(header), fieldSep)
	if len(hp) < 3 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n, convErr := strconv.Atoi(hp[2])
	if convErr != nil || n < 0 {
		return "", nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(r, buf); err != nil {
		return "", nil, err
	}
	if _, err = readUntilByte(r, '\n'); err != nil {
		return "", nil, err
	}
	return hp[1], strings.Split(string(buf), fieldSep), nil
}

func readUntilByte(r io.Reader, delim byte) ([]byte, error) {
	var out []byte
	one := make([]byte, 1)
	for {
		_, err := r.Read(one)
		if err != nil {
			return out, err
		}
		out = append(out, one[0])
		if one[0] == delim {
			return out, nil
		}
	}
}
