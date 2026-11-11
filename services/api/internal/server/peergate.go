package server

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The owner listener has no accounts: it is loopback, and localOnly stops web pages. Neither stops another user
// account on the same machine from connecting (curl, a WebSocket client). On Linux the kernel knows which uid owns
// each TCP socket, so the owner listener accepts a connection only when the peer socket belongs to the uid this
// server runs as. The check is on the socket table (/proc/net/tcp and tcp6), not on anything the peer sends.
// Same-account processes stay trusted (the single-user model, ADR 0029). Platforms without /proc are not covered.

// PeerUIDFunc returns the uid that owns the socket peer (the connecting side, as seen from this server: peer is the
// connection's remote address, ours its local address).
type PeerUIDFunc func(peer, ours netip.AddrPort) (uint32, error)

// PeerGate wraps the owner listener and closes, before any HTTP is read, a connection whose peer uid is not Allowed.
// If the lookup fails the connection is refused too (fail closed).
type PeerGate struct {
	net.Listener
	Allowed uint32
	Lookup  PeerUIDFunc

	refused  atomic.Uint64
	logMu    sync.Mutex
	lastLog  time.Time
	logEvery time.Duration
}

// NewPeerGate gates l to the uid of this process using the Linux socket table.
func NewPeerGate(l net.Listener) *PeerGate {
	return &PeerGate{Listener: l, Allowed: uint32(os.Getuid()), Lookup: ProcPeerUID}
}

// Refused is the number of connections closed by the gate.
func (g *PeerGate) Refused() uint64 { return g.refused.Load() }

func (g *PeerGate) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if reason := g.check(c); reason != "" {
			n := g.refused.Add(1)
			_ = c.Close()
			g.logRefusal(n, reason)
			continue
		}
		return c, nil
	}
}

// check returns why the connection is refused, or "" when it may be served.
func (g *PeerGate) check(c net.Conn) string {
	peer, ok1 := addrPort(c.RemoteAddr())
	ours, ok2 := addrPort(c.LocalAddr())
	if !ok1 || !ok2 {
		return "peer address is not TCP"
	}
	uid, err := g.Lookup(peer, ours)
	switch {
	case err != nil:
		return "peer uid unknown: " + err.Error()
	case uid != g.Allowed:
		return "peer belongs to another user account"
	}
	return ""
}

// logRefusal logs one line, at most one per 10 s so a flood cannot fill the log: no addresses, no request data.
func (g *PeerGate) logRefusal(total uint64, reason string) {
	g.logMu.Lock()
	defer g.logMu.Unlock()
	every := g.logEvery
	if every == 0 {
		every = 10 * time.Second
	}
	if !g.lastLog.IsZero() && time.Since(g.lastLog) < every {
		return
	}
	g.lastLog = time.Now()
	log.Printf("owner listener: refused a local connection (%s); %d refused so far", reason, total)
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	ap := t.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
}

const (
	procNetLimit     = 16 << 20 // bytes read per table
	procNetMaxLine   = 512
	procNetUIDColumn = 7
)

// ProcPeerUID looks the peer up in /proc/net/tcp and /proc/net/tcp6.
func ProcPeerUID(peer, ours netip.AddrPort) (uint32, error) {
	return procPeerUID("/proc/net", peer, ours)
}

func procPeerUID(dir string, peer, ours netip.AddrPort) (uint32, error) {
	var firstErr error
	readable := 0
	for _, name := range []string{"tcp", "tcp6"} {
		f, err := os.Open(dir + "/" + name)
		if err != nil {
			// A machine without IPv6 has no tcp6; that is not an error unless nothing at all can be read.
			if firstErr == nil && !errors.Is(err, os.ErrNotExist) {
				firstErr = err
			}
			continue
		}
		readable++
		uid, found, err := scanProcNet(io.LimitReader(f, procNetLimit), peer, ours)
		_ = f.Close()
		if err != nil {
			return 0, err
		}
		if found {
			return uid, nil
		}
	}
	if readable == 0 {
		if firstErr == nil {
			firstErr = errors.New("/proc/net/tcp not available")
		}
		return 0, firstErr
	}
	return 0, errors.New("connection not found in the socket table")
}

// scanProcNet finds the row whose local address is the peer's and whose remote address is ours, and returns its uid.
func scanProcNet(r io.Reader, peer, ours netip.AddrPort) (uid uint32, found bool, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), procNetMaxLine)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) <= procNetUIDColumn {
			return 0, false, errors.New("unexpected socket table row")
		}
		local, err := parseProcAddr(f[1])
		if err != nil {
			return 0, false, err
		}
		if local != peer {
			continue
		}
		rem, err := parseProcAddr(f[2])
		if err != nil {
			return 0, false, err
		}
		if rem != ours {
			continue
		}
		u, err := strconv.ParseUint(f[procNetUIDColumn], 10, 32)
		if err != nil {
			return 0, false, fmt.Errorf("unexpected uid column: %w", err)
		}
		return uint32(u), true, nil
	}
	if err := sc.Err(); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

// parseProcAddr reads "0100007F:1F90" (IPv4) or a 32-digit IPv6 form. The kernel prints each 32-bit word of the
// address as one %08X number in host byte order, so every word is converted with the machine's own byte order.
// IPv4-mapped IPv6 addresses are returned as plain IPv4.
func parseProcAddr(s string) (netip.AddrPort, error) {
	host, port, ok := strings.Cut(s, ":")
	if !ok || len(port) != 4 {
		return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("bad port in %q", s)
	}
	var b [16]byte
	switch len(host) {
	case 8:
		w, err := strconv.ParseUint(host, 16, 32)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
		}
		binary.NativeEndian.PutUint32(b[:4], uint32(w))
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), uint16(p)), nil
	case 32:
		for i := 0; i < 4; i++ {
			w, err := strconv.ParseUint(host[i*8:i*8+8], 16, 32)
			if err != nil {
				return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
			}
			binary.NativeEndian.PutUint32(b[i*4:], uint32(w))
		}
		return netip.AddrPortFrom(netip.AddrFrom16(b).Unmap(), uint16(p)), nil
	}
	return netip.AddrPort{}, fmt.Errorf("bad address %q", s)
}
