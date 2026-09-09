package vpctunnel

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/pires/go-proxyproto"
)

const (
	// ExternalConnectionIDTLVType carries the External Connection ID.
	ExternalConnectionIDTLVType proxyproto.PP2Type = proxyproto.PP2_TYPE_MIN_CUSTOM + 1

	// EndpointTLVType carries the endpoint key configured by execonnect.
	EndpointTLVType proxyproto.PP2Type = proxyproto.PP2_TYPE_MIN_CUSTOM + 2

	// IntegrationNameTLVType carries the integration lookup name authorized by exed.
	IntegrationNameTLVType proxyproto.PP2Type = proxyproto.PP2_TYPE_MIN_CUSTOM + 3

	// VMNameTLVType carries the originating VM lookup name authorized by exed.
	VMNameTLVType proxyproto.PP2Type = proxyproto.PP2_TYPE_MIN_CUSTOM + 4
)

// FlowContext identifies the integration and originating VM for one flow.
type FlowContext struct {
	IntegrationName string
	VMName          string
}

// FormatFlowProxyV2Header formats routing metadata for an External Connection flow.
func FormatFlowProxyV2Header(externalConnectionID, endpoint string, targetPort int, flowContext FlowContext) ([]byte, error) {
	externalConnectionID = strings.TrimSpace(externalConnectionID)
	if externalConnectionID == "" {
		return nil, errors.New("empty External Connection ID")
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("empty endpoint")
	}
	if targetPort <= 0 || targetPort > 65535 {
		return nil, fmt.Errorf("invalid target port %d", targetPort)
	}
	if strings.TrimSpace(flowContext.IntegrationName) == "" {
		return nil, errors.New("empty integration name")
	}
	if strings.TrimSpace(flowContext.VMName) == "" {
		return nil, errors.New("empty VM name")
	}

	header := proxyproto.HeaderProxyFromAddrs(
		2,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: targetPort},
	)
	if err := header.SetTLVs([]proxyproto.TLV{
		{Type: ExternalConnectionIDTLVType, Value: []byte(externalConnectionID)},
		{Type: EndpointTLVType, Value: []byte(endpoint)},
		{Type: IntegrationNameTLVType, Value: []byte(flowContext.IntegrationName)},
		{Type: VMNameTLVType, Value: []byte(flowContext.VMName)},
	}); err != nil {
		return nil, err
	}
	return header.Format()
}

// FlowMetadataFromProxyV2Header extracts External Connection routing metadata.
func FlowMetadataFromProxyV2Header(header *proxyproto.Header) (externalConnectionID, endpoint string, targetPort int, flowContext FlowContext, err error) {
	if header == nil {
		return "", "", 0, FlowContext{}, errors.New("nil PROXY v2 header")
	}
	if header.Version != 2 || !header.Command.IsProxy() {
		return "", "", 0, FlowContext{}, errors.New("invalid PROXY v2 header")
	}
	_, destination, ok := header.TCPAddrs()
	if !ok || destination == nil || destination.Port <= 0 || destination.Port > 65535 {
		return "", "", 0, FlowContext{}, errors.New("missing or invalid destination TCP port")
	}

	tlvs, err := header.TLVs()
	if err != nil {
		return "", "", 0, FlowContext{}, err
	}
	var externalConnectionIDSeen, endpointSeen, integrationNameSeen, vmNameSeen bool
	for _, tlv := range tlvs {
		switch tlv.Type {
		case ExternalConnectionIDTLVType:
			if externalConnectionIDSeen {
				return "", "", 0, FlowContext{}, errors.New("duplicate External Connection ID TLV")
			}
			externalConnectionIDSeen = true
			externalConnectionID = strings.TrimSpace(string(tlv.Value))
		case EndpointTLVType:
			if endpointSeen {
				return "", "", 0, FlowContext{}, errors.New("duplicate endpoint TLV")
			}
			endpointSeen = true
			endpoint = strings.TrimSpace(string(tlv.Value))
		case IntegrationNameTLVType:
			if integrationNameSeen {
				return "", "", 0, FlowContext{}, errors.New("duplicate integration name TLV")
			}
			integrationNameSeen = true
			flowContext.IntegrationName = string(tlv.Value)
		case VMNameTLVType:
			if vmNameSeen {
				return "", "", 0, FlowContext{}, errors.New("duplicate VM name TLV")
			}
			vmNameSeen = true
			flowContext.VMName = string(tlv.Value)
		}
	}
	if !externalConnectionIDSeen || externalConnectionID == "" {
		return "", "", 0, FlowContext{}, errors.New("missing or empty External Connection ID TLV")
	}
	if !endpointSeen || endpoint == "" {
		return "", "", 0, FlowContext{}, errors.New("missing or empty endpoint TLV")
	}
	if !integrationNameSeen || strings.TrimSpace(flowContext.IntegrationName) == "" {
		return "", "", 0, FlowContext{}, errors.New("missing or empty integration name TLV")
	}
	if !vmNameSeen || strings.TrimSpace(flowContext.VMName) == "" {
		return "", "", 0, FlowContext{}, errors.New("missing or empty VM name TLV")
	}
	return externalConnectionID, endpoint, destination.Port, flowContext, nil
}
