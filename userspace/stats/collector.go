package stats

import (
	"context"
	"fmt"
	"strings"
	"time"

	"container-lb/userspace/bpf"
	"container-lb/userspace/discovery"
)

// Collector periodically fetches and displays statistics from the BPF data plane.
type Collector struct {
	bpfManager *bpf.Manager
	pool       *discovery.Pool
	interval   time.Duration
}

// NewCollector creates a new statistics collector.
func NewCollector(bpfManager *bpf.Manager, pool *discovery.Pool, interval time.Duration) *Collector {
	return &Collector{
		bpfManager: bpfManager,
		pool:       pool,
		interval:   interval,
	}
}

// Start begins periodic statistics logging.
func (c *Collector) Start(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.render()
		}
	}
}

func (c *Collector) render() {
	backends := c.pool.GetBackends()

	var sb strings.Builder
	sb.WriteString("\n" + strings.Repeat("=", 80) + "\n")
	sb.WriteString(fmt.Sprintf(" [%s] XDP LOAD BALANCER - STATS & ACTIVE BACKENDS (%d Total)\n",
		time.Now().Format("15:04:05"), len(backends)))
	sb.WriteString(strings.Repeat("-", 80) + "\n")
	sb.WriteString(fmt.Sprintf(" %-5s %-16s %-21s %-18s %-12s %-12s\n",
		"IDX", "CONTAINER", "ENDPOINT", "MAC ADDRESS", "PACKETS", "BYTES"))
	sb.WriteString(strings.Repeat("-", 80) + "\n")

	if len(backends) == 0 {
		sb.WriteString(" (No healthy backends discovered. Waiting for matching Docker containers...)\n")
	} else {
		for i, b := range backends {
			pkts, bytes, err := c.bpfManager.ReadStats(uint32(i))
			if err != nil {
				pkts, bytes = 0, 0
			}

			endpoint := fmt.Sprintf("%s:%d", b.IP.String(), b.Port)
			sb.WriteString(fmt.Sprintf(" %-5d %-16s %-21s %-18s %-12d %-12s\n",
				i, truncate(b.Name, 16), endpoint, b.MAC.String(), pkts, formatBytes(bytes)))
		}
	}
	sb.WriteString(strings.Repeat("=", 80) + "\n")

	fmt.Print(sb.String())
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
