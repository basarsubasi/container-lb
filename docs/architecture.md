# Architecture — Layers & Components

Companion to the [README](../README.md). Two views of the system: the layer stack and component layout, and a packet's journey through it.

---

## 1. The Big Picture — Layers & Components

```
                        ┌──────────────────────────────────────────────┐
                        │                   CLIENTS                    │
                        │           ping 10.200.200.200 (VIP)          │
                        └──────────────────────┬───────────────────────┘
                                               │  frames destined for the VIP
═══════════════════════════════════════════════╪══════════════════════════════
 HOST KERNEL                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                                                                             │
│  ① XDP DATA PLANE — eBPF (C). Runs in the driver exactly once per frame,   │
│     BEFORE the kernel network stack sees the packet.                        │
│                                                                             │
│    ┌──────────────────────────────┐      ┌───────────────────────────────┐  │
│    │  xdp_lb_ingress   (DNAT)     │      │  xdp_veth_snat   (SNAT)       │  │
│    │  attached: the ingress iface │      │  attached: every backend's    │  │
│    │  (host eth0, or the client-  │      │  host-side veth (catches      │  │
│    │  facing veth in e2e/tests)   │      │  traffic leaving a container) │  │
│    │                              │      │                               │  │
│    │  · filter: IP + ICMP + VIP   │      │  · src IP in backend_ips_map? │  │
│    │  · pick backend: rnd % N     │      │  · rewrite src IP → VIP       │  │
│    │  · rewrite dst IP + dst MAC  │      │  · recompute IP checksum      │  │
│    │  · recompute IP checksum     │      │  · return XDP_PASS            │  │
│    │  · count packets/bytes       │      │                               │  │
│    │  · return XDP_PASS           │      │                               │  │
│    └──────────────┬───────────────┘      └───────────────┬───────────────┘  │
│                   │                                     │                  │
│                   │ read / write                       │ read            │
│                   ▼                                     ▼                  │
│    ┌─────────────────────────────────────────────────────────────────────┐ │
│    │  BPF MAPS — the only shared state. Both XDP programs AND the Go    │ │
│    │  control plane read/write here.                                    │ │
│    │                                                                    │ │
│    │  config_map        VIP, backend_count, source MAC                  │ │
│    │  backend_map       backend index → IP + MAC                        │ │
│    │  backend_ips_map   backend IP → index  (SNAT reverse lookup)       │ │
│    │  counters_map      per-CPU packets/bytes per backend               │ │
│    └────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  ② KERNEL NETSTACK + DOCKER BRIDGE — plain L2/L3, untouched by XDP:        │
│     the bridge L2-forwards DNAT'd frames between veth ports.               │
└─────────────────────────────────────────────────────────────────────────────┘
              ▲                                      ▲
              │ bpf() syscalls:                      │ netlink + netns:
              │ load programs, update maps,          │ find each container's
              │ attach / detach XDP programs         │ host-side veth
══════════════╪══════════════════════════════════════╪══════════════════════════
 USER SPACE   │                                      │
┌─────────────┼──────────────────────────────────────┼───────────────────────────┐
│             │                                      │                           │
│  ② CONTROL PLANE (Go)                              │                           │
│  ┌──────────────────┐   ┌──────────────────────┐   │                           │
│  │ DockerWatcher    │──▶│ Pool (reconciler)    │   │                           │
│  │ · label filter   │   │ · diff old vs new    │──▶│                           │
│  │ · IP/MAC/PID     │   │ · update BPF maps    │   │                           │
│  │ · veth discovery │   │ · attach SNAT veths  │   │                           │
│  └──────────────────┘   └──────────┬───────────┘   │                           │
│                                    │                │                           │
│                             maps CRUD via          │ netns inspection          │
│                             bpf.Manager            │ (per backend container)   │
│                                    │                │                           │
│  ┌──────────────────┐              │                │                           │
│  │ stats.Collector  │◀─────────────┘                │                           │
│  │ · reads counters │  ReadStats                    │                           │
│  │ · names from Pool│                               │                           │
│  └──────────────────┘                               │                           │
└─────────────────────────────────────────────────────────────────────────────────┘
              ▲                                        │ docker.sock (all containers)
══════════════╪════════════════════════════════════════╪════════════════════════════
 DOCKER       │                                        │
┌─────────────┴────────────────────────────────────────▼───────────────────────────┐
│  backend-1 (172.18.0.2)              backend-2 (172.18.0.3)            ...       │
│  eth0 ◄── veth pair ──► (host if)    eth0 ◄── veth pair ──► (host if)            │
└───────────────────────────────────────────────────────────────────────────────────┘
```

**The core idea:** the data plane never talks to Docker and the control plane never touches a packet. They meet in exactly one place — the **BPF maps**.

---

## 4. A Packet's Journey

Client `172.18.0.4` pings the VIP `10.200.200.200`; the pool holds two backends (`b1` 172.18.0.2, `b2` 172.18.0.3).

```
  CLIENT (172.18.0.4)          HOST: XDP + DOCKER BRIDGE           BACKEND (172.18.0.2)
        │                                │                                  │
        │ ① client sends                │                                  │
        │    dst IP   = 10.200.200.200  │                                  │
        │    dst MAC  = gateway MAC     │                                  │
        │──────────────────────────────▶│                                  │
        │                                │                                  │
        │                                │ ② xdp_lb_ingress (client-facing │
        │                                │    veth / eth0)                 │
        │                                │    · ICMP + dst=VIP? → match    │
        │                                │    · pick idx = prandom() % 2   │
        │                                │    · dst IP  : VIP → 172.18.0.2 │
        │                                │    · dst MAC : → backend MAC    │
        │                                │    · recompute IP checksum      │
        │                                │    · counters_map[idx]++        │
        │                                │    · return XDP_PASS            │
        │                                │                                  │
        │                                │ ③ docker bridge L2-forwards on  │
        │                                │    dst MAC → backend's veth     │
        │                                │────────────────────────────────▶│
        │                                │                                  │ ④ backend replies
        │                                │                                  │    src IP = 172.18.0.2
        │                                │◀────────────────────────────────│    dst IP = 172.18.0.4
        │                                │                                  │    dst MAC = client
        │                                │                                  │
        │                                │ ⑤ xdp_veth_snat (backend's      │
        │                                │    host-side veth, ingress dir)  │
        │                                │    · src IP in backend_ips_map?  │
        │                                │      → yes, index found          │
        │                                │    · src IP : 172.18.0.2 → VIP   │
        │                                │    · recompute IP checksum       │
        │                                │    · return XDP_PASS             │
        │                                │                                  │
        │ ⑥ reply arrives "from the VIP" │                                  │
        │    src IP = 10.200.200.200    │                                  │
        │◀──────────────────────────────│                                  │
        │                                │                                  │
```

Key details behind the diagram:

- **ICMP checksum is never touched** — it covers header + payload only, and only IP fields are rewritten. The **IPv4 checksum is fully recomputed** (RFC 1071) after each rewrite (`update_ip_csum` in `xdp_lb.c`).
- **Stateless NAT.** No flow table exists. The reply finds its way back purely because the SNAT program matches the packet's *source IP* against `backend_ips_map`; the reply's destination (the client) is never rewritten.
- **The VIP exists on no interface.** It is only translation state inside `config_map` — which is also why no ARP entry for the VIP is ever needed.
- **Inside the backend you only see the translated view**: request `dst` = the backend's own IP (post-DNAT), reply `src` = its own IP (pre-SNAT). The VIP is invisible in there — see the README's “Observing ping inside the backend containers”.
- **The random backend pick** (`prandom() % N`) means a later reply can be answered by a *different* container than the one that got the request — which is fine for ICMP echo, since only the source IP is rewritten back to the VIP.