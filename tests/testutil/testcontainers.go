package testutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartNginx starts an nginx alpine container with specific labels on a specific network.
func StartNginx(ctx context.Context, labels map[string]string, networkName string) (testcontainers.Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        "nginx:alpine",
		ExposedPorts: []string{"80/tcp"},
		WaitingFor:   wait.ForListeningPort("80/tcp"),
		Labels:       labels,
	}
	if networkName != "" {
		req.Networks = []string{networkName}
	}

	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

// CreateNetwork creates a test docker bridge network.
func CreateNetwork(ctx context.Context, name string) (testcontainers.Network, error) {
	req := testcontainers.GenericNetworkRequest{
		NetworkRequest: testcontainers.NetworkRequest{
			Name:   name,
			Driver: "bridge",
		},
	}
	return testcontainers.GenericNetwork(ctx, req)
}

// GetBridgeName resolves the host bridge interface name for a given docker network.
func GetBridgeName(netName string) (string, error) {
	out, err := exec.Command("docker", "network", "inspect", netName, "-f", "{{.Id}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker network inspect failed: %w", err)
	}
	netID := strings.TrimSpace(string(out))
	if len(netID) < 12 {
		return "", fmt.Errorf("invalid network ID returned: %s", netID)
	}
	return "br-" + netID[:12], nil
}
