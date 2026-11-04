package speedlayer

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// resp.go is a deliberately minimal Redis/Valkey client: the speed layer
// needs a dozen commands, and pulling the full go-redis module graph into
// a binary that ships to a 1 vCPU box for those dozen is the heavier
// choice (repo AGENTS.md §4.F — measured cost over assumed cost). What
// lives here: RESP2 encoding/decoding, AUTH/SELECT on connect, and a
// small connection pool. Nothing else speaks the protocol.

// respError is a server-side error reply (a "-" line).
type respError string

func (e respError) Error() string { return string(e) }

type respClient struct {
	addr     string
	password string
	dbIndex  int
	useTLS   bool

	mu   sync.Mutex
	idle []*respConn
}

const (
	respPoolMax    = 8
	respDialTO     = 2 * time.Second
	respCommandTO  = 2 * time.Second
	respMaxBulkLen = 64 << 20 // a reply larger than this is a corrupt stream, not data
)

type respConn struct {
	conn net.Conn
	r    *bufio.Reader
}

// do runs one command and returns its decoded reply:
// string (+/$), int64 (:), []any (*), or nil (null bulk/array).
func (c *respClient) do(ctx context.Context, args ...any) (any, error) {
	rc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	v, err := rc.roundTrip(ctx, args)
	if err != nil {
		rc.close()
		return nil, err
	}
	c.release(rc)
	return v, nil
}

// doPair runs two commands back-to-back on one connection (the counter
// write + its TTL refresh) and returns the first reply.
func (c *respClient) doPair(ctx context.Context, first, second []any) (any, error) {
	rc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	v, err := rc.roundTrip(ctx, first)
	if err != nil {
		rc.close()
		return nil, err
	}
	if _, err := rc.roundTrip(ctx, second); err != nil {
		rc.close()
		return nil, err
	}
	c.release(rc)
	return v, nil
}

func (c *respClient) acquire(ctx context.Context) (*respConn, error) {
	c.mu.Lock()
	if n := len(c.idle); n > 0 {
		rc := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		return rc, nil
	}
	c.mu.Unlock()
	return c.dial(ctx)
}

func (c *respClient) release(rc *respConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.idle) >= respPoolMax {
		rc.close()
		return
	}
	c.idle = append(c.idle, rc)
}

func (c *respClient) dial(ctx context.Context) (*respConn, error) {
	d := net.Dialer{Timeout: respDialTO}
	var (
		conn net.Conn
		err  error
	)
	if c.useTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", c.addr, &tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", c.addr)
	}
	if err != nil {
		return nil, fmt.Errorf("redis dial %s: %w", c.addr, err)
	}
	rc := &respConn{conn: conn, r: bufio.NewReader(conn)}
	if c.password != "" {
		if _, err := rc.roundTrip(ctx, []any{"AUTH", c.password}); err != nil {
			rc.close()
			return nil, fmt.Errorf("redis auth: %w", err)
		}
	}
	if c.dbIndex > 0 {
		if _, err := rc.roundTrip(ctx, []any{"SELECT", c.dbIndex}); err != nil {
			rc.close()
			return nil, fmt.Errorf("redis select db %d: %w", c.dbIndex, err)
		}
	}
	return rc, nil
}

func (rc *respConn) close() { _ = rc.conn.Close() }

// roundTrip writes one command and reads its reply. Any error poisons
// the connection — the caller closes it rather than returning it to the
// pool, because a half-read reply would desynchronise the stream.
func (rc *respConn) roundTrip(ctx context.Context, args []any) (any, error) {
	deadline := time.Now().Add(respCommandTO)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = rc.conn.SetDeadline(deadline)
	if err := writeCommand(rc.conn, args); err != nil {
		return nil, err
	}
	return readReply(rc.r)
}

func writeCommand(w io.Writer, args []any) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		switch v := a.(type) {
		case string:
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(v), v)
		case int:
			s := strconv.Itoa(v)
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(s), s)
		case int64:
			s := strconv.FormatInt(v, 10)
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(s), s)
		default:
			return fmt.Errorf("redis: unsupported argument type %T", a)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func readReply(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, respError(line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("redis: bad bulk length: %w", err)
		}
		if n < 0 {
			return nil, nil
		}
		if n > respMaxBulkLen {
			return nil, fmt.Errorf("redis: bulk reply too large (%d bytes)", n)
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("redis: bad array length: %w", err)
		}
		if n < 0 {
			return nil, nil
		}
		items := make([]any, 0, n)
		for i := 0; i < n; i++ {
			item, err := readReply(r)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("redis: unexpected reply prefix %q", line[0])
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// replyString extracts a string reply, tolerating the integer form some
// commands return for numeric values.
func replyString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	default:
		return "", false
	}
}
