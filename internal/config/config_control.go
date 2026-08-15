package config

// ControlConfig governs whether the service may act on containers, as opposed
// to only reporting on them.
//
// It exists because the two are very different things to expose. Reading needs
// no protection beyond who can reach the page; starting, stopping and removing
// mean anyone who can reach the page can do those things, and the page has no
// authentication of its own.
type ControlConfig struct {
	// Enabled allows the action buttons and the endpoints behind them.
	//
	// Off by default, which is the opposite of what is convenient: the default
	// is what an install that has not thought about it gets, and a page that
	// only reports is the one that is safe not to have thought about. Turning
	// it on is a deliberate statement that whoever can reach this page is
	// allowed to stop and remove containers.
	//
	// Off does not mean the buttons are hidden. The service holds no handle
	// that can act on a container at all, and the endpoints refuse even when
	// called directly.
	Enabled bool `env:"ENABLED" envDefault:"false"`
}
