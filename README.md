# XDP-Based Container Load Balancer

A high-performance layer-4 load balancer implemented with an **XDP (eBPF)** kernel data plane in C and a **Go** userspace control plane with automatic Docker container discovery.

---

## Architecture & Layer Layout

```
container-lb/
├── Makefile                          # Build targets (generate, build, test, run)
├── go.mod                            # Go module definition
├── go.sum                            # Dependency checksums
│
├── kernelspace/                      # === Layer 1: Kernel Space / Data Plane (C & eBPF) ===
│   ├── include/                      # BPF helper headers & macros
│   │   ├── bpf_helpers.h             # Standard BPF section macros & helper wrappers
│   │   └── bpf_endian.h              # Network byte order macros (bpf_htons, bpf_ntohl)
│   ├── types.h                       # Shared C structs (backend_info, stats_counter, lb_config)
│   └── xdp_lb.c                      # Stateless XDP load balancer:
│                                     #   - Parse Ethernet / IPv4 / TCP / UDP
│                                     #   - Compute stateless hash % num_backends
│                                     #   - Lookup backend in backend_map
│                                     #   - Rewrite MAC / IP / Port & update checksums
│                                     #   - Forward via XDP_TX / XDP_REDIRECT
│
├── userspace/                        # === Layer 2: User Space / Control Plane (Go) ===
│   ├── cmd/                          # Application Entry Point
│   │   └── main.go                   # CLI flags, signal handling, orchestration
│   │
│   ├── bpf/                          # BPF Loader & Map Management
│   │   ├── gen.go                    # //go:generate bpf2go compiling kernelspace/xdp_lb.c
│   │   ├── loader.go                 # Loads BPF object, sets rlimit, attaches XDP to eth0
│   │   └── maps.go                   # BackendMap and CountersMap CRUD helpers
│   │
│   ├── discovery/                    # Docker Discovery Engine
│   │   ├── docker.go                 # Docker client (extracts container IP & MAC)
│   │   └── pool.go                   # Reconciles active backends into BPF Backend map
│   │
│   └── stats/                        # Observability & Metrics
│       └── collector.go              # Reads BPF counters & renders live terminal dashboard
│
└── scripts/                          # === Tooling & Testing Scripts ===
    ├── setup_dependencies.sh         # Dependency installation script for Ubuntu
    ├── deploy_backends.sh            # Helper to launch sample Docker backend containers
    └── test_traffic.sh               # Simulates traffic to verify round-robin / load balance
```

---

## How It Works

1. **Discovery (Go)**:
   - Queries Docker API for healthy containers labeled `lb.backend=true`.
   - Extracts their container IP, MAC address, and port.
   - Populates the BPF `backend_map` and updates the active backend count.

2. **Data Plane (XDP/C)**:
   - Intercepts incoming packets at the driver/NIC layer before the kernel network stack.
   - Filters packets matching target VIP / Port.
   - Computes stateless hash of 5-tuple (`src_ip`, `dst_ip`, `src_port`, `dst_port`, `proto`) modulo active backend count.
   - Rewrites destination MAC, IP, and Port.
   - Incrementally recalculates IPv4 and TCP/UDP checksums.
   - Increments per-CPU packet/byte counters and forwards the packet via `XDP_TX`.

3. **Observability (Go)**:
   - Periodically reads the BPF `counters_map` and renders live packet/byte throughput in the terminal.

---

## Getting Started

### 1. Install Dependencies
```bash
chmod +x scripts/*.sh
./scripts/setup_dependencies.sh
```

### 2. Generate eBPF Bindings & Build
```bash
make generate
make build
```

### 3. Deploy Test Docker Backends
```bash
make backends-up
```

### 4. Run the Load Balancer
```bash
# Attach to target interface (e.g. eth0 or docker0)
sudo ./bin/container-lb -iface eth0 -port 8080
```

### 5. Send Test Traffic
In another terminal:
```bash
make test-traffic
```

### 6. Clean Up
```bash
make backends-down
make clean
```
