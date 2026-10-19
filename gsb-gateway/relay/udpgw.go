package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// udpgw is the UDP-forwarding framing used by tun2proxy (a variant of
// badvpn-udpgw). Over one TCP connection the client multiplexes many UDP
// flows, each identified by a connection id. Big-endian throughout:
//
//	LEN(2) | FLAGS(1) | CONN_ID(2) | [ATYP(1) ADDR PORT(2)] | DATA
//
// LEN counts everything after itself. The address block is present only when
// FLAGS has flagData. ATYP follows SOCKS5: 1=IPv4, 3=domain, 4=IPv6.
const (
	flagKeepalive = 0x01
	flagData      = 0x02
	flagErr       = 0x20

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04
)

type udpgwPacket struct {
	flags  byte
	connID uint16
	addr   *net.UDPAddr // nil for keepalive/error frames
	data   []byte
}

func readPacket(r *bufio.Reader) (*udpgwPacket, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n < 3 {
		return nil, errors.New("udpgw: short frame")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	p := &udpgwPacket{flags: body[0], connID: binary.BigEndian.Uint16(body[1:3])}
	rest := body[3:]
	if p.flags&flagData == 0 {
		return p, nil
	}
	addr, consumed, err := parseAddr(rest)
	if err != nil {
		return nil, err
	}
	p.addr = addr
	p.data = append([]byte(nil), rest[consumed:]...)
	return p, nil
}

func parseAddr(b []byte) (*net.UDPAddr, int, error) {
	if len(b) < 1 {
		return nil, 0, errors.New("udpgw: no atyp")
	}
	switch b[0] {
	case atypIPv4:
		if len(b) < 1+4+2 {
			return nil, 0, errors.New("udpgw: short ipv4")
		}
		ip := net.IPv4(b[1], b[2], b[3], b[4])
		port := binary.BigEndian.Uint16(b[5:7])
		return &net.UDPAddr{IP: ip, Port: int(port)}, 7, nil
	case atypIPv6:
		if len(b) < 1+16+2 {
			return nil, 0, errors.New("udpgw: short ipv6")
		}
		ip := append([]byte(nil), b[1:17]...)
		port := binary.BigEndian.Uint16(b[17:19])
		return &net.UDPAddr{IP: ip, Port: int(port)}, 19, nil
	case atypDomain:
		if len(b) < 2 {
			return nil, 0, errors.New("udpgw: short domain")
		}
		dl := int(b[1])
		if len(b) < 2+dl+2 {
			return nil, 0, errors.New("udpgw: short domain body")
		}
		host := string(b[2 : 2+dl])
		port := binary.BigEndian.Uint16(b[2+dl : 2+dl+2])
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, 0, errors.New("udpgw: resolve " + host)
		}
		return &net.UDPAddr{IP: ips[0], Port: int(port)}, 2 + dl + 2, nil
	}
	return nil, 0, errors.New("udpgw: bad atyp")
}

// writeTo encodes a frame and writes it. Replies reuse the original address
// block so the client can match the connection id.
func (p *udpgwPacket) writeTo(w io.Writer) error {
	var ab []byte
	if p.flags&flagData != 0 && p.addr != nil {
		ab = encodeAddr(p.addr)
	}
	n := 3 + len(ab) + len(p.data)
	out := make([]byte, 2+n)
	binary.BigEndian.PutUint16(out[0:2], uint16(n))
	out[2] = p.flags
	binary.BigEndian.PutUint16(out[3:5], p.connID)
	copy(out[5:], ab)
	copy(out[5+len(ab):], p.data)
	_, err := w.Write(out)
	return err
}

func encodeAddr(a *net.UDPAddr) []byte {
	if v4 := a.IP.To4(); v4 != nil {
		b := make([]byte, 7)
		b[0] = atypIPv4
		copy(b[1:5], v4)
		binary.BigEndian.PutUint16(b[5:7], uint16(a.Port))
		return b
	}
	b := make([]byte, 19)
	b[0] = atypIPv6
	copy(b[1:17], a.IP.To16())
	binary.BigEndian.PutUint16(b[17:19], uint16(a.Port))
	return b
}

// flowTable maps connection ids to their outbound UDP sockets for one client.
type flowTable struct {
	mu      sync.Mutex
	flows   map[uint16]*net.UDPConn
	out     chan *udpgwPacket
	timeout time.Duration
	mtu     int
}

func newFlowTable(out chan *udpgwPacket, timeout time.Duration, mtu int) *flowTable {
	return &flowTable{flows: map[uint16]*net.UDPConn{}, out: out, timeout: timeout, mtu: mtu}
}

func (t *flowTable) handle(p *udpgwPacket) {
	if p.flags&flagKeepalive != 0 {
		t.out <- &udpgwPacket{flags: flagKeepalive, connID: p.connID}
		return
	}
	if p.flags&flagData == 0 || p.addr == nil {
		return
	}
	conn := t.conn(p.connID, p.addr)
	if conn == nil {
		t.out <- &udpgwPacket{flags: flagErr, connID: p.connID}
		return
	}
	conn.SetWriteDeadline(time.Now().Add(t.timeout))
	if _, err := conn.Write(p.data); err != nil {
		t.out <- &udpgwPacket{flags: flagErr, connID: p.connID}
	}
}

// conn returns the socket for a flow, creating and starting a reader the
// first time a connection id is seen. The reply keeps the client's address
// block so the stack routes datagrams back to the right local UDP socket.
func (t *flowTable) conn(id uint16, dst *net.UDPAddr) *net.UDPConn {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.flows[id]; ok {
		return c
	}
	c, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		return nil
	}
	t.flows[id] = c
	go t.readLoop(id, dst, c)
	return c
}

func (t *flowTable) readLoop(id uint16, dst *net.UDPAddr, c *net.UDPConn) {
	buf := make([]byte, t.mtu)
	for {
		c.SetReadDeadline(time.Now().Add(t.timeout))
		n, err := c.Read(buf)
		if n > 0 {
			t.out <- &udpgwPacket{flags: flagData, connID: id, addr: dst, data: append([]byte(nil), buf[:n]...)}
		}
		if err != nil {
			break
		}
	}
	t.mu.Lock()
	delete(t.flows, id)
	t.mu.Unlock()
	c.Close()
}

func (t *flowTable) closeAll() {
	t.mu.Lock()
	for _, c := range t.flows {
		c.Close()
	}
	t.flows = map[uint16]*net.UDPConn{}
	t.mu.Unlock()
}
