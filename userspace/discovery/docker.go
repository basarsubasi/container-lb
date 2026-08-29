package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
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

	containers, err := w.cli.ContainerList(ctx, types.ContainerListOptions{
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
//     netlink.NewHandleAt opens a netlink socket inside the namespace without
//     altering the calling goroutine's own network namespace.
//  3. List interfaces inside the container and record the ifindex of the veth
//     (this is the container-side veth index, valid within its own namespace).
//  4. On the host, scan /sys/class/net/<iface>/iflink for every interface.
//     The kernel reports a veth's peer ifindex as iflink. If the peer is in a
//     different namespace, iflink equals the peer's ifindex in THAT namespace.
//     Match: host_iface.iflink == container_veth.ifindex → that is our host veth.
func FindHostVeth(containerPID int) (name string, ifIndex int, err error) {
	// Open the container's network namespace (does not change current goroutine's ns).
	ns, err := netns.GetFromPid(containerPID)
	if err != nil {
		return "", 0, fmt.Errorf("get container netns (pid %d): %w", containerPID, err)
	}
	defer ns.Close()

	// Create a netlink handle scoped to the container's network namespace.
	// NewHandleAt opens a netlink socket inside the given namespace without
	// altering the calling goroutine's own network namespace.
	h, err := netlink.NewHandleAt(ns)
	if err != nil {
		return "", 0, fmt.Errorf("create netlink handle in container netns: %w", err)
	}
	defer h.Delete() // netlink.Handle uses Delete(), not Close(), in v1.1.0

	// List interfaces in the container namespace.
	links, err := h.LinkList()
	if err != nil {
		return "", 0, fmt.Errorf("list links in container netns (pid %d): %w", containerPID, err)
	}

	// Find the veth and record its ifindex inside the container namespace.
	var containerVethIdx int
	for _, l := range links {
		if l.Type() == "veth" {
			containerVethIdx = l.Attrs().Index
			break
		}
	}
	if containerVethIdx == 0 {
		return "", 0, fmt.Errorf("no veth interface found in container (pid %d)", containerPID)
	}

	// Scan host interfaces via /sys/class/net to find the peer.
	// For a veth pair across namespaces, the host-side veth's iflink equals
	// the container-side veth's ifindex (in the container's namespace).
	return hostVethByPeerIdx(containerVethIdx)
}

// hostVethByPeerIdx scans /sys/class/net to find the host interface whose
// iflink matches the given peer interface index (from a different namespace).
func hostVethByPeerIdx(peerIdx int) (string, int, error) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return "", 0, fmt.Errorf("read /sys/class/net: %w", err)
	}

	for _, e := range entries {
		iface := e.Name()

		ifIndex, err := readSysNetInt(iface, "ifindex")
		if err != nil {
			continue
		}
		ifLink, err := readSysNetInt(iface, "iflink")
		if err != nil {
			continue
		}

		// A veth has iflink != ifindex.
		// When the peer is in another namespace, iflink = peer's ifindex in
		// that namespace — which matches containerVethIdx.
		if ifLink != ifIndex && ifLink == peerIdx {
			return iface, ifIndex, nil
		}
	}

	return "", 0, fmt.Errorf("no host veth found with peer ifindex %d", peerIdx)
}

// readSysNetInt reads an integer value from /sys/class/net/<iface>/<file>.
func readSysNetInt(iface, file string) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/%s", iface, file))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// Close releases the Docker client connection.
func (w *DockerWatcher) Close() error {
	return w.cli.Close()
}
