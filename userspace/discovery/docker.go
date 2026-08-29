package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// Backend represents a discovered, healthy container backend.
type Backend struct {
	ID           string
	Name         string
	IP           net.IP
	MAC          net.HardwareAddr
	Port         uint16
	Healthy      bool
	ContainerPID int    // host PID of the container init process
	VethName     string // host-side veth interface name
	VethIdx      int    // host-side veth interface index
}

// DockerWatcher discovers backend containers via the Docker API.
type DockerWatcher struct {
	cli         *client.Client
	labelFilter string
	targetPort  uint16
}

// NewDockerWatcher creates a Docker API client.
func NewDockerWatcher(labelFilter string, targetPort uint16) (*DockerWatcher, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	return &DockerWatcher{
		cli:         cli,
		labelFilter: labelFilter,
		targetPort:  targetPort,
	}, nil
}

// DiscoverBackends lists running containers matching the label filter and
// resolves their IP, MAC, PID, and host-side veth.
func (w *DockerWatcher) DiscoverBackends(ctx context.Context) ([]Backend, error) {
	filterArgs := filters.NewArgs()
	if w.labelFilter != "" {
		filterArgs.Add("label", w.labelFilter)
	}

	containers, err := w.cli.ContainerList(ctx, container.ListOptions{
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	var backends []Backend
	for _, c := range containers {
		inspect, err := w.cli.ContainerInspect(ctx, c.ID)
		if err != nil || !inspect.State.Running {
			continue
		}

		// Skip unhealthy containers (if a health check is configured).
		if inspect.State.Health != nil {
			status := inspect.State.Health.Status
			if status != "healthy" && status != "none" {
				continue
			}
		}

		// Extract IPv4 and MAC from the bridge network settings.
		var ip net.IP
		var mac net.HardwareAddr
		for _, ns := range inspect.NetworkSettings.Networks {
			if ns.IPAddress == "" {
				continue
			}
			parsed := net.ParseIP(ns.IPAddress)
			if parsed == nil || parsed.To4() == nil {
				continue
			}
			parsedMAC, err := net.ParseMAC(ns.MacAddress)
			if err != nil || len(parsedMAC) != 6 {
				continue
			}
			ip = parsed
			mac = parsedMAC
			break
		}
		if ip == nil {
			continue
		}

		// Resolve port (per-container label override or the global default).
		port := w.targetPort
		if portLabel, ok := c.Labels["lb.port"]; ok {
			if p, err := strconv.ParseUint(strings.TrimSpace(portLabel), 10, 16); err == nil {
				port = uint16(p)
			}
		}

		// Discover the host-side veth by looking inside the container's netns.
		pid := inspect.State.Pid
		vethName, vethIdx, err := FindHostVeth(pid)
		if err != nil {
			fmt.Printf("[WARN] Could not find host veth for container %s (pid %d): %v\n",
				inspect.Name, pid, err)
			continue
		}

		name := strings.TrimPrefix(inspect.Name, "/")
		backends = append(backends, Backend{
			ID:           c.ID[:12],
			Name:         name,
			IP:           ip,
			MAC:          mac,
			Port:         port,
			Healthy:      true,
			ContainerPID: pid,
			VethName:     vethName,
			VethIdx:      vethIdx,
		})
	}

	return backends, nil
}

// FindHostVeth discovers the host-side veth interface for a given container PID.
//
// Strategy (no goroutine thread-switching / Setns required):
//  1. Open the container's network namespace via netns.GetFromPid.
//  2. Create a netlink handle that operates inside that namespace.
//  3. List interfaces inside the container and find the veth.
//  4. For a veth pair across namespaces, the container veth's iflink
//     (mapped to ParentIndex in netlink) is the ifindex of the host veth
//     in the host namespace!
//  5. Look up the host interface by its ifindex.
func FindHostVeth(containerPID int) (name string, ifIndex int, err error) {
	// Open the container's network namespace
	ns, err := netns.GetFromPid(containerPID)
	if err != nil {
		return "", 0, fmt.Errorf("get container netns (pid %d): %w", containerPID, err)
	}
	defer ns.Close()

	// Create a netlink handle scoped to the container's network namespace
	h, err := netlink.NewHandleAt(ns)
	if err != nil {
		return "", 0, fmt.Errorf("create netlink handle in container netns: %w", err)
	}
	defer h.Delete()

	// List interfaces in the container namespace
	links, err := h.LinkList()
	if err != nil {
		return "", 0, fmt.Errorf("list links in container netns (pid %d): %w", containerPID, err)
	}

	// Find the veth and record its iflink (ParentIndex)
	var hostVethIdx int
	for _, l := range links {
		fmt.Printf("DEBUG: FindHostVeth(pid %d) found link %s (type %s), index %d, parent %d\n", containerPID, l.Attrs().Name, l.Type(), l.Attrs().Index, l.Attrs().ParentIndex)
		if l.Type() == "veth" {
			hostVethIdx = l.Attrs().ParentIndex
			break
		}
	}
	if hostVethIdx == 0 {
		return "", 0, fmt.Errorf("no veth interface found in container (pid %d)", containerPID)
	}

	// Fetch the host interface by its ifindex (in the host namespace)
	hostLink, err := netlink.LinkByIndex(hostVethIdx)
	if err != nil {
		return "", 0, fmt.Errorf("find host veth by index %d: %w", hostVethIdx, err)
	}
	fmt.Printf("DEBUG: FindHostVeth(pid %d) returning host link %s (index %d)\n", containerPID, hostLink.Attrs().Name, hostVethIdx)

	return hostLink.Attrs().Name, hostVethIdx, nil
}

// Close releases the Docker client connection.
func (w *DockerWatcher) Close() error {
	return w.cli.Close()
}
