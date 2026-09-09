package v1

const (
	// LegacyCapabilityVersion identifies connectors that predate explicit protocol versioning.
	LegacyCapabilityVersion uint32 = 0

	// InitialCapabilityVersion is the initial versioned connector protocol baseline.
	InitialCapabilityVersion uint32 = 1

	// WireGuardRehandshakeCapabilityVersion adds the in-place re-handshake control event.
	WireGuardRehandshakeCapabilityVersion uint32 = 2

	// CurrentCapabilityVersion is the connector protocol capability advertised by this build.
	CurrentCapabilityVersion uint32 = WireGuardRehandshakeCapabilityVersion
)
