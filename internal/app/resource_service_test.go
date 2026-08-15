package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

type fakeNetworks struct {
	networks []domain.Network
	err      error
	removed  []string
}

var _ ports.NetworkSource = (*fakeNetworks)(nil)

func (f *fakeNetworks) List(context.Context) ([]domain.Network, error) { return f.networks, f.err }

func (f *fakeNetworks) Inspect(_ context.Context, id string) (domain.Network, error) {
	if f.err != nil {
		return domain.Network{}, f.err
	}
	for _, network := range f.networks {
		if network.ID == id {
			return network, nil
		}
	}
	return domain.Network{}, ports.ErrNotFound
}

type fakeVolumes struct {
	volumes []domain.Volume
	err     error
	removed []string
}

var _ ports.VolumeSource = (*fakeVolumes)(nil)

func (f *fakeVolumes) List(context.Context) ([]domain.Volume, error) { return f.volumes, f.err }

func (f *fakeVolumes) Inspect(_ context.Context, name string) (domain.Volume, error) {
	if f.err != nil {
		return domain.Volume{}, f.err
	}
	for _, volume := range f.volumes {
		if volume.Name == name {
			return volume, nil
		}
	}
	return domain.Volume{}, ports.ErrNotFound
}

func (f *fakeNetworks) Remove(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeVolumes) Remove(_ context.Context, name string) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, name)
	return nil
}

func TestNetworkService(t *testing.T) {
	ctx := testContext(t)
	source := &fakeNetworks{networks: []domain.Network{{ID: "n1", Name: "example_default"}}}
	service := NewNetworkService(ctx, source, newFakeSource(), nil)

	listed, err := service.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	found, err := service.Inspect(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, "example_default", found.Name)

	_, err = service.Inspect(ctx, "somebody-elses")
	assert.ErrorIs(t, err, ports.ErrNotFound,
		"the not-found sentinel survives wrapping, because the handler switches on it")
}

func TestNetworkServiceReportsAnUnreachableRuntime(t *testing.T) {
	ctx := testContext(t)
	service := NewNetworkService(ctx, &fakeNetworks{err: errors.New("socket gone")}, newFakeSource(), nil)

	_, listErr := service.List(ctx)
	_, inspectErr := service.Inspect(ctx, "n1")

	require.Error(t, listErr)
	assert.Contains(t, listErr.Error(), "socket gone")
	require.Error(t, inspectErr)
	assert.Contains(t, inspectErr.Error(), "socket gone")
}

func TestVolumeService(t *testing.T) {
	ctx := testContext(t)
	source := &fakeVolumes{volumes: []domain.Volume{{Name: "example_pgdata", Driver: "local"}}}
	service := NewVolumeService(ctx, source, newFakeSource(), nil)

	listed, err := service.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	found, err := service.Inspect(ctx, "example_pgdata")
	require.NoError(t, err)
	assert.Equal(t, "local", found.Driver)

	_, err = service.Inspect(ctx, "somebody-elses")
	assert.ErrorIs(t, err, ports.ErrNotFound)
}

func TestVolumeServiceReportsAnUnreachableRuntime(t *testing.T) {
	ctx := testContext(t)
	service := NewVolumeService(ctx, &fakeVolumes{err: errors.New("socket gone")}, newFakeSource(), nil)

	_, listErr := service.List(ctx)
	_, inspectErr := service.Inspect(ctx, "v1")

	require.Error(t, listErr)
	assert.Contains(t, listErr.Error(), "socket gone")
	require.Error(t, inspectErr)
	assert.Contains(t, inspectErr.Error(), "socket gone")
}

// --- removing networks and volumes ------------------------------------------

// usedBy builds a container source reporting the given usage counts.
func usedBy(networks, volumes map[string]int) *fakeSource {
	source := newFakeSource()
	source.usage = domain.ResourceUsage{Networks: networks, Volumes: volumes}
	return source
}

func TestNetworkRemoveRefusesOneStillInUse(t *testing.T) {
	ctx := testContext(t)
	source := &fakeNetworks{networks: []domain.Network{{ID: "n1", Name: "example_default"}}}
	service := NewNetworkService(ctx, source, usedBy(map[string]int{"example_default": 2}, nil), source)

	err := service.Remove(ctx, "n1")

	assert.ErrorIs(t, err, ErrInUse)
	assert.Empty(t, source.removed, "the runtime is never asked")
}

func TestNetworkRemoveDeletesAnUnusedOne(t *testing.T) {
	ctx := testContext(t)
	source := &fakeNetworks{networks: []domain.Network{{ID: "n1", Name: "example_default"}}}
	service := NewNetworkService(ctx, source, usedBy(nil, nil), source)

	require.NoError(t, service.Remove(ctx, "n1"))

	assert.Equal(t, []string{"n1"}, source.removed)
}

func TestNetworkInUseCountsAttachedMembers(t *testing.T) {
	ctx := testContext(t)
	// A network reports its own members on inspect, and that is what the
	// runtime judges a removal on — so it wins over the container count.
	source := &fakeNetworks{networks: []domain.Network{{
		ID: "n1", Name: "example_default",
		Members: []domain.NetworkMember{{ContainerID: "c1"}, {ContainerID: "c2"}},
	}}}
	service := NewNetworkService(ctx, source, usedBy(nil, nil), source)

	found, err := service.Inspect(ctx, "n1")

	require.NoError(t, err)
	assert.Equal(t, 2, found.UsedBy)
	assert.True(t, found.InUse())
	assert.ErrorIs(t, service.Remove(ctx, "n1"), ErrInUse)
}

func TestVolumeRemoveRefusesOneStillMounted(t *testing.T) {
	ctx := testContext(t)
	source := &fakeVolumes{volumes: []domain.Volume{{Name: "example_pgdata"}}}
	service := NewVolumeService(ctx, source, usedBy(nil, map[string]int{"example_pgdata": 1}), source)

	err := service.Remove(ctx, "example_pgdata")

	assert.ErrorIs(t, err, ErrInUse)
	assert.Empty(t, source.removed, "this is the only action that destroys data; it is not attempted on a guess")
}

func TestVolumeRemoveDeletesAnUnusedOne(t *testing.T) {
	ctx := testContext(t)
	source := &fakeVolumes{volumes: []domain.Volume{{Name: "example_pgdata"}}}
	service := NewVolumeService(ctx, source, usedBy(nil, nil), source)

	require.NoError(t, service.Remove(ctx, "example_pgdata"))

	assert.Equal(t, []string{"example_pgdata"}, source.removed)
}

func TestRemovalIsRefusedWhenControlIsDisabled(t *testing.T) {
	ctx := testContext(t)
	networks := &fakeNetworks{networks: []domain.Network{{ID: "n1", Name: "n"}}}
	volumes := &fakeVolumes{volumes: []domain.Volume{{Name: "v"}}}

	assert.ErrorIs(t, NewNetworkService(ctx, networks, usedBy(nil, nil), nil).Remove(ctx, "n1"), ErrControlDisabled)
	assert.ErrorIs(t, NewVolumeService(ctx, volumes, usedBy(nil, nil), nil).Remove(ctx, "v"), ErrControlDisabled)
}

func TestUsageFailureDoesNotFailTheListing(t *testing.T) {
	ctx := testContext(t)
	broken := newFakeSource()
	broken.fail(errors.New("socket gone"))

	// Usage decides whether a remove button is drawn and nothing else. A page
	// without the button is still worth serving.
	networks, err := NewNetworkService(ctx, &fakeNetworks{networks: []domain.Network{{ID: "n1", Name: "n"}}}, broken, nil).List(ctx)
	require.NoError(t, err)
	require.Len(t, networks, 1)
	assert.False(t, networks[0].InUse())

	volumes, err := NewVolumeService(ctx, &fakeVolumes{volumes: []domain.Volume{{Name: "v"}}}, broken, nil).List(ctx)
	require.NoError(t, err)
	require.Len(t, volumes, 1)
}
