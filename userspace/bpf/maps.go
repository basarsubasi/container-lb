package bpf

import (
	"encoding/binary"
	"fmt"
	"net"
)

// UpdateConfig writes the global config (VIP, target port, active backend count,
// docker0 MAC) into the BPF config_map at index 0.
func (m *Manager) UpdateConfig(vip net.IP, vport uint16, backendCount uint32, srcMAC net.HardwareAddr) error {
	var vipU32 uint32
	if v4 := vip.To4(); v4 != nil {
		vipU32 = binary.BigEndian.Uint32(v4)
	}

	var mac [6]uint8
	if len(srcMAC) == 6 {
		copy(mac[:], srcMAC)
	}

	cfg := lbLbConfig{
		Vip:          vipU32,
		Vport:        vport,
		BackendCount: backendCount,
		SrcMac:       mac,
	}

	zero := uint32(0)
	if err := m.objs.ConfigMap.Put(&zero, &cfg); err != nil {
		return fmt.Errorf("config_map put: %w", err)
	}
	return nil
}

// UpdateBackend writes a backend entry at index in backend_map and registers
// its IPv4 address in backend_ips_map for O(1) reverse lookup on the egress path.
func (m *Manager) UpdateBackend(index uint32, ip net.IP, port uint16, mac net.HardwareAddr) error {
	v4 := ip.To4()
	if v4 == nil {
		return fmt.Errorf("backend IP must be IPv4: %s", ip)
	}

	var macBytes [6]uint8
	if len(mac) == 6 {
		copy(macBytes[:], mac)
	}

	backend := lbBackendInfo{
		Ipv4:   binary.BigEndian.Uint32(v4),
		Port:   port,
		Mac:    macBytes,
		Weight: 1,
	}

	// Write forward entry: index → backend info
	if err := m.objs.BackendMap.Put(&index, &backend); err != nil {
		return fmt.Errorf("backend_map put idx=%d: %w", index, err)
	}

	// Write reverse entry: backend IPv4 → index (used by TC egress for SNAT)
	ipKey := binary.BigEndian.Uint32(v4)
	if err := m.objs.BackendIpsMap.Put(&ipKey, &index); err != nil {
		return fmt.Errorf("backend_ips_map put ip=%s: %w", ip, err)
	}

	return nil
}

// ClearBackend zeroes the backend entry at index and removes it from
// backend_ips_map so the egress SNAT path stops rewriting its packets.
func (m *Manager) ClearBackend(index uint32, ip net.IP) error {
	// Zero the backend_map entry
	var empty lbBackendInfo
	if err := m.objs.BackendMap.Put(&index, &empty); err != nil {
		return fmt.Errorf("backend_map clear idx=%d: %w", index, err)
	}

	// Remove from reverse IP map (ignore not-found errors)
	if v4 := ip.To4(); v4 != nil {
		ipKey := binary.BigEndian.Uint32(v4)
		_ = m.objs.BackendIpsMap.Delete(&ipKey)
	}

	return nil
}

// ReadStats reads and sums the per-CPU counters for a given backend index.
func (m *Manager) ReadStats(index uint32) (packets uint64, bytes uint64, err error) {
	var perCPU []lbStatsCounter
	if err = m.objs.CountersMap.Lookup(&index, &perCPU); err != nil {
		return 0, 0, err
	}
	for _, s := range perCPU {
		packets += s.RxPackets
		bytes += s.RxBytes
	}
	return packets, bytes, nil
}
