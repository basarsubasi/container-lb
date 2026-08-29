package bpf

import (
	"fmt"
	"net"
	"sync"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Manager handles the lifecycle of all XDP programs:
//   - One XDP on docker0 ingress (DNAT: client → backend)
//   - One XDP per container's host-side veth ingress (SNAT: backend → VIP)
type Manager struct {
	objs      lbObjects
	xdpLink   link.Link            // docker0 ingress
	vethLinks map[string]link.Link // key = veth interface name
	mu        sync.Mutex
	ifaceName string
	ifaceIdx  int
}

// NewManager removes memory limits, loads the compiled BPF collection, and
// attaches the XDP DNAT program to docker0 ingress.
// Per-container veth SNAT programs are attached later via AttachVethXDP.
func NewManager(ifaceName string) (*Manager, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock: %w", err)
	}

	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %q not found: %w", ifaceName, err)
	}

	var objs lbObjects
	if err := loadLbObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("load BPF objects: %w", err)
	}

	m := &Manager{
		objs:      objs,
		vethLinks: make(map[string]link.Link),
		ifaceName: ifaceName,
		ifaceIdx:  iface.Index,
	}

	// Attach the DNAT program to the ingress interface.
	// Try native driver mode first; veth/bridge interfaces may require generic.
	xdpLink, err := link.AttachXDP(link.XDPOptions{
		Program:   objs.XdpLbIngress,
		Interface: iface.Index,
		Flags:     link.XDPDriverMode,
	})
	if err != nil {
		xdpLink, err = link.AttachXDP(link.XDPOptions{
			Program:   objs.XdpLbIngress,
			Interface: iface.Index,
			Flags:     link.XDPGenericMode,
		})
		if err != nil {
			objs.Close()
			return nil, fmt.Errorf("attach XDP to %s (driver+generic both failed): %w", ifaceName, err)
		}
	}
	m.xdpLink = xdpLink

	return m, nil
}

// AttachVethXDP attaches the SNAT XDP program to a container's host-side veth.
// Safe to call concurrently. No-ops if already attached to this veth.
func (m *Manager) AttachVethXDP(vethName string, vethIfIndex int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.vethLinks[vethName]; exists {
		return nil // already attached
	}

	// Veths typically support native XDP in modern kernels.
	l, err := link.AttachXDP(link.XDPOptions{
		Program:   m.objs.XdpVethSnat,
		Interface: vethIfIndex,
		Flags:     link.XDPDriverMode,
	})
	if err != nil {
		errDriver := err
		l, err = link.AttachXDP(link.XDPOptions{
			Program:   m.objs.XdpVethSnat,
			Interface: vethIfIndex,
			Flags:     link.XDPGenericMode,
		})
		if err != nil {
			return fmt.Errorf("driver mode err: %v, generic mode err: %v", errDriver, err)
		}
	}

	m.vethLinks[vethName] = l
	return nil
}

// DetachVethXDP removes the SNAT XDP program from a container's host-side veth.
// Safe to call concurrently. No-ops if not currently attached.
func (m *Manager) DetachVethXDP(vethName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	l, exists := m.vethLinks[vethName]
	if !exists {
		return nil
	}

	if err := l.Close(); err != nil {
		return fmt.Errorf("detach XDP SNAT from veth %s: %w", vethName, err)
	}
	delete(m.vethLinks, vethName)
	return nil
}

// Objects returns the loaded BPF collection (maps + programs).
func (m *Manager) Objects() *lbObjects {
	return &m.objs
}

// Close detaches all XDP programs (docker0 + all veths) and frees BPF resources.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error

	for name, l := range m.vethLinks {
		if err := l.Close(); err != nil {
			errs = append(errs, fmt.Errorf("detach veth %s: %w", name, err))
		}
		delete(m.vethLinks, name)
	}

	if m.xdpLink != nil {
		if err := m.xdpLink.Close(); err != nil {
			errs = append(errs, fmt.Errorf("detach docker0 XDP: %w", err))
		}
	}

	if err := m.objs.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close BPF objects: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("BPF manager close: %v", errs)
	}
	return nil
}
