package templates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	templates "github.com/k15g/compose-monitor/internal/adapters/web/shared/templates"
	"github.com/k15g/compose-monitor/internal/domain"
)

func TestTrail(t *testing.T) {
	tests := []struct {
		name string
		got  []templates.Crumb
		want []templates.Crumb
	}{
		{
			// A trail that reaches only its section is a section page, so that
			// step is the current one and is not linked.
			name: "a section on its own",
			got:  templates.Trail("Example project", "Services", "/services"),
			want: []templates.Crumb{
				{Label: "Example project", Href: "/"},
				{Label: "Services"},
			},
		},
		{
			name: "one level down",
			got:  templates.Trail("Example project", "Services", "/services", "web"),
			want: []templates.Crumb{
				{Label: "Example project", Href: "/"},
				{Label: "Services", Href: "/services"},
				{Label: "web"},
			},
		},
		{
			name: "two levels down",
			got:  templates.Trail("Example project", "Services", "/services", "web", "/services/c1", "Log"),
			want: []templates.Crumb{
				{Label: "Example project", Href: "/"},
				{Label: "Services", Href: "/services"},
				{Label: "web", Href: "/services/c1"},
				{Label: "Log"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.got)

			// The last crumb is the page itself, and a link to where you
			// already are is noise.
			assert.Empty(t, test.got[len(test.got)-1].Href)

			// The first is always the project, and always the way home.
			assert.Equal(t, "/", test.got[0].Href)
		})
	}
}

func TestShortImage(t *testing.T) {
	const digest = "sha256:1010acc839eccd5694743efd676ada2ff40e0dedc6dc75025ecbc33976821a9c"

	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "the case this exists for",
			image: "ghcr.io/example/api@" + digest,
			want:  "ghcr.io/example/api@sha256:1010acc839ec",
		},
		{
			name:  "a tag and a digest together",
			image: "ghcr.io/example/thing:1.4.2@" + digest,
			want:  "ghcr.io/example/thing:1.4.2@sha256:1010acc839ec",
		},
		{
			name:  "another algorithm",
			image: "example@sha512:1010acc839eccd5694743efd676ada2ff40e0dedc6dc75025ecbc33976821a9c",
			want:  "example@sha512:1010acc839ec",
		},

		// Everything that is already short enough, or is not a digest at all,
		// is left exactly as it came.
		{name: "a plain tag", image: "postgres:18", want: "postgres:18"},
		{name: "no tag at all", image: "postgres", want: "postgres"},
		{name: "a registry and a port", image: "registry:5000/thing:1", want: "registry:5000/thing:1"},
		{name: "a digest already short", image: "thing@sha256:abc", want: "thing@sha256:abc"},
		{name: "exactly the cut length", image: "thing@sha256:1010acc839ec", want: "thing@sha256:1010acc839ec"},
		{name: "an at with no colon after it", image: "thing@nonsense", want: "thing@nonsense"},
		{name: "empty", image: "", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, templates.ShortImage(test.image))
		})
	}
}

func TestUptimeLabel(t *testing.T) {
	tests := []struct {
		name    string
		service domain.Service
		want    string
	}{
		{
			name:    "running",
			service: domain.Service{Status: "Up 2 hours (healthy)", Elapsed: "2 hours"},
			want:    "2 hours",
		},
		{
			// The state has a badge of its own beside this one, so repeating
			// "Exited (0)" here would say it twice.
			name:    "stopped",
			service: domain.Service{Status: "Exited (0) 2 hours ago", Elapsed: "2 hours ago"},
			want:    "2 hours ago",
		},
		{
			name:    "restarting",
			service: domain.Service{Status: "Restarting (1) 4 seconds ago", Elapsed: "4 seconds ago"},
			want:    "4 seconds ago",
		},
		{
			// No time in the status at all: the status itself says more than
			// an empty badge would.
			name:    "never started",
			service: domain.Service{Status: "Created"},
			want:    "Created",
		},
		{
			name:    "nothing at all",
			service: domain.Service{},
			want:    "—",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, templates.UptimeLabel(test.service))
		})
	}
}
