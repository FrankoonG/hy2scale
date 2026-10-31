package app

// GeoIPConfig is the `geoip` block of the config: "manual" (the operator
// uploads the databases; the default) or "auto" (downloaded from the
// official publishers, internal/geoip).
type GeoIPConfig struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// GeoIPMode is the configured mode ("manual" when unset).
func (c Config) GeoIPMode() string {
	if c.GeoIP != nil && c.GeoIP.Mode == "auto" {
		return "auto"
	}
	return "manual"
}
