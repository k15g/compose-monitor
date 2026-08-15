package config

import "context"

// contextKey is unexported so no other package can collide with it.
type contextKey struct{}

// WithConfig returns a context carrying cfg. It is called once, in cmd/,
// right after loading.
func WithConfig(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, contextKey{}, cfg)
}

// GetConfig returns the configuration carried by ctx.
//
// It panics when there is none. That is deliberate: every constructor in the
// service reads its configuration this way, so a context without one is a
// wiring bug in cmd/ that should fail loudly at startup rather than surface
// later as an empty setting.
func GetConfig(ctx context.Context) *Config {
	cfg, ok := ctx.Value(contextKey{}).(*Config)
	if !ok {
		panic("config: no configuration in context — cmd/ must call config.WithConfig")
	}
	return cfg
}
