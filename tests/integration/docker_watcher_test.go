package integration

import (
	"context"
	"testing"
	"time"

	"container-lb/tests/testutil"
	"container-lb/userspace/discovery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerWatcher_DiscoverBackends(t *testing.T) {
	ctx := context.Background()

	// Spin up a container with the load balancer label using testutil
	container, err := testutil.StartNginx(ctx, map[string]string{"lb.enable": "true"}, "")
	require.NoError(t, err)
	defer func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
	}()

	// Wait slightly just to be completely sure the container is registered in Docker properly
	time.Sleep(1 * time.Second)

	watcher, err := discovery.NewDockerWatcher("lb.enable=true", 80)
	require.NoError(t, err)

	backends, err := watcher.DiscoverBackends(ctx)
	require.NoError(t, err)

	assert.Len(t, backends, 1)
	
	if len(backends) == 1 {
		b := backends[0]
		assert.NotEmpty(t, b.IP)
		assert.NotEmpty(t, b.MAC)
		assert.NotEmpty(t, b.VethName)
		assert.Greater(t, b.VethIdx, 0)
	}
}
