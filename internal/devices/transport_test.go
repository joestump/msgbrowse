// In-memory duplex conn for exercising TLS handshakes without a socket.
//
// net.Pipe is fully synchronous: every Write blocks until the peer Reads.
// A TLS 1.3 client that rejects the server certificate mid-flight wants to
// WRITE an alert while the server may still be blocked WRITING the tail of
// its own flight — with net.Pipe that is a deadlock whenever read buffering
// doesn't happen to consume the whole flight. memConnPair buffers each
// direction, so writes never block and the abort paths under test terminate
// deterministically. Still zero sockets: it is all bytes.Buffer and sync.Cond.
package devices

import (
	"bytes"
	"io"
	"net"
	"sync"
	"time"
)

type pipeBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newPipeBuffer() *pipeBuffer {
	b := &pipeBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *pipeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := b.buf.Write(p)
	b.cond.Broadcast()
	return n, err
}

func (b *pipeBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.buf.Len() == 0 {
		return 0, io.EOF
	}
	return b.buf.Read(p)
}

func (b *pipeBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.cond.Broadcast()
	return nil
}

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "in-memory" }

// memConn is one end of an in-memory duplex byte stream.
type memConn struct {
	r *pipeBuffer // owned read side
	w *pipeBuffer // peer's read side
}

func (c *memConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *memConn) Write(p []byte) (int, error) { return c.w.Write(p) }

// Close tears down both directions: blocked reads return, writes fail —
// exactly what tls.HandshakeContext needs to honor context cancellation.
func (c *memConn) Close() error {
	_ = c.r.Close()
	_ = c.w.Close()
	return nil
}

func (c *memConn) LocalAddr() net.Addr  { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr { return memAddr{} }

// Deadlines are no-ops: tests bound runtime with watchdogs/client timeouts
// that Close the conn, which unblocks everything.
func (c *memConn) SetDeadline(time.Time) error      { return nil }
func (c *memConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

// memConnPair returns the two ends of an in-memory buffered duplex conn.
func memConnPair() (net.Conn, net.Conn) {
	a, b := newPipeBuffer(), newPipeBuffer()
	return &memConn{r: a, w: b}, &memConn{r: b, w: a}
}
