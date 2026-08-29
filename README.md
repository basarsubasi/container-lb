# XDP-Based Container Load Balancer

A high-performance load balancer with an **XDP (eBPF) kernel data plane** written in C and a **Go userspace control plane** with automatic Docker container discovery.

Packets destined for a **Virtual IP (VIP)** are intercepted at the NIC driver level — before the kernel network stack — and distributed across a dynamic pool of Docker containers. Replies are transparently rewritten so clients only ever see the VIP.

> 📐 See [docs/architecture.md](docs/architecture.md) for detailed layer/component diagrams, the component ownership map, the startup lifecycle, and a step-by-step packet walk.

---

## How It Works

```
                         ┌──────────────────────────── HOST ────────────────────────────┐
                         │                                                              │
 client ──────► eth0/veth│ ──► [1] xdp_lb_ingress (DNAT) ──► docker bridge ──► [2] veth │──► backend-1
  10.0.0.5               │     dst IP: VIP → backend IP     (L2 forward)                │
  ping 10.200.200.200    │     dst MAC: → backend MAC                                   │
                         │                                                              │
 client ◄────── eth0/veth│ ◄── [4] xdp_lb_ingress... ◄── bridge ◄── [3] xdp_veth_snat ◄─┘ backend-1
  from 10.200.200.200    │     (passes through, no match)         (SNAT: src IP → VIP)
```

### 1. Request path — DNAT (`xdp_lb_ingress`)

Attached to the **ingress interface** (e.g. the host NIC `eth0`, or a client-facing veth in tests).

1. Parses Ethernet / IPv4 / ICMP headers (verifiers-safe bounds checks).
2. Drops everything that isn't ICMP, or whose destination doesn't match the configured VIP (`0.0.0.0` = match all).
3. Picks a backend **statelessly**: `bpf_get_prandom_u32() % backend_count` — per-packet random distribution.
4. Rewrites the destination MAC (backend's MAC) and destination IP (backend's IP), sets the source MAC from config, and **recomputes the IPv4 header checksum** (RFC 1071). ICMP checksums are unaffected by IP rewrites.
5. Increments per-CPU packet/byte counters for the chosen backend.
6. Returns `XDP_PASS` — the frame continues into the kernel, where the bridge L2-forwards it to the backend's veth based on the rewritten destination MAC.

### 2. Reply path — SNAT (`xdp_veth_snat`)

A **second XDP program is attached to every backend's host-side veth**. From the host's perspective, packets *leaving* a container arrive as *ingress* on its host-side veth — exactly where the reply can be caught.

1. Reverse-looks-up the source IP in `backend_ips_map` (O(1) hash). Non-backend traffic passes untouched.
2. Rewrites the source IP to the **VIP** and recomputes the checksum, then `XDP_PASS`.
3. The container's reply (dst = client IP, same L2 subnet) is delivered back to the client, which sees it as coming **from the VIP**.

> Both translations are stateless — no conntrack, no flow table. The ICMP echo `id`/`seq` in the payload keeps request/reply pairs matched on the client side.

### 3. Discovery & reconciliation (Go control plane)

A polling loop (every `-poll`, default 2s) keeps the kernel maps in sync with reality:

1. **Discover** — queries the Docker API for running, healthy containers matching the label filter (`-label`, default `lb.backend=true`), extracting IP + MAC from the bridge network settings.
2. **Resolve veths** — opens each container's network namespace (`/proc/<pid>/ns/net`), finds its `eth0`, and reads its `iflink` — which *is* the ifindex of the host-side veth peer.
3. **Reconcile** (`discovery.Pool.Sync`) — diffs against the previous state:
   - new backends → write `backend_map` + `backend_ips_map`, attach the SNAT program to their veth,
   - removed backends → clear map entries, detach their SNAT program,
   - finally updates `config_map` (VIP + backend count + source MAC) so the data plane only ever sees a consistent snapshot.

### 4. Observability

A stats collector (every `-stats-interval`, default 3s) sums the per-CPU counters and renders a live dashboard:

```
================================================================================
 [21:28:48] XDP LOAD BALANCER - STATS & ACTIVE BACKENDS (2 Total)
--------------------------------------------------------------------------------
 IDX   CONTAINER        ENDPOINT              MAC ADDRESS        PACKETS      BYTES
--------------------------------------------------------------------------------
 0     img-b2           172.18.0.3:8080       a2:b0:c0:99:1e:fd  2            196 B
 1     img-b1           172.18.0.2:8080       ba:83:2e:bf:55:fe  8            784 B
================================================================================
```

---

## BPF Maps

| Map | Type | Key → Value | Purpose |
|---|---|---|---|
| `config_map` | array (1) | `u32 → lb_config` | Global config: VIP, port, active backend count, source MAC |
| `backend_map` | array (64) | index → `backend_info` | Forward table: backend index → IP + MAC |
| `backend_ips_map` | hash (64) | backend IP → index | Reverse table for SNAT on the reply path |
| `counters_map` | per-CPU array (64) | index → `stats_counter` | Lock-free packet/byte accounting |


---

## Repository Layout

```
container-lb/
├── Makefile                    # generate, build, run, test-e2e, test-integration, backends-up/down
├── kernelspace/                # Data plane (C / eBPF)
│   ├── xdp_lb.c                #   xdp_lb_ingress (DNAT) + xdp_veth_snat (SNAT)
│   ├── types.h                 #   Shared structs mirrored into Go via bpf2go/BTF
│   └── vmlinux.h               #   Kernel types (generated from BTF: make vmlinux)
├── userspace/                  # Control plane (Go)
│   ├── cmd/main.go             #   CLI flags, wiring, signal handling
│   ├── bpf/                    #   bpf2go-generated loader + map CRUD
│   │   ├── lb_bpfel.o          #   Compiled BPF objects (embedded via go:embed)
│   │   ├── loader.go           #   Attach/detach XDP (driver mode, generic fallback)
│   │   └── maps.go             #   Config/backend/counters map updates
│   ├── discovery/              #   Docker watcher, veth resolver, reconciliation pool
│   └── stats/collector.go      #   Live stats dashboard
├── tests/
│   ├── e2e/                    # Full data-plane test: real containers, real XDP, real ping
│   ├── integration/            # Docker discovery test (needs root)
│   └── testutil/               # testcontainers helpers
└── scripts/                    # deploy_backends.sh, test_traffic.sh, setup_dependencies.sh
```

---

## Usage

```bash
sudo ./bin/container-lb -iface eth0 -vip 10.200.200.200 -port 8080 -label lb.backend=true
```

| Flag | Default | Description |
|---|---|---|
| `-iface` | `eth0` | Interface to attach the DNAT program to |
| `-vip` | `0.0.0.0` | Virtual IP to load balance (`0.0.0.0` = all destinations) |
| `-port` | `8080` | Virtual port (informational; stored in config) |
| `-label` | `lb.backend=true` | Docker label selector for backends |
| `-poll` | `2s` | Discovery polling interval |
| `-stats-interval` | `3s` | Stats dashboard interval |

Per-container port override: label the container with `lb.port=80`.

### Build from source

```bash
make generate   # bpf2go: compile kernelspace/xdp_lb.c, generate Go bindings (needs clang + bpftool)
make build      # go build -> bin/container-lb
```

### Test with Docker backends

```bash
make backends-up                      # 3 http-echo containers labeled lb.backend=true
sudo ./bin/container-lb -iface docker0
make backends-down                    # cleanup
```

---

## Testing

| Target | What it does | Needs root |
|---|---|---|
| `go test ./...` | Unit tests (`discovery`) | no |
| `make test-integration` | Spins up a real container and verifies Docker discovery + veth resolution | **yes** (netns inspection) |
| `make test-e2e` | Full data-plane test: creates a Docker network with 2 nginx backends + a client, attaches the real XDP programs, and asserts **4/4 ping replies from the VIP** | **yes** (XDP attach + netns) |

Both root-required targets run under a Makefile wrapper (`SUDO_GO`) that re-exports `PATH` and the Go caches after `sudo` strips them, and redirects `GOCACHE` so root never leaves root-owned files in your user cache.

The e2e test attaches the DNAT program to the **client's host-side veth** rather than the Docker bridge — modeling how you'd attach to a real host NIC in production, and avoiding the `EEXIST` conflict of generic XDP on a bridge and its ports simultaneously.

---

## Running as a Container

Multi-stage `Dockerfile`: static binary (CGO disabled, BPF objects embedded via `go:embed`) on `gcr.io/distroless/static-debian13` (~18 MB, no shell, no package manager).

```bash
docker build -t container-lb .

docker run -d --name container-lb \
  --privileged --network host --pid host \
  -v /sys:/sys:ro -v /sys/fs/bpf:/sys/fs/bpf \
  -v /var/run/docker.sock:/var/run/docker.sock \
  container-lb -iface eth0 -vip 10.200.200.200 -label lb.backend=true
```

| Mount / flag | Why |
|---|---|
| `--privileged` | `bpf()` syscalls, XDP attach, netns inspection |
| `--network host` | Attach XDP to *host* interfaces |
| `--pid host` | Discover container netns via host PIDs |
| `/var/run/docker.sock` | Docker API discovery |
| `/sys` + `/sys/fs/bpf` | Kernel interfaces for eBPF |

The default (root) distroless variant is used deliberately — do **not** use `:nonroot`; XDP attach requires real root.

### Observing ping inside the backend containers

The VIP is only visible *outside* — inside a backend you see the post-DNAT / pre-SNAT view:

```bash
docker exec -d <backend> sh -c "tcpdump -i eth0 -n icmp > /tmp/icmp.txt 2>&1"
docker exec <backend> cat /tmp/icmp.txt
```
```
172.18.0.4 > 172.18.0.2: ICMP echo request   ← dst = backend's real IP (DNAT already applied)
172.18.0.2 > 172.18.0.4: ICMP echo reply     ← src = real IP (SNAT happens later, on the host veth)
```

Or capture on the host side with the backend's host veth (`docker exec <backend> cat /sys/class/net/eth0/iflink` gives its ifindex).

---

## Design Notes & Limitations

- **ICMP only, for now.** Both XDP programs currently filter on `IPPROTO_ICMP` to keep the data plane verifiable and the test story end-to-end. TCP/UDP extension points are already in place (`backend_info.port`, per-flow hashing would replace the random pick for stateful protocols).
- **No health checks from the data plane.** A backend that stops responding stays in the pool until Docker reports it unhealthy/stopped; then the control plane removes it within one poll interval.
- **Driver-mode XDP with generic fallback.** Native/driver XDP is attempted first (veth supports it on modern kernels); the loader silently falls back to generic (skb) mode.
- **No connection persistence.** Stateful L4 balancing (same flow → same backend) requires a flow table; the current per-packet random choice is only suitable for stateless protocols.
