package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"container-lb/userspace/bpf"
	"container-lb/userspace/discovery"
	"container-lb/userspace/stats"
)

func main() {
	ifaceName := flag.String("iface", "eth0", "Network interface to attach the XDP program to")
	vipStr := flag.String("vip", "0.0.0.0", "Virtual IP (VIP) to load balance (0.0.0.0 matches all incoming traffic)")
	vport := flag.Int("port", 8080, "Virtual port (VPORT) to load balance")
	label := flag.String("label", "lb.backend=true", "Docker container label selector for backend discovery")
	pollInterval := flag.Duration("poll", 2*time.Second, "Docker container discovery polling interval")
	statsInterval := flag.Duration("stats-interval", 3*time.Second, "Statistics reporting interval")
	flag.Parse()

	log.Printf("[*] Starting XDP Load Balancer on interface '%s'...", *ifaceName)

	vip := net.ParseIP(*vipStr)
	if vip == nil {
		log.Fatalf("[ERROR] Invalid VIP address: %s", *vipStr)
	}

	// Resolve local interface MAC address
	iface, err := net.InterfaceByName(*ifaceName)
	if err != nil {
		log.Fatalf("[ERROR] Could not find interface %s: %v", *ifaceName, err)
	}
	srcMAC := iface.HardwareAddr
	log.Printf("[*] Interface %s index=%d MAC=%s", *ifaceName, iface.Index, srcMAC)

	// 1. Initialize BPF Manager and attach XDP program
	bpfManager, err := bpf.NewManager(*ifaceName)
	if err != nil {
		log.Fatalf("[ERROR] Failed to initialize BPF manager: %v", err)
	}
	defer func() {
		log.Printf("[*] Detaching XDP program and cleaning up BPF maps...")
		if err := bpfManager.Close(); err != nil {
			log.Printf("[WARNING] Error closing BPF manager: %v", err)
		}
	}()
	log.Printf("[+] XDP program successfully loaded and attached to %s", *ifaceName)

	// 2. Initialize Docker Discovery Engine
	dockerWatcher, err := discovery.NewDockerWatcher(*label, uint16(*vport))
	if err != nil {
		log.Fatalf("[ERROR] Failed to connect to Docker daemon: %v", err)
	}
	defer dockerWatcher.Close()

	// 3. Initialize Backend Pool
	backendPool := discovery.NewPool(bpfManager, vip, uint16(*vport), srcMAC)

	// Root context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 4. Initial Backend Discovery
	initialBackends, err := dockerWatcher.DiscoverBackends(ctx)
	if err != nil {
		log.Printf("[WARNING] Initial backend discovery failed: %v", err)
	} else {
		log.Printf("[+] Discovered %d healthy backends on startup", len(initialBackends))
		if err := backendPool.Sync(initialBackends); err != nil {
			log.Printf("[ERROR] Failed to sync backends to BPF: %v", err)
		}
	}

	// 5. Start Background Discovery Loop
	go func() {
		ticker := time.NewTicker(*pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				backends, err := dockerWatcher.DiscoverBackends(ctx)
				if err != nil {
					log.Printf("[WARNING] Docker discovery poll error: %v", err)
					continue
				}
				if err := backendPool.Sync(backends); err != nil {
					log.Printf("[ERROR] Failed to sync backends: %v", err)
				}
			}
		}
	}()

	// 6. Start Stats Collector
	statsCollector := stats.NewCollector(bpfManager, backendPool, *statsInterval)
	go statsCollector.Start(ctx)

	log.Printf("[+] Load Balancer running! Target: %s:%d -> Docker containers (label: %s)", *vipStr, *vport, *label)
	log.Printf("[+] Press Ctrl+C to stop.")

	// Wait for OS termination signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println()
	log.Printf("[*] Received shutdown signal. Exiting gracefully...")
}
