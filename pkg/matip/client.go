package matip

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Client keeps a MATIP session up to a listener, redialing with backoff, the
// way an airline host keeps its circuit to a network up. It mirrors the plain
// transport client's contract so an assembly can swap one for the other: Run
// blocks until the context ends, Send fails cleanly while the session is
// down, and cancellation takes an idle session down promptly.
type Client struct {
	Addr   string
	Config Config
	Log    *slog.Logger
	// OnMessage handles each Type B message the session delivers.
	OnMessage func(ctx context.Context, raw []byte) error
	// OnUp is called each time a session is established.
	OnUp func()

	mu   sync.Mutex
	sess *Session
}

// Run dials and keeps the session up until ctx is cancelled. Reconnection
// backs off to a ceiling rather than retrying immediately.
func (c *Client) Run(ctx context.Context) error {
	backoff := 200 * time.Millisecond
	const maxBackoff = 15 * time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log().Warn("matip session ended", "addr", c.Addr, "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) session(ctx context.Context) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return err
	}
	// Receive only notices cancellation when a packet arrives, and an idle
	// session sees none. Closing the conn is what actually takes it down.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	sess, err := Dial(conn, c.Config)
	if err != nil {
		conn.Close()
		return err
	}
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	c.log().Info("matip session up", "addr", c.Addr)
	if c.OnUp != nil {
		c.OnUp()
	}

	var rerr error
	for {
		raw, err := sess.Receive()
		if err != nil {
			rerr = err
			break
		}
		if c.OnMessage != nil {
			if herr := c.OnMessage(ctx, raw); herr != nil {
				c.log().Error("matip handler failed", "err", herr)
			}
		}
	}

	c.mu.Lock()
	if c.sess == sess {
		c.sess = nil
	}
	c.mu.Unlock()
	sess.Close(CloseNormal) //nolint:errcheck
	if ctx.Err() != nil {
		return nil
	}
	return rerr
}

// Send transmits one Type B message on the current session.
func (c *Client) Send(ctx context.Context, peer string, raw []byte) error {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return fmt.Errorf("matip: session to %s is not up", c.Addr)
	}
	return s.Send(raw)
}

func (c *Client) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}
