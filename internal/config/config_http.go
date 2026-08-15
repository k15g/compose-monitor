package config

import (
	"net"
	"strconv"
	"time"
)

// HttpConfig is the HTTP server's configuration.
//
// Note the absence of a write timeout. It is not an oversight: Go applies
// Server.WriteTimeout as a deadline on the connection once the request
// headers are read, and an event stream is held open for hours. There is no
// way to exempt one route from it, so the server must not set it at all.
type HttpConfig struct {
	Host string `env:"HOST" envDefault:"0.0.0.0"`
	Port int    `env:"PORT" envDefault:"8080"`

	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" envDefault:"15s"`
	IdleTimeout       time.Duration `env:"IDLE_TIMEOUT" envDefault:"60s"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"10s"`

	// KeepAlive is how often the event stream emits a comment frame. Proxies
	// close a connection that has been idle too long, and a stream can be
	// legitimately idle for a long time when nothing changes.
	KeepAlive time.Duration `env:"KEEP_ALIVE" envDefault:"20s"`
}

// Addr is the address to listen on.
func (c HttpConfig) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
