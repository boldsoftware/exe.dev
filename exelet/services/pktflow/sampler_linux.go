//go:build linux

package pktflow

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	ethHeaderLen = 14
	ethPIPv4     = 0x0800
	ethPIPv6     = 0x86DD
	// Ancillary data offsets from linux/filter.h for classic BPF random.
	skfAdOff    = 0xfffff000
	skfAdRandom = 56
)

type flowKey struct {
	dstIP     string
	ipVersion uint8
	ipProto   uint8
	srcPort   uint16
	dstPort   uint16
	icmpType  uint8
	tcpFlags  uint8
	fragment  bool
}

type flowAgg struct {
	packets uint64
	bytes   uint64
}

type tapSampler struct {
	tap        string
	sampleRate uint32
	maxFlows   int
	log        *slog.Logger

	fd int
	wg sync.WaitGroup

	mu     sync.Mutex
	counts map[flowKey]*flowAgg
}

func newTapSampler(tap string, sampleRate uint32, maxFlows int, log *slog.Logger) (*tapSampler, error) {
	if sampleRate == 0 {
		sampleRate = 1024
	}
	if sampleRate&(sampleRate-1) != 0 {
		return nil, fmt.Errorf("sample rate must be power of two: %d", sampleRate)
	}
	if maxFlows <= 0 {
		maxFlows = 200
	}

	iface, err := net.InterfaceByName(tap)
	if err != nil {
		return nil, fmt.Errorf("lookup interface %s: %w", tap, err)
	}

	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("open packet socket: %w", err)
	}

	sa := &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_ALL),
		Ifindex:  iface.Index,
	}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind packet socket: %w", err)
	}

	if sampleRate > 1 {
		if err := attachSampleFilter(fd, sampleRate); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("attach sample filter: %w", err)
		}
	}

	s := &tapSampler{
		tap:        tap,
		sampleRate: sampleRate,
		maxFlows:   maxFlows,
		log:        log,
		fd:         fd,
		counts:     make(map[flowKey]*flowAgg),
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run()
	}()

	return s, nil
}

func (s *tapSampler) Close() {
	if s.fd == -1 {
		return
	}
	_ = unix.Close(s.fd)
	s.fd = -1
	s.wg.Wait()
}

func (s *tapSampler) Snapshot() []FlowRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.counts) == 0 {
		return nil
	}

	records := make([]FlowRecord, 0, len(s.counts))
	scale := uint64(s.sampleRate)
	for key, agg := range s.counts {
		records = append(records, FlowRecord{
			DstIP:     key.dstIP,
			IPVersion: key.ipVersion,
			IPProto:   key.ipProto,
			SrcPort:   key.srcPort,
			DstPort:   key.dstPort,
			ICMPType:  key.icmpType,
			TCPFlags:  key.tcpFlags,
			Fragment:  key.fragment,
			Packets:   agg.packets * scale,
			Bytes:     agg.bytes * scale,
		})
	}
	s.counts = make(map[flowKey]*flowAgg)
	return records
}

func (s *tapSampler) run() {
	buf := make([]byte, 2048)
	for {
		n, from, err := unix.Recvfrom(s.fd, buf, 0)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if !fromVM(from) {
			continue
		}
		if n <= ethHeaderLen {
			continue
		}
		s.handlePacket(buf[:n])
	}
}

func (s *tapSampler) handlePacket(pkt []byte) {
	ethType := binary.BigEndian.Uint16(pkt[12:14])
	switch ethType {
	case ethPIPv4:
		s.handleIPv4(pkt)
	case ethPIPv6:
		s.handleIPv6(pkt)
	}
}

func (s *tapSampler) handleIPv4(pkt []byte) {
	if len(pkt) < ethHeaderLen+20 {
		return
	}
	ip := pkt[ethHeaderLen:]
	ihl := int(ip[0]&0x0f) * 4
	if ihl < 20 || len(ip) < ihl {
		return
	}
	totalLen := int(binary.BigEndian.Uint16(ip[2:4]))
	pktLen := totalLen
	if pktLen <= 0 || pktLen > len(ip) {
		pktLen = len(ip)
	}

	ipProto := ip[9]
	dstIP := net.IP(ip[16:20]).String()

	fragField := binary.BigEndian.Uint16(ip[6:8])
	flags := fragField >> 13
	offset := fragField & 0x1fff
	fragment := offset != 0 || (flags&0x1) != 0

	var srcPort, dstPort uint16
	var icmpType uint8
	var tcpFlags uint8
	l4 := ip[ihl:]
	switch ipProto {
	case 1:
		if len(l4) >= 1 {
			icmpType = l4[0]
		}
	case 6:
		if len(l4) >= 14 {
			srcPort = binary.BigEndian.Uint16(l4[0:2])
			dstPort = binary.BigEndian.Uint16(l4[2:4])
			tcpFlags = l4[13]
		}
	case 17:
		if len(l4) >= 4 {
			srcPort = binary.BigEndian.Uint16(l4[0:2])
			dstPort = binary.BigEndian.Uint16(l4[2:4])
		}
	}

	key := flowKey{
		dstIP:     dstIP,
		ipVersion: 4,
		ipProto:   ipProto,
		srcPort:   srcPort,
		dstPort:   dstPort,
		icmpType:  icmpType,
		tcpFlags:  tcpFlags,
		fragment:  fragment,
	}
	s.add(key, uint64(pktLen))
}

func (s *tapSampler) handleIPv6(pkt []byte) {
	if len(pkt) < ethHeaderLen+40 {
		return
	}
	ip := pkt[ethHeaderLen:]
	ipProto := ip[6]
	payloadLen := int(binary.BigEndian.Uint16(ip[4:6]))
	pktLen := payloadLen + 40
	if pktLen <= 0 || pktLen > len(ip) {
		pktLen = len(ip)
	}

	dstIP := net.IP(ip[24:40]).String()
	fragment := false
	l4offset := 40
	if ipProto == 44 && len(ip) >= 48 {
		fragment = true
		ipProto = ip[40]
		fragField := binary.BigEndian.Uint16(ip[42:44])
		_ = fragField
		l4offset = 48
	}

	var srcPort, dstPort uint16
	var icmpType uint8
	var tcpFlags uint8
	if len(ip) >= l4offset {
		l4 := ip[l4offset:]
		switch ipProto {
		case 58:
			if len(l4) >= 1 {
				icmpType = l4[0]
			}
		case 6:
			if len(l4) >= 14 {
				srcPort = binary.BigEndian.Uint16(l4[0:2])
				dstPort = binary.BigEndian.Uint16(l4[2:4])
				tcpFlags = l4[13]
			}
		case 17:
			if len(l4) >= 4 {
				srcPort = binary.BigEndian.Uint16(l4[0:2])
				dstPort = binary.BigEndian.Uint16(l4[2:4])
			}
		}
	}

	key := flowKey{
		dstIP:     dstIP,
		ipVersion: 6,
		ipProto:   ipProto,
		srcPort:   srcPort,
		dstPort:   dstPort,
		icmpType:  icmpType,
		tcpFlags:  tcpFlags,
		fragment:  fragment,
	}
	s.add(key, uint64(pktLen))
}

func (s *tapSampler) add(key flowKey, pktLen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if agg, ok := s.counts[key]; ok {
		agg.packets++
		agg.bytes += pktLen
		return
	}
	if len(s.counts) >= s.maxFlows {
		return
	}
	s.counts[key] = &flowAgg{packets: 1, bytes: pktLen}
}

func attachSampleFilter(fd int, sampleRate uint32) error {
	filters := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: skfAdOff + skfAdRandom},
		{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: sampleRate - 1},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: 0, Jt: 1, Jf: 0},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0xFFFFFFFF},
	}
	prog := unix.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	return unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog)
}

func htons(v uint16) uint16 {
	return (v<<8)&0xff00 | v>>8
}

func fromVM(sa unix.Sockaddr) bool {
	if sa == nil {
		return true
	}
	sll, ok := sa.(*unix.SockaddrLinklayer)
	if !ok {
		return true
	}
	return sll.Pkttype != unix.PACKET_OUTGOING
}
