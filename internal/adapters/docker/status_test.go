package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusKindDropsTheElapsedTime(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		// Running, across every duration the runtime's humaniser produces.
		{"Up Less than a second", "Up"},
		{"Up 1 second", "Up"},
		{"Up 45 seconds", "Up"},
		{"Up About a minute", "Up"},
		{"Up 3 minutes", "Up"},
		{"Up About an hour", "Up"},
		{"Up 5 hours", "Up"},
		{"Up 3 days", "Up"},
		{"Up 2 weeks", "Up"},
		{"Up 4 months", "Up"},
		{"Up 2 years", "Up"},

		// Health is a separate field, but it rides along in the status line.
		{"Up 5 minutes (healthy)", "Up (healthy)"},
		{"Up 3 seconds (health: starting)", "Up (health: starting)"},
		{"Up 5 minutes (Paused)", "Up (Paused)"},

		// The exit code is the part that carries meaning, and it stays.
		{"Exited (0) 2 hours ago", "Exited (0)"},
		{"Exited (137) 3 minutes ago", "Exited (137)"},
		{"Restarting (1) 4 seconds ago", "Restarting (1)"},

		// Statuses with no duration in them are left alone.
		{"Created", "Created"},
		{"Dead", "Dead"},
		{"Removal In Progress", "Removal In Progress"},
		{"", ""},
	}

	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			assert.Equal(t, test.want, statusKind(test.status))
		})
	}
}

func TestStatusKindTellsTheSameContainerFromADifferentOne(t *testing.T) {
	// The whole point: a container nothing has happened to compares equal
	// between two reads, while a real change still comes through.
	assert.Equal(t, statusKind("Up 3 minutes"), statusKind("Up 4 minutes"))
	assert.Equal(t, statusKind("Exited (0) 1 hour ago"), statusKind("Exited (0) 2 hours ago"))

	assert.NotEqual(t, statusKind("Up 3 minutes"), statusKind("Exited (0) 1 second ago"))
	assert.NotEqual(t, statusKind("Exited (0) 1 hour ago"), statusKind("Exited (137) 1 hour ago"))
	assert.NotEqual(t, statusKind("Up 3 minutes"), statusKind("Up 3 minutes (healthy)"))
}

func TestStatusElapsedKeepsOnlyTheTime(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"Up 3 minutes", "3 minutes"},
		{"Up 5 minutes (healthy)", "5 minutes"},
		{"Up Less than a second", "Less than a second"},
		{"Up About an hour", "About an hour"},
		{"Up About a minute", "About a minute"},

		// The state is a badge of its own, so the exit code and the word in
		// front of the time are not repeated beside it.
		{"Exited (0) 2 hours ago", "2 hours ago"},
		{"Exited (137) 3 minutes ago", "3 minutes ago"},
		{"Restarting (1) 4 seconds ago", "4 seconds ago"},

		// Nothing to give.
		{"Created", ""},
		{"Dead", ""},
		{"Removal In Progress", ""},
		{"", ""},
	}

	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			assert.Equal(t, test.want, statusElapsed(test.status))
		})
	}
}

func TestTheTwoHalvesOfAStatus(t *testing.T) {
	// Between them, statusKind and statusElapsed account for what a status
	// says: what happened, and when.
	for _, status := range []string{
		"Up 3 minutes", "Up 5 minutes (healthy)", "Exited (0) 2 hours ago", "Created",
	} {
		t.Run(status, func(t *testing.T) {
			elapsed := statusElapsed(status)
			if elapsed == "" {
				// A status with no time in it is all kind.
				assert.Equal(t, status, statusKind(status))
				return
			}

			assert.Contains(t, status, elapsed, "the time is taken from the status, not invented")
			assert.NotContains(t, statusKind(status), elapsed, "and the kind is what is left of it")
		})
	}
}
