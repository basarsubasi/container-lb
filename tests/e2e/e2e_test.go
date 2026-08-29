package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"container-lb/tests/testutil"
	"container-lb/userspace/discovery"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func TestE2E_LoadBalancer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("e2e test requires root (XDP attach + container netns inspection); re-run with sudo")
	}

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

	// Get client PID to find its host veth
	state, err := cClient.State(ctx)
	require.NoError(t, err)

	clientVethName, _, err := discovery.FindHostVeth(state.Pid)
	require.NoError(t, err)
	t.Logf("Discovered client veth interface: %s", clientVethName)

	// 6. Start the load balancer binary, passing the client veth as the ingress interface!
	// This avoids the EEXIST error from attaching Generic XDP to a bridge and its ports simultaneously,
	// and accurately models attaching to a host interface (e.g., eth0) in a real deployment.
	// The test process already runs as root (see root guard above).
	vip := "10.200.200.200"
	cmd := exec.Command("../../bin/container-lb",
		"-iface", clientVethName,
		"-label", "lb.e2e=true",
		"-vip", vip,
	)
	// Run the LB in its own process group so teardown can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	require.NoError(t, err)
	defer func() {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cmd.Wait()
		}
	}()

	// Wait for LB to initialize and discover backends
	time.Sleep(5 * time.Second)

	// 7. Test ICMP Ping to the VIP from the client container
	exitCode, pingOutReader, err := cClient.Exec(ctx, []string{"ping", "-c", "4", "-W", "2", vip})
	require.NoError(t, err, "Ping command failed to execute")

	// The exec stream is multiplexed by the Docker API; demultiplex it.
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, pingOutReader)
	require.NoError(t, err, "Failed to read ping output")

	pingOut := stdout.String()
	if pingOut == "" {
		pingOut = stderr.String()
	}
	t.Logf("Ping exit code: %d, output:\n%s", exitCode, pingOut)
	assert.Equal(t, 0, exitCode, "Ping command returned non-zero exit code")
	// nginx:alpine ships busybox ping; match its success line.
	assert.Contains(t, pingOut, "4 packets transmitted, 4 packets received, 0% packet loss", "Did not receive 4 ping replies")
}
