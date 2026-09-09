package vpctunnel

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/pires/go-proxyproto"
)

var testFlowContext = FlowContext{IntegrationName: "Office-API", VMName: "Dev-Box"}

func TestFlowProxyV2HeaderRoundTrip(t *testing.T) {
	raw, err := FormatFlowProxyV2Header("extconn-123", "postgres", 5432, testFlowContext)
	if err != nil {
		t.Fatal(err)
	}
	rawAgain, err := FormatFlowProxyV2Header("extconn-123", "postgres", 5432, testFlowContext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, rawAgain) {
		t.Fatal("flow header formatting is not deterministic")
	}
	header, err := proxyproto.Read(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}

	externalConnectionID, endpoint, targetPort, flowContext, err := FlowMetadataFromProxyV2Header(header)
	if err != nil {
		t.Fatal(err)
	}
	if externalConnectionID != "extconn-123" {
		t.Errorf("External Connection ID = %q, want extconn-123", externalConnectionID)
	}
	if endpoint != "postgres" {
		t.Errorf("endpoint = %q, want postgres", endpoint)
	}
	if targetPort != 5432 {
		t.Errorf("target port = %d, want 5432", targetPort)
	}
	if flowContext != testFlowContext {
		t.Errorf("flow context = %+v, want %+v", flowContext, testFlowContext)
	}
}

func TestFormatFlowProxyV2HeaderRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name                 string
		externalConnectionID string
		endpoint             string
		targetPort           int
		flowContext          FlowContext
	}{
		{name: "empty External Connection ID", endpoint: "postgres", targetPort: 5432, flowContext: testFlowContext},
		{name: "empty endpoint", externalConnectionID: "extconn-123", targetPort: 5432, flowContext: testFlowContext},
		{name: "zero port", externalConnectionID: "extconn-123", endpoint: "postgres", flowContext: testFlowContext},
		{name: "port too large", externalConnectionID: "extconn-123", endpoint: "postgres", targetPort: 65536, flowContext: testFlowContext},
		{name: "empty integration name", externalConnectionID: "extconn-123", endpoint: "postgres", targetPort: 5432, flowContext: FlowContext{VMName: "dev-box"}},
		{name: "blank integration name", externalConnectionID: "extconn-123", endpoint: "postgres", targetPort: 5432, flowContext: FlowContext{IntegrationName: " \t", VMName: "dev-box"}},
		{name: "empty VM name", externalConnectionID: "extconn-123", endpoint: "postgres", targetPort: 5432, flowContext: FlowContext{IntegrationName: "office-api"}},
		{name: "blank VM name", externalConnectionID: "extconn-123", endpoint: "postgres", targetPort: 5432, flowContext: FlowContext{IntegrationName: "office-api", VMName: " \t"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := FormatFlowProxyV2Header(test.externalConnectionID, test.endpoint, test.targetPort, test.flowContext); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFlowMetadataFromProxyV2HeaderStrictTLVs(t *testing.T) {
	base := []proxyproto.TLV{
		{Type: ExternalConnectionIDTLVType, Value: []byte("extconn-123")},
		{Type: EndpointTLVType, Value: []byte("postgres")},
		{Type: IntegrationNameTLVType, Value: []byte("office-api")},
		{Type: VMNameTLVType, Value: []byte("dev-box")},
	}
	tests := []struct {
		name string
		tlvs []proxyproto.TLV
	}{
		{name: "missing External Connection ID", tlvs: withoutTLV(base, ExternalConnectionIDTLVType)},
		{name: "missing endpoint", tlvs: withoutTLV(base, EndpointTLVType)},
		{name: "missing integration name", tlvs: withoutTLV(base, IntegrationNameTLVType)},
		{name: "missing VM name", tlvs: withoutTLV(base, VMNameTLVType)},
		{name: "empty External Connection ID", tlvs: replaceTLV(base, ExternalConnectionIDTLVType, nil)},
		{name: "empty endpoint", tlvs: replaceTLV(base, EndpointTLVType, nil)},
		{name: "empty integration name", tlvs: replaceTLV(base, IntegrationNameTLVType, nil)},
		{name: "blank integration name", tlvs: replaceTLV(base, IntegrationNameTLVType, []byte(" \t"))},
		{name: "empty VM name", tlvs: replaceTLV(base, VMNameTLVType, nil)},
		{name: "blank VM name", tlvs: replaceTLV(base, VMNameTLVType, []byte(" \t"))},
		{name: "duplicate External Connection ID", tlvs: appendTLV(base, proxyproto.TLV{Type: ExternalConnectionIDTLVType, Value: []byte("other")})},
		{name: "duplicate endpoint", tlvs: appendTLV(base, proxyproto.TLV{Type: EndpointTLVType, Value: []byte("other")})},
		{name: "duplicate integration name", tlvs: appendTLV(base, proxyproto.TLV{Type: IntegrationNameTLVType, Value: []byte("other")})},
		{name: "duplicate VM name", tlvs: appendTLV(base, proxyproto.TLV{Type: VMNameTLVType, Value: []byte("other")})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, _, err := FlowMetadataFromProxyV2Header(newFlowHeader(t, 5432, test.tlvs)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFlowMetadataFromProxyV2HeaderIgnoresUnknownTLV(t *testing.T) {
	tlvs := []proxyproto.TLV{
		{Type: ExternalConnectionIDTLVType, Value: []byte("extconn-123")},
		{Type: proxyproto.PP2_TYPE_MIN_CUSTOM + 10, Value: []byte("future")},
		{Type: EndpointTLVType, Value: []byte("postgres")},
		{Type: IntegrationNameTLVType, Value: []byte("Office-API")},
		{Type: VMNameTLVType, Value: []byte("Dev-Box")},
	}
	_, _, _, flowContext, err := FlowMetadataFromProxyV2Header(newFlowHeader(t, 5432, tlvs))
	if err != nil {
		t.Fatal(err)
	}
	want := FlowContext{IntegrationName: "Office-API", VMName: "Dev-Box"}
	if flowContext != want {
		t.Fatalf("flow context = %+v, want %+v", flowContext, want)
	}
}

func TestFlowMetadataFromProxyV2HeaderRejectsInvalidHeader(t *testing.T) {
	if _, _, _, _, err := FlowMetadataFromProxyV2Header(nil); err == nil {
		t.Fatal("nil header accepted")
	}
	header := newFlowHeader(t, 0, []proxyproto.TLV{
		{Type: ExternalConnectionIDTLVType, Value: []byte("extconn-123")},
		{Type: EndpointTLVType, Value: []byte("postgres")},
		{Type: IntegrationNameTLVType, Value: []byte("office-api")},
		{Type: VMNameTLVType, Value: []byte("dev-box")},
	})
	if _, _, _, _, err := FlowMetadataFromProxyV2Header(header); err == nil || !strings.Contains(err.Error(), "destination TCP port") {
		t.Fatalf("error = %v, want destination port error", err)
	}
}

func newFlowHeader(t *testing.T, targetPort int, tlvs []proxyproto.TLV) *proxyproto.Header {
	t.Helper()
	header := proxyproto.HeaderProxyFromAddrs(
		2,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: targetPort},
	)
	if err := header.SetTLVs(tlvs); err != nil {
		t.Fatal(err)
	}
	return header
}

func withoutTLV(tlvs []proxyproto.TLV, typ proxyproto.PP2Type) []proxyproto.TLV {
	out := make([]proxyproto.TLV, 0, len(tlvs)-1)
	for _, tlv := range tlvs {
		if tlv.Type != typ {
			out = append(out, tlv)
		}
	}
	return out
}

func replaceTLV(tlvs []proxyproto.TLV, typ proxyproto.PP2Type, value []byte) []proxyproto.TLV {
	out := append([]proxyproto.TLV(nil), tlvs...)
	for index := range out {
		if out[index].Type == typ {
			out[index].Value = value
			return out
		}
	}
	return out
}

func appendTLV(tlvs []proxyproto.TLV, tlv proxyproto.TLV) []proxyproto.TLV {
	return append(append([]proxyproto.TLV(nil), tlvs...), tlv)
}
