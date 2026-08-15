package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/config"
)

func TestActingOnContainersIsOffUnlessAskedFor(t *testing.T) {
	t.Setenv("PROJECT_NAME", "example")

	loaded, err := config.Load()
	require.NoError(t, err)

	// The default is what an install that has not thought about it gets, and
	// the page has no authentication of its own.
	assert.False(t, loaded.Control.Enabled)
}

func TestActingOnContainersIsTurnedOnDeliberately(t *testing.T) {
	t.Setenv("PROJECT_NAME", "example")
	t.Setenv("CONTROL_ENABLED", "true")

	loaded, err := config.Load()
	require.NoError(t, err)

	assert.True(t, loaded.Control.Enabled)
}

func TestTheProjectMustBeNamed(t *testing.T) {
	// No default: a guess would quietly monitor the wrong project, which is
	// worse than refusing to start.
	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROJECT_NAME")
}
