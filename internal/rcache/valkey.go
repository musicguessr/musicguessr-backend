package rcache

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// valkeyClient is a minimal RESP2 client supporting only the handful of
// commands this package needs (AUTH, SELECT, GET, SET ... EX, PING) —
// matches this codebase's stdlib-only convention (see the hand-rolled S3
// SigV4 signer in internal/deckstore) rather than pulling in a full
// Redis/Valkey driver dependency for four commands.
type valkeyClient struct {
	addr     string
	password string
	db       int

	dialTimeout time.Duration
	cmdTimeout  time.Duration

	mu   sync.Mutex
	pool []*valkeyConn
}

type valkeyConn struct {
	nc net.Conn
	br *bufio.Reader
}

func newValkeyClient(addr, password string, db int) *valkeyClient {
	return &valkeyClient{
		addr:        addr,
		password:    password,
		db:          db,
		dialTimeout: 3 * time.Second,
		cmdTimeout:  2 * time.Second,
	}
}

const valkeyPoolMax = 8

func (c *valkeyClient) getConn(ctx context.Context) (*valkeyConn, error) {
	c.mu.Lock()
	if n := len(c.pool); n > 0 {
		vc := c.pool[n-1]
		c.pool = c.pool[:n-1]
		c.mu.Unlock()
		return vc, nil
	}
	c.mu.Unlock()

	d := net.Dialer{Timeout: c.dialTimeout}
	nc, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("rcache: valkey dial: %w", err)
	}
	vc := &valkeyConn{nc: nc, br: bufio.NewReader(nc)}

	// A connection that completes the TCP handshake but then never answers
	// AUTH/SELECT (overloaded server, half-open network path, etc.) would
	// otherwise block this goroutine on an un-deadlined socket read forever
	// — DialContext only bounds the dial itself. Reuse the same cmdTimeout/
	// ctx-deadline policy Get/Set apply to the actual command.
	done := c.withDeadline(ctx, vc)
	defer done()

	if c.password != "" {
		if _, err := c.exec(vc, "AUTH", c.password); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("rcache: valkey auth: %w", err)
		}
	}
	if c.db != 0 {
		if _, err := c.exec(vc, "SELECT", strconv.Itoa(c.db)); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("rcache: valkey select: %w", err)
		}
	}
	return vc, nil
}

// putConn returns a connection to the pool, or discards it (closing the
// socket) if the pool is full or the connection is known-bad after a
// protocol/network error — a connection can't be reused once its request
// stream is out of sync or its socket is closed.
func (c *valkeyClient) putConn(vc *valkeyConn, healthy bool) {
	if !healthy {
		_ = vc.nc.Close()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pool) >= valkeyPoolMax {
		_ = vc.nc.Close()
		return
	}
	c.pool = append(c.pool, vc)
}

// exec sends one RESP2 command and returns its parsed reply. It does not
// manage the connection's deadline — callers set that via context in Get/Set.
func (c *valkeyClient) exec(vc *valkeyConn, args ...string) (respValue, error) {
	if err := writeCommand(vc.nc, args); err != nil {
		return respValue{}, err
	}
	return readReply(vc.br)
}

func (c *valkeyClient) withDeadline(ctx context.Context, vc *valkeyConn) func() {
	deadline := time.Now().Add(c.cmdTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = vc.nc.SetDeadline(deadline)
	return func() { _ = vc.nc.SetDeadline(time.Time{}) }
}

// Get returns the raw value for key, or ok=false if it doesn't exist.
func (c *valkeyClient) Get(ctx context.Context, key string) (data []byte, ok bool, err error) {
	vc, err := c.getConn(ctx)
	if err != nil {
		return nil, false, err
	}
	done := c.withDeadline(ctx, vc)
	v, err := c.exec(vc, "GET", key)
	done()
	if err != nil {
		c.putConn(vc, false)
		return nil, false, err
	}
	c.putConn(vc, true)
	if v.isNil {
		return nil, false, nil
	}
	return []byte(v.str), true, nil
}

// Set stores value under key with the given TTL (SET key value EX seconds).
func (c *valkeyClient) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	vc, err := c.getConn(ctx)
	if err != nil {
		return err
	}
	secs := int64(ttl.Seconds())
	if secs < 1 {
		secs = 1
	}
	done := c.withDeadline(ctx, vc)
	_, err = c.exec(vc, "SET", key, string(value), "EX", strconv.FormatInt(secs, 10))
	done()
	if err != nil {
		c.putConn(vc, false)
		return err
	}
	c.putConn(vc, true)
	return nil
}

// --- minimal RESP2 wire protocol ---

func writeCommand(w net.Conn, args []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	_, err := w.Write([]byte(b.String()))
	return err
}

type respValue struct {
	str   string
	isNil bool
}

var errRespProtocol = errors.New("rcache: malformed valkey reply")

// readReply parses one RESP2 reply. Only the reply types this client's
// commands can receive are handled: simple strings (+OK), errors (-ERR ...),
// integers (:N), and bulk strings ($N ... or $-1 for nil) — arrays are never
// returned by AUTH/SELECT/GET/SET/PING so array parsing isn't implemented.
func readReply(r *bufio.Reader) (respValue, error) {
	line, err := readLine(r)
	if err != nil {
		return respValue{}, err
	}
	if len(line) == 0 {
		return respValue{}, errRespProtocol
	}
	switch line[0] {
	case '+':
		return respValue{str: line[1:]}, nil
	case '-':
		return respValue{}, fmt.Errorf("rcache: valkey error: %s", line[1:])
	case ':':
		return respValue{str: line[1:]}, nil
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return respValue{}, errRespProtocol
		}
		if n < 0 {
			return respValue{isNil: true}, nil
		}
		buf := make([]byte, n+2) // +2 for trailing \r\n
		if _, err := readFull(r, buf); err != nil {
			return respValue{}, err
		}
		return respValue{str: string(buf[:n])}, nil
	default:
		return respValue{}, errRespProtocol
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
