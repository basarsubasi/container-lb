#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/icmp.h>
#include <linux/in.h>

#include "include/bpf_helpers.h"
#include "include/bpf_endian.h"
#include "types.h"

/* =========================================================================
 * BPF Maps (shared between both XDP programs)
 * =========================================================================
 *
 * config_map       – single global config slot (VIP, backend count, docker0 MAC)
 * backend_map      – array of backends (IP + MAC), indexed 0..N-1
 * backend_ips_map  – hash of backend IPv4 → index; O(1) reverse lookup for SNAT
 * counters_map     – per-CPU packet/byte counters per backend index (lock-free)
 */
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __type(key, __u32);
    __type(value, struct lb_config);
    __uint(max_entries, 1);
} config_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __type(key, __u32);
    __type(value, struct backend_info);
    __uint(max_entries, MAX_BACKENDS);
} backend_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, __u32);   /* backend IPv4 in network byte order */
    __type(value, __u32); /* backend index into backend_map     */
    __uint(max_entries, MAX_BACKENDS);
} backend_ips_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __type(key, __u32);
    __type(value, struct stats_counter);
    __uint(max_entries, MAX_BACKENDS);
} counters_map SEC(".maps");

/* =========================================================================
 * IP checksum recalculation (RFC 1071)
 * =========================================================================
 * Zero the field, sum every 16-bit word of the 20-byte fixed IPv4 header
 * with carry folding.  Must be called after modifying any IP header field.
 */
static __always_inline void update_ip_csum(struct iphdr *iph)
{
    iph->check = 0;
    __u32 csum = 0;
    __u16 *p = (__u16 *)iph;

    #pragma unroll
    for (int i = 0; i < (int)(sizeof(struct iphdr) >> 1); i++)
        csum += *p++;

    csum  = (csum >> 16) + (csum & 0xffff);
    csum += (csum >> 16);
    iph->check = (__u16)(~csum);
}

/* =========================================================================
 * PROGRAM 1 – XDP ingress on docker0
 * =========================================================================
 * Intercepts ICMP packets destined to the VIP.
 * Performs DNAT: picks a backend randomly, rewrites dst MAC and dst IP.
 * Returns XDP_TX so the docker0 bridge delivers the frame to the correct
 * container veth.
 */
SEC("xdp")
int xdp_lb_ingress(struct xdp_md *ctx)
{
    void *data     = (void *)(long)ctx->data;
    void *data_end = (void *)(long)ctx->data_end;

    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return XDP_PASS;
    if (eth->h_proto != bpf_htons(ETH_P_IP))
        return XDP_PASS;

    struct iphdr *ip = (void *)(eth + 1);
    if ((void *)(ip + 1) > data_end)
        return XDP_PASS;
    if (ip->protocol != IPPROTO_ICMP)
        return XDP_PASS;

    struct icmphdr *icmp = (void *)((char *)ip + (ip->ihl * 4));
    if ((void *)(icmp + 1) > data_end)
        return XDP_PASS;

    __u32 zero = 0;
    struct lb_config *cfg = bpf_map_lookup_elem(&config_map, &zero);
    if (!cfg || cfg->backend_count == 0)
        return XDP_PASS;

    /* Filter by VIP (0.0.0.0 = accept all dst IPs) */
    if (cfg->vip != 0 && ip->daddr != cfg->vip)
        return XDP_PASS;

    /* Stateless per-packet backend selection */
    __u32 idx = bpf_get_prandom_u32() % cfg->backend_count;

    struct backend_info *backend = bpf_map_lookup_elem(&backend_map, &idx);
    if (!backend || backend->ipv4 == 0)
        return XDP_PASS;

    /* DNAT: rewrite dst MAC and dst IP */
    #pragma unroll
    for (int i = 0; i < ETH_ALEN; i++) {
        eth->h_dest[i]   = backend->mac[i];
        eth->h_source[i] = cfg->src_mac[i];
    }
    ip->daddr = backend->ipv4;
    update_ip_csum(ip);
    /* ICMP checksum covers only ICMP header+payload, not IP fields. */

    /* Per-CPU stats */
    struct stats_counter *stats = bpf_map_lookup_elem(&counters_map, &idx);
    if (stats) {
        stats->rx_packets++;
        stats->rx_bytes += ((__u64)(data_end - data));
    }

    return XDP_TX;
}

/* =========================================================================
 * PROGRAM 2 – XDP ingress on each container's host-side veth
 * =========================================================================
 * From the host's perspective, traffic leaving the container arrives here
 * as "ingress" on the host-side veth peer.
 *
 * Performs SNAT: rewrites src IP back to the VIP so the client receives the
 * ICMP echo reply from the expected address.
 *
 * Returns XDP_PASS: the kernel continues processing the frame.  Because the
 * container used docker0 as its default gateway, the Ethernet dst MAC is
 * docker0's own MAC.  The bridge hands the packet to the Linux IP stack,
 * which routes it out eth0 to the client — no bpf_redirect needed.
 *
 * ICMP note: ICMP checksum does not cover the IP source address, so it does
 * NOT need to be updated after rewriting ip->saddr.
 */
SEC("xdp")
int xdp_veth_snat(struct xdp_md *ctx)
{
    void *data     = (void *)(long)ctx->data;
    void *data_end = (void *)(long)ctx->data_end;

    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return XDP_PASS;
    if (eth->h_proto != bpf_htons(ETH_P_IP))
        return XDP_PASS;

    struct iphdr *ip = (void *)(eth + 1);
    if ((void *)(ip + 1) > data_end)
        return XDP_PASS;
    if (ip->protocol != IPPROTO_ICMP)
        return XDP_PASS;

    struct icmphdr *icmp = (void *)((char *)ip + (ip->ihl * 4));
    if ((void *)(icmp + 1) > data_end)
        return XDP_PASS;

    /* Reverse lookup: is this src IP one of our registered backends? */
    __u32 *idx = bpf_map_lookup_elem(&backend_ips_map, &ip->saddr);
    if (!idx)
        return XDP_PASS;  /* not a backend packet – leave it alone */

    __u32 zero = 0;
    struct lb_config *cfg = bpf_map_lookup_elem(&config_map, &zero);
    if (!cfg || cfg->vip == 0)
        return XDP_PASS;

    /* SNAT: rewrite src IP to VIP */
    ip->saddr = cfg->vip;
    update_ip_csum(ip);

    /* XDP_PASS: kernel routes the reply to the client via docker0 → eth0 */
    return XDP_PASS;
}

char _license[] SEC("license") = "GPL";
