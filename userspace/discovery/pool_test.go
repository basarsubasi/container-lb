package discovery

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockBPFManager struct {
	updatedBackends map[uint32]net.IP
	clearedBackends map[uint32]net.IP
	attachedVeths   map[string]int
	detachedVeths   map[string]bool
	configUpdates   int
}

func newMockBPFManager() *mockBPFManager {
	return &mockBPFManager{
		updatedBackends: make(map[uint32]net.IP),
		clearedBackends: make(map[uint32]net.IP),
		attachedVeths:   make(map[string]int),
		detachedVeths:   make(map[string]bool),
	}
}

func (m *mockBPFManager) UpdateBackend(idx uint32, ip net.IP, port uint16, mac net.HardwareAddr) error {
	m.updatedBackends[idx] = ip
	return nil
}

func (m *mockBPFManager) ClearBackend(idx uint32, ip net.IP) error {
	m.clearedBackends[idx] = ip
	return nil
}

func (m *mockBPFManager) UpdateConfig(vip net.IP, vport uint16, backendCount uint32, srcMAC net.HardwareAddr) error {
	m.configUpdates++
	return nil
}

func (m *mockBPFManager) AttachVethXDP(vethName string, vethIdx int) error {
	m.attachedVeths[vethName] = vethIdx
	return nil
}

func (m *mockBPFManager) DetachVethXDP(vethName string) error {
	m.detachedVeths[vethName] = true
	return nil
}

func TestPool_Sync(t *testing.T) {
	mock := newMockBPFManager()
	vip := net.ParseIP("10.0.0.100")
	mac, _ := net.ParseMAC("02:42:0a:00:00:01")
	pool := NewPool(mock, vip, 8080, mac)

	backends := []Backend{
		{
			ID:       "container1",
			Name:     "app1",
			IP:       net.ParseIP("10.0.0.2"),
			MAC:      mac,
			Port:     80,
			VethName: "veth1",
			VethIdx:  10,
		},
		{
			ID:       "container2",
			Name:     "app2",
			IP:       net.ParseIP("10.0.0.3"),
			MAC:      mac,
			Port:     80,
			VethName: "veth2",
			VethIdx:  11,
		},
	}

	// Initial Sync
	err := pool.Sync(backends)
	assert.NoError(t, err)
	assert.Len(t, mock.updatedBackends, 2)
	assert.Equal(t, mock.attachedVeths["veth1"], 10)
	assert.Equal(t, mock.attachedVeths["veth2"], 11)
	assert.Equal(t, 1, mock.configUpdates)
	assert.Len(t, pool.GetBackends(), 2)

	// Update Sync: Remove container2, add container3
	backends2 := []Backend{
		backends[0], // Keep app1
		{
			ID:       "container3",
			Name:     "app3",
			IP:       net.ParseIP("10.0.0.4"),
			MAC:      mac,
			Port:     80,
			VethName: "veth3",
			VethIdx:  12,
		},
	}

	err = pool.Sync(backends2)
	assert.NoError(t, err)

	// Should have cleared backend index 1 (the old container2)
	assert.Len(t, mock.clearedBackends, 1)
	assert.Contains(t, mock.clearedBackends, uint32(1))
	// Should have detached veth2
	assert.True(t, mock.detachedVeths["veth2"])
	// Should have attached veth3
	assert.Equal(t, mock.attachedVeths["veth3"], 12)
	assert.Equal(t, 2, mock.configUpdates)
}
