#!/usr/bin/env python3
"""Probe the layers between this host and the internet, once per run.

The probes follow a packet's path so that one failure names the layer that broke:

  gw_icmp       ICMP to the default gateway: the WiFi link and the router.
  resolver_dns  UDP DNS to the host's own primary resolver (the router today): the
                resolver every service uses and its upstream forwarder.
  wan_dns       DNS over TLS to 1.1.1.1:853: DNS that reached a real authority over the
                WAN. This is the only honest way to test that path here, because the
                router intercepts every UDP/53 query whatever its destination address
                (measured 10 November 2026: a query to TEST-NET 192.0.2.1 is answered),
                so a plain query to 1.1.1.1 tests the router, not the WAN. The TLS
                handshake with certificate and name verification also proves the peer is
                really Cloudflare and not a local proxy.
  wan_icmp      ICMP to 1.1.1.1: the WAN with no DNS and no router resolver involved.
  tcp443        TCP connect to 1.1.1.1:443: the WAN for TCP, which ICMP filtering
                cannot fake.

Both DNS probes ask for a random name in the zone this repo manages, so no answer can
come from a cache: only a resolver that reached the authority proves the whole chain.
Any reply counts, including the NXDOMAIN (rcode 3) that the authority returns for a
random name.

`ok` is true when the two layers everything on this host depends on answered: the
host's resolver and WAN TCP. The ICMP probes and `wan_dns` localise a failure but do
not define it, because a firewall may drop ICMP or DNS-over-TLS legitimately.

Output is one NDJSON line per run, prefixed [OK] or [BAD] for the severity parser in
platform/otel/helmrelease-daemonset.yaml. A layer that does not answer inside its
timeout is null, never 0. The process exits 0 even when layers fail: [BAD] is the
signal, and a failed unit would only add systemd noise to the incident it records.
"""

import json
import secrets
import socket
import ssl
import struct
import subprocess
import time
from datetime import datetime, timezone

GATEWAY = "192.168.1.1"
PUBLIC_RESOLVER = "1.1.1.1"
PUBLIC_TCP_TARGET = ("1.1.1.1", 443)
DOT_SERVER_NAME = "one.one.one.one"
DOT_PORT = 853
RESOLV_CONF = "/etc/resolv.conf"
# A zone this repo's dynamic DNS manages, so the authority answers NXDOMAIN for any
# label below it. Keep it a zone someone else's resolver cannot invent an answer for.
QUERY_ZONE = "swhurl.com"
DNS_TIMEOUT = 2.0
DOT_TIMEOUT = 3.0
TCP_TIMEOUT = 2.0
ICMP_TIMEOUT = 1  # seconds, passed to ping -W


def primary_resolver(path=RESOLV_CONF):
    """The first IPv4 nameserver in path, or None. IPv6 resolvers are skipped: the
    DNS probes here are IPv4, and this host's IPv6 is known to be half-configured."""
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                fields = line.split()
                if len(fields) < 2 or fields[0] != "nameserver":
                    continue
                try:
                    socket.inet_aton(fields[1])
                except OSError:
                    continue
                return fields[1]
    except OSError:
        return None
    return None


def build_query(query_id):
    name = f"probe-{secrets.token_hex(6)}.{QUERY_ZONE}"
    packet = struct.pack(">HHHHHH", query_id, 0x0100, 1, 0, 0, 0)
    for label in name.split("."):
        packet += bytes([len(label)]) + label.encode()
    return packet + b"\x00" + struct.pack(">HH", 1, 1)


def parse_reply(reply, query_id):
    """The rcode of reply, or None when it is not the answer to query_id."""
    if len(reply) < 12 or struct.unpack(">H", reply[0:2])[0] != query_id:
        return None
    return struct.unpack(">H", reply[2:4])[0] & 0xF


def read_exact(stream, length):
    buffered = b""
    while len(buffered) < length:
        chunk = stream.recv(length - len(buffered))
        if not chunk:
            raise OSError("connection closed")
        buffered += chunk
    return buffered


def dns_probe(server):
    """Return (milliseconds, rcode) for a random A query to server, or (None, None)."""
    query_id = secrets.randbelow(1 << 16)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(DNS_TIMEOUT)
    started = time.monotonic()
    try:
        sock.sendto(build_query(query_id), (server, 53))
        while True:
            reply, _ = sock.recvfrom(2048)
            rcode = parse_reply(reply, query_id)
            if rcode is not None:
                return round((time.monotonic() - started) * 1000, 1), rcode
    except OSError:
        return None, None
    finally:
        sock.close()


def dot_probe():
    """Return (milliseconds, rcode) for DNS over TLS to PUBLIC_RESOLVER, or (None, None)."""
    query_id = secrets.randbelow(1 << 16)
    query = build_query(query_id)
    context = ssl.create_default_context()
    started = time.monotonic()
    try:
        with socket.create_connection((PUBLIC_RESOLVER, DOT_PORT), timeout=DOT_TIMEOUT) as raw:
            with context.wrap_socket(raw, server_hostname=DOT_SERVER_NAME) as tls:
                tls.settimeout(DOT_TIMEOUT)
                tls.sendall(struct.pack(">H", len(query)) + query)
                length = struct.unpack(">H", read_exact(tls, 2))[0]
                rcode = parse_reply(read_exact(tls, length), query_id)
                if rcode is None:
                    return None, None
                return round((time.monotonic() - started) * 1000, 1), rcode
    except (OSError, ssl.SSLError):
        return None, None


def icmp_probe(address):
    """Return milliseconds if one echo request is answered, or None."""
    started = time.monotonic()
    try:
        result = subprocess.run(
            ["ping", "-n", "-q", "-c", "1", "-W", str(ICMP_TIMEOUT), address],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=ICMP_TIMEOUT + 3,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    return round((time.monotonic() - started) * 1000, 1) if result.returncode == 0 else None


def tcp_probe(target):
    """Return milliseconds until the TCP handshake completes, or None."""
    started = time.monotonic()
    try:
        with socket.create_connection(target, timeout=TCP_TIMEOUT):
            return round((time.monotonic() - started) * 1000, 1)
    except OSError:
        return None


def main():
    resolver = primary_resolver()
    probe = {
        "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "gw_icmp": icmp_probe(GATEWAY),
        "resolver": resolver,
        "resolver_dns": None,
        "resolver_rcode": None,
        "wan_dns": None,
        "wan_dns_rcode": None,
        "wan_icmp": icmp_probe(PUBLIC_RESOLVER),
        "tcp443": tcp_probe(PUBLIC_TCP_TARGET),
    }
    if resolver is not None:
        probe["resolver_dns"], probe["resolver_rcode"] = dns_probe(resolver)
    probe["wan_dns"], probe["wan_dns_rcode"] = dot_probe()
    probe["ok"] = probe["resolver_dns"] is not None and probe["tcp443"] is not None
    level = "OK" if probe["ok"] else "BAD"
    print(f"[{level}] {json.dumps(probe, separators=(',', ':'))}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
