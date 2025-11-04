package exelet

type OptConfig struct {
	IsMaintenance bool
}

type ServerOpt func(cfg *OptConfig)

// WithMaintenance is an opt that sets the exelet state in maintenance
func WithMaintenance() ServerOpt {
	return func(cfg *OptConfig) {
		cfg.IsMaintenance = true
	}
}
