// Package config is the single place configuration is defined and loaded. It
// is loaded once, in cmd/, and carried to the rest of the program on the
// context — nothing outside this package calls Load.
package config

// Config is the whole of the service's configuration. Each section lives in
// its own file, and its envPrefix is what the environment variables are
// spelled with.
type Config struct {
	Application ApplicationConfig `envPrefix:"APP_"`
	Project     ProjectConfig     `envPrefix:"PROJECT_"`
	Docker      DockerConfig      `envPrefix:"DOCKER_"`
	Control     ControlConfig     `envPrefix:"CONTROL_"`
	Http        HttpConfig        `envPrefix:"HTTP_"`
	Log         LogConfig         `envPrefix:"LOG_"`
}
