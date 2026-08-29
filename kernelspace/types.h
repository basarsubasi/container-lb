#ifndef __LB_TYPES_H__
#define __LB_TYPES_H__

#include <linux/types.h>

#define MAX_BACKENDS 64
#define ETH_ALEN 6

/* Backend definition stored in BPF backend_map */
struct backend_info {
    __u32 ipv4;             /* Backend IPv4 in network byte order */
    __u16 port;             /* Backend Port in network byte order */
    __u16 pad;              /* 2-byte alignment padding */
    __u8  mac[ETH_ALEN];    /* Backend MAC address */
    __u8  pad2[2];          /* 2-byte alignment padding */
    __u32 weight;           /* Weight or state (1 = active) */
};

/* Global load balancer configuration */
struct lb_config {
    __u32 vip;              /* Target Virtual IP in network byte order */
    __u16 vport;            /* Target Virtual Port in network byte order */
    __u16 pad;              /* Padding */
    __u32 backend_count;    /* Number of active healthy backends */
    __u8  src_mac[ETH_ALEN];/* Source MAC to rewrite on forwarding */
    __u8  pad2[2];          /* Padding */
};

/* Per-backend or global statistics counters */
struct stats_counter {
    __u64 rx_packets;
    __u64 rx_bytes;
};

#endif /* __LB_TYPES_H__ */
