package config

// DockerConfig locates the container runtime.
type DockerConfig struct {
	// Host is the endpoint to talk to. The default is the local socket, which
	// is what the service is meant to be given — read-only.
	Host string `env:"HOST" envDefault:"unix:///var/run/docker.sock"`
}
