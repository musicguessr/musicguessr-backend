package rcache

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeValkeyServer is a minimal in-process RESP2 server implementing just
// enough of GET/SET/AUTH/SELECT to exercise valkeyClient's wire protocol
// without requiring a real Valkey/Redis instance in CI.
func fakeValkeyServer(t *testing.T, wantPassword string) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	store := map[string]string{}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				for {
					args, err := readCommand(br)
					if err != nil {
						return
					}
					if len(args) == 0 {
						continue
					}
					switch strings.ToUpper(args[0]) {
					case "AUTH":
						if len(args) == 2 && args[1] == wantPassword && wantPassword != "" {
							_, _ = conn.Write([]byte("+OK\r\n"))
						} else {
							_, _ = conn.Write([]byte("-ERR invalid password\r\n"))
						}
					case "SELECT":
						_, _ = conn.Write([]byte("+OK\r\n"))
					case "SET":
						if len(args) < 3 {
							_, _ = conn.Write([]byte("-ERR wrong args\r\n"))
							continue
						}
						store[args[1]] = args[2]
						_, _ = conn.Write([]byte("+OK\r\n"))
					case "GET":
						if len(args) < 2 {
							_, _ = conn.Write([]byte("-ERR wrong args\r\n"))
							continue
						}
						v, ok := store[args[1]]
						if !ok {
							_, _ = conn.Write([]byte("$-1\r\n"))
							continue
						}
						_, _ = conn.Write([]byte("$" + itoa(len(v)) + "\r\n" + v + "\r\n"))
					default:
						_, _ = conn.Write([]byte("-ERR unknown command\r\n"))
					}
				}
			}()
		}
	}()

	return ln.Addr().String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// readCommand parses one RESP2 array-of-bulk-strings command, mirroring how
// a real server reads what writeCommand sends.
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, errRespProtocol
	}
	var n int
	for _, c := range line[1:] {
		n = n*10 + int(c-'0')
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := readLine(r)
		if err != nil {
			return nil, err
		}
		if len(hdr) == 0 || hdr[0] != '$' {
			return nil, errRespProtocol
		}
		var size int
		for _, c := range hdr[1:] {
			size = size*10 + int(c-'0')
		}
		buf := make([]byte, size+2)
		if _, err := readFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func TestValkeyClient_SetGet(t *testing.T) {
	addr := fakeValkeyServer(t, "")
	c := newValkeyClient(addr, "", 0)
	ctx := context.Background()

	if err := c.Set(ctx, "mykey", []byte("hello world"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	data, ok, err := c.Get(ctx, "mykey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected key to be found")
	}
	if string(data) != "hello world" {
		t.Errorf("got %q, want %q", data, "hello world")
	}
}

func TestValkeyClient_GetMiss(t *testing.T) {
	addr := fakeValkeyServer(t, "")
	c := newValkeyClient(addr, "", 0)
	ctx := context.Background()

	_, ok, err := c.Get(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("expected miss, got hit")
	}
}

func TestValkeyClient_Auth(t *testing.T) {
	addr := fakeValkeyServer(t, "secret123")
	ctx := context.Background()

	good := newValkeyClient(addr, "secret123", 0)
	if err := good.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set with correct password: %v", err)
	}

	bad := newValkeyClient(addr, "wrong", 0)
	if err := bad.Set(ctx, "k", []byte("v"), time.Minute); err == nil {
		t.Error("expected error with wrong password, got nil")
	}
}

func TestValkeyClient_ConnectionReuse(t *testing.T) {
	addr := fakeValkeyServer(t, "")
	c := newValkeyClient(addr, "", 0)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set #%d: %v", i, err)
		}
		if _, _, err := c.Get(ctx, "k"); err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
	}
	c.mu.Lock()
	pooled := len(c.pool)
	c.mu.Unlock()
	if pooled == 0 {
		t.Error("expected at least one connection to be pooled after sequential requests")
	}
}
