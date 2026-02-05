package pktflow

// FlowRecord summarizes sampled traffic for a destination.
type FlowRecord struct {
	DstIP     string `json:"dst_ip"`
	IPVersion uint8  `json:"ip_version"`
	IPProto   uint8  `json:"ip_proto"`
	SrcPort   uint16 `json:"src_port,omitempty"`
	DstPort   uint16 `json:"dst_port,omitempty"`
	ICMPType  uint8  `json:"icmp_type,omitempty"`
	TCPFlags  uint8  `json:"tcp_flags,omitempty"`
	Fragment  bool   `json:"fragment,omitempty"`
	Packets   uint64 `json:"packets"`
	Bytes     uint64 `json:"bytes"`
}
