package discovery

import (
	"fmt"
	"net"
	"sync"
)

// BPFUpdater defines the kernel-side map and XDP program lifecycle operations
// required by the discovery pool.
type BPFUpdater interface {
	UpdateBackend(idx uint32, ip net.IP, port uint16, mac net.HardwareAddr) error
	ClearBackend(idx uint32, ip net.IP) error
	UpdateConfig(vip net.IP, vport uint16, backendCount uint32, srcMAC net.HardwareAddr) error
	AttachVethXDP(vethName string, vethIfIndex int) error
	DetachVethXDP(vethName string) error
}

// Pool reconciles discovered backends with the kernel BPF maps and manages
// per-container host-side veth XDP attachments for SNAT on the return path.
type Pool struct {
	mu         sync.RWMutex
	bpfManager BPFUpdater
	backends   []Backend       // snapshot of last-synced backends
	byVeth     map[string]bool // set of currently-attached veth names
	vip        net.IP
	vport      uint16
	srcMAC     net.HardwareAddr
}

// NewPool initialises the backend pool.
func NewPool(bpfManager BPFUpdater, vip net.IP, vport uint16, srcMAC net.HardwareAddr) *Pool {
	return &Pool{
		bpfManager: bpfManager,
		byVeth:     make(map[string]bool),
		vip:        vip,
		vport:      vport,
		srcMAC:     srcMAC,
	}
}

// Sync reconciles the new backend list with both the BPF maps and the set of
// per-veth XDP SNAT programs:
//  1. Writes/updates backend_map + backend_ips_map for new/changed backends.
//  2. Attaches xdp_veth_snat to host-side veths of newly-added backends.
//  3. Clears BPF entries and detaches XDP from veths of removed backends.
//  4. Updates config_map (VIP / count) atomically last.
func (p *Pool) Sync(newBackends []Backend) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Build a map of old backends keyed by veth name for O(1) diff.
	oldByVeth := make(map[string]Backend, len(p.backends))
	for _, b := range p.backends {
		oldByVeth[b.VethName] = b
	}

	// ── Write new backend entries ──────────────────────────────────────────
	for i, b := range newBackends {
		// If this index was previously used by a different IP, clear the old IP
		// from the reverse map (backend_ips_map) so we don't leak entries.
		if i < len(p.backends) && !p.backends[i].IP.Equal(b.IP) {
			_ = p.bpfManager.ClearBackend(uint32(i), p.backends[i].IP)
		}

		if err := p.bpfManager.UpdateBackend(uint32(i), b.IP, b.Port, b.MAC); err != nil {
			return fmt.Errorf("write backend %d (%s): %w", i, b.Name, err)
		}

		// Attach veth XDP SNAT program if not already attached.
		if !p.byVeth[b.VethName] {
			if err := p.bpfManager.AttachVethXDP(b.VethName, b.VethIdx); err != nil {
				return fmt.Errorf("attach veth XDP for %s (%s): %w", b.Name, b.VethName, err)
			}
			p.byVeth[b.VethName] = true
			fmt.Printf("[+] Attached XDP SNAT on veth %s (backend %s %s)\n",
				b.VethName, b.Name, b.IP)
		}

		delete(oldByVeth, b.VethName) // mark as still-active
	}

	// ── Remove stale backends (were active, no longer in new list) ─────────
	staleCount := len(p.backends) - len(newBackends)
	for i := 0; i < staleCount; i++ {
		idx := uint32(len(newBackends) + i)
		old := p.backends[len(newBackends)+i]
		if err := p.bpfManager.ClearBackend(idx, old.IP); err != nil {
			return fmt.Errorf("clear backend %d (%s): %w", idx, old.Name, err)
		}
	}

	// Detach XDP from veths no longer present in the new list.
	for vethName, old := range oldByVeth {
		if err := p.bpfManager.DetachVethXDP(vethName); err != nil {
			return fmt.Errorf("detach veth XDP for %s (%s): %w", old.Name, vethName, err)
		}
		delete(p.byVeth, vethName)
		fmt.Printf("[-] Detached XDP SNAT from veth %s (backend %s removed)\n",
			vethName, old.Name)
	}

	// ── Update config_map last so XDP sees consistent state ────────────────
	if err := p.bpfManager.UpdateConfig(p.vip, p.vport, uint32(len(newBackends)), p.srcMAC); err != nil {
		return fmt.Errorf("update BPF config: %w", err)
	}

	p.backends = newBackends
	return nil
}

// GetBackends returns a point-in-time snapshot of active backends.
func (p *Pool) GetBackends() []Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]Backend, len(p.backends))
	copy(out, p.backends)
	return out
}
