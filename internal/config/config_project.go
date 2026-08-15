package config

import "time"

// ProjectConfig identifies the compose project to watch.
type ProjectConfig struct {
	// Name is the project's name — the `name:` key of its compose file, which
	// is also what the runtime labels every container of the project with.
	// Required: there is no sensible default, and guessing would silently
	// monitor the wrong thing.
	Name string `env:"NAME,required"`

	// Title is what the page calls the project. Defaults to Name when unset.
	Title string `env:"TITLE"`

	// Interval is how often the project is re-read regardless of whether the
	// runtime reported a change. The event stream is the primary trigger;
	// this is the backstop for the case where it drops silently.
	Interval time.Duration `env:"INTERVAL" envDefault:"30s"`

	// Debounce is how long to wait for the runtime to stop reporting changes
	// before re-reading. A `compose up` emits a burst of events, and without
	// this each one would cost a full read.
	Debounce time.Duration `env:"DEBOUNCE" envDefault:"250ms"`
}

// DisplayTitle is the name to show for the project.
func (c ProjectConfig) DisplayTitle() string {
	if c.Title != "" {
		return c.Title
	}
	return c.Name
}
