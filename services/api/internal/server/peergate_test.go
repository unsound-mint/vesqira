package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// procWord prints a 32-bit word the way the kernel does (%08X of the host-order number whose memory bytes are b).
func procWord(b []byte) string { return fmt.Sprintf("%08X", binary.NativeEndian.Uint32(b)) }

func procV4(ip string, port uint16) string {
	a := netip.MustParseAddr(ip).As4()
	return fmt.Sprintf("%s:%04X", procWord(a[:]), port)
}

func procV6(ip string, port uint16) string {
	a := netip.MustParseAddr(ip).As16()
	return fmt.Sprintf("%s%s%s%s:%04X", procWord(a[0:4]), procWord(a[4:8]), procWord(a[8:12]), procWord(a[12:16]), port)
}

const procHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func procRow(sl int, local, remote string, uid int) string {
	return fmt.Sprintf("%4d: %s %s 01 00000000:00000000 00:00000000 00000000  %5d        0 12345 1 0000000000000000 100 0 0 10 0\n", sl, local, remote, uid)
}

func TestParseProcAddrKnownLittleEndianForms(t *testing.T) {
	t.Parallel()
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("literal kernel strings below are for little-endian hosts")
	}
	got, err := parseProcAddr("0100007F:1F90")
	if err != nil || got != netip.MustParseAddrPort("127.0.0.1:8080") {
		t.Fatalf("ipv4: %v %v", got, err)
	}
	got, err = parseProcAddr("00000000000000000000000001000000:1F90")
	if err != nil || got != netip.MustParseAddrPort("[::1]:8080") {
		t.Fatalf("ipv6 loopback: %v %v", got, err)
	}
	got, err = parseProcAddr("0000000000000000FFFF00000100007F:0050")
	if err != nil || got != netip.MustParseAddrPort("127.0.0.1:80") {
		t.Fatalf("mapped: %v %v", got, err)
	}
	for _, bad := range []string{"", "0100007F", "0100007F:80", "ZZ00007F:1F90", "0100007F00:1F90", "0100007F:ZZZZ"} {
		if _, err := parseProcAddr(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestScanProcNetFindsThePeerRowByBothAddresses(t *testing.T) {
	t.Parallel()
	ours := netip.MustParseAddrPort("127.0.0.1:8787")
	peer := netip.MustParseAddrPort("127.0.0.1:51000")
	other := netip.MustParseAddrPort("127.0.0.1:51001")
	tab := procHeader +
		// the server's side of the same connection: local is ours, and carries the server's uid
		procRow(0, procV4("127.0.0.1", 8787), procV4("127.0.0.1", 51000), 1000) +
		// another client's connection to us with another uid
		procRow(1, procV4("127.0.0.1", 51001), procV4("127.0.0.1", 8787), 2000) +
		// same local port but towards another server: must not match
		procRow(2, procV4("127.0.0.1", 51000), procV4("127.0.0.1", 9999), 3000) +
		procRow(3, procV4("127.0.0.1", 51000), procV4("127.0.0.1", 8787), 4242)
	uid, ok, err := scanProcNet(strings.NewReader(tab), peer, ours)
	if err != nil || !ok || uid != 4242 {
		t.Fatalf("peer: %d %v %v", uid, ok, err)
	}
	uid, ok, err = scanProcNet(strings.NewReader(tab), other, ours)
	if err != nil || !ok || uid != 2000 {
		t.Fatalf("other: %d %v %v", uid, ok, err)
	}
	if _, ok, _ := scanProcNet(strings.NewReader(tab), netip.MustParseAddrPort("127.0.0.1:1"), ours); ok {
		t.Fatal("unknown peer found")
	}
}

func TestScanProcNetIPv6AndMapped(t *testing.T) {
	t.Parallel()
	ours6 := netip.MustParseAddrPort("[::1]:8787")
	peer6 := netip.MustParseAddrPort("[::1]:52000")
	tab := procHeader + procRow(0, procV6("::1", 52000), procV6("::1", 8787), 777)
	if uid, ok, err := scanProcNet(strings.NewReader(tab), peer6, ours6); err != nil || !ok || uid != 777 {
		t.Fatalf("v6: %d %v %v", uid, ok, err)
	}
	// a dual-stack listener sees an IPv4 peer as ::ffff:127.0.0.1 in tcp6; the connection's addresses are unmapped
	mapped := procHeader + procRow(0, procV6("::ffff:127.0.0.1", 52001), procV6("::ffff:127.0.0.1", 8787), 888)
	if uid, ok, err := scanProcNet(strings.NewReader(mapped), netip.MustParseAddrPort("127.0.0.1:52001"), netip.MustParseAddrPort("127.0.0.1:8787")); err != nil || !ok || uid != 888 {
		t.Fatalf("mapped: %d %v %v", uid, ok, err)
	}
}

func TestScanProcNetRefusesMalformedAndOversizedRows(t *testing.T) {
	t.Parallel()
	p, o := netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2")
	if _, _, err := scanProcNet(strings.NewReader(procHeader+"   0: short row\n"), p, o); err == nil {
		t.Error("short row accepted")
	}
	if _, _, err := scanProcNet(strings.NewReader(procHeader+strings.Repeat("x", 2*procNetMaxLine)+"\n"), p, o); err == nil {
		t.Error("oversized line accepted")
	}
	row := procRow(0, procV4("127.0.0.1", 1), procV4("127.0.0.1", 2), 5)
	row = strings.Replace(row, "     5 ", "   abc ", 1)
	if _, _, err := scanProcNet(strings.NewReader(procHeader+row), p, o); err == nil {
		t.Error("bad uid accepted")
	}
}

func TestProcPeerUIDFailsClosedWhenTablesAreMissing(t *testing.T) {
	t.Parallel()
	p, o := netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2")
	if _, err := procPeerUID(t.TempDir(), p, o); err == nil {
		t.Fatal("no tables must be an error")
	}
	dir := t.TempDir() // tcp readable, tcp6 absent: fine, but a missing row still refuses
	if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(procHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := procPeerUID(dir, p, o); err == nil {
		t.Fatal("unknown connection must be an error")
	}
	if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(procHeader+procRow(0, procV4("127.0.0.1", 1), procV4("127.0.0.1", 2), 9)), 0o600); err != nil {
		t.Fatal(err)
	}
	if uid, err := procPeerUID(dir, p, o); err != nil || uid != 9 {
		t.Fatalf("%d %v", uid, err)
	}
}

func gatedServer(t *testing.T, g func(net.Listener) *PeerGate) (*PeerGate, string, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := g(l)
	served := new(atomic.Int32)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served.Add(1); _, _ = io.WriteString(w, "ok") })}
	go func() { _ = srv.Serve(gate) }()
	t.Cleanup(func() { _ = srv.Close() })
	return gate, l.Addr().String(), served
}

func TestPeerGateClosesOtherUsersBeforeAnyHTTP(t *testing.T) {
	t.Parallel()
	gate, addr, served := gatedServer(t, func(l net.Listener) *PeerGate {
		return &PeerGate{Listener: l, Allowed: 1000, Lookup: func(_, _ netip.AddrPort) (uint32, error) { return 1001, nil }}
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if b, err := io.ReadAll(c); len(b) != 0 {
		t.Fatalf("got a response %q (err %v)", b, err)
	}
	if served.Load() != 0 || gate.Refused() != 1 {
		t.Fatalf("served %d refused %d", served.Load(), gate.Refused())
	}
}

func TestPeerGateFailsClosedOnLookupError(t *testing.T) {
	t.Parallel()
	gate, addr, served := gatedServer(t, func(l net.Listener) *PeerGate {
		return &PeerGate{Listener: l, Allowed: 1000, Lookup: func(_, _ netip.AddrPort) (uint32, error) { return 0, errors.New("no /proc") }}
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if b, _ := io.ReadAll(c); len(b) != 0 {
		t.Fatalf("got %q", b)
	}
	if served.Load() != 0 || gate.Refused() != 1 {
		t.Fatalf("served %d refused %d", served.Load(), gate.Refused())
	}
}

func TestPeerGateServesTheSameUser(t *testing.T) {
	t.Parallel()
	_, addr, served := gatedServer(t, func(l net.Listener) *PeerGate {
		return &PeerGate{Listener: l, Allowed: 1000, Lookup: func(_, _ netip.AddrPort) (uint32, error) { return 1000, nil }}
	})
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if served.Load() != 1 {
		t.Fatalf("served %d", served.Load())
	}
}

// A real loopback connection from this process, checked against the real socket table.
func TestPeerGateAcceptsARealLoopbackConnectionOfTheSameUser(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("socket-table peer check is Linux only")
	}
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net/tcp in this environment")
	}
	for _, network := range []string{"127.0.0.1:0", "[::1]:0"} {
		l, err := net.Listen("tcp", network)
		if err != nil {
			continue // no IPv6 here
		}
		gate := NewPeerGate(l)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })}
		go func() { _ = srv.Serve(gate) }()
		resp, err := http.Get("http://" + l.Addr().String() + "/")
		if err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		resp.Body.Close()
		if gate.Refused() != 0 {
			t.Fatalf("%s: refused %d", network, gate.Refused())
		}
		_ = srv.Close()
	}
}
