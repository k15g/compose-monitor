package config

// Mode is how the service is being run.
type Mode string

const (
	// ModeProduction is the default.
	ModeProduction Mode = "production"
	// ModeDevelopment turns on the conveniences that are unsafe to ship.
	ModeDevelopment Mode = "development"
)

// ApplicationConfig holds settings about the run as a whole.
type ApplicationConfig struct {
	Mode Mode `env:"MODE" envDefault:"production"`
}

// IsDevelopment reports whether the service is running in development mode.
func (c ApplicationConfig) IsDevelopment() bool {
	return c.Mode == ModeDevelopment
}
