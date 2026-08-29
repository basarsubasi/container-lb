package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"container-lb/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func TestE2E_LoadBalancer(t *testing.T) {
	ctx := context.Background()

	// 1. Create a dedicated test network
	netName := "lb-e2e-net"
	netw, err := testutil.CreateNetwork(ctx, netName)
	require.NoError(t, err)
	defer netw.Remove(ctx)

	// 2. Start two backends
	labels := map[string]string{"lb.e2e": "true"}

	c1, err := testutil.StartNginx(ctx, labels, netName)
	require.NoError(t, err)
	defer c1.Terminate(ctx)

	c2, err := testutil.StartNginx(ctx, labels, netName)
	require.NoError(t, err)
	defer c2.Terminate(ctx)

	// Wait for network propagation
	time.Sleep(2 * time.Second)

	// 3. Find the bridge interface name for the network
	bridgeName, err := testutil.GetBridgeName(netName)
	require.NoError(t, err)
	t.Logf("Discovered bridge interface: %s", bridgeName)

	// 5. Start a client container to send the ping
	reqClient := testcontainers.ContainerRequest{
		Image:    "nginx:alpine",
		Networks: []string{netName},
		Cmd:      []string{"sleep", "infinity"},
	}
	cClient, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: reqClient,
		Started:          true,
	})
	require.NoError(t, err)
	defer cClient.Terminate(ctx)

	// 6. Start the load balancer binary
	vip := "10.200.200.200"
	cmd := exec.Command("sudo", "../../bin/container-lb",
		"-iface", bridgeName,
		"-label", "lb.e2e=true",
		"-vip", vip,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	err = cmd.Start()
	require.NoError(t, err)
	defer func() {
		if cmd.Process != nil {
			exec.Command("sudo", "kill", "-9", fmt.Sprintf("%d", cmd.Process.Pid)).Run()
			cmd.Process.Kill()
		}
	}()

	// Wait for LB to initialize and discover backends
	time.Sleep(5 * time.Second)

	// 7. Test ICMP Ping to the VIP from the client container
	exitCode, pingOutReader, err := cClient.Exec(ctx, []string{"ping", "-c", "4", "-W", "2", vip})
	
	pingOut, _ := io.ReadAll(pingOutReader)
	t.Logf("Ping exit code: %d, output:\n%s", exitCode, string(pingOut))
	assert.NoError(t, err, "Ping command failed to execute")
	assert.Equal(t, 0, exitCode, "Ping command returned non-zero exit code")
	assert.Contains(t, string(pingOut), "4 packets transmitted, 4 received", "Did not receive 4 ping replies")
}
