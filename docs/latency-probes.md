# Latency Probe Nodes

[简体中文](zh-CN/latency-probes.md) · English

Hostpin can measure every monitored server from independent network
perspectives. A Probe Node can run in a public region, branch office, home, NAS,
or OpenWrt router. It needs outbound access to the Hostpin panel but does not
need a public IP.

## How the connection works

```text
private router / regional host
        │
        └── outbound WSS or HTTPS ──> Hostpin server
              heartbeat
              structured ICMP/TCP targets
              average RTT + packet loss
```

Hostpin never dials the Probe Node. This makes the design safe for NAT, CGNAT,
dynamic residential addresses, and networks where all unsolicited inbound
traffic is blocked. The protocol has no shell, arbitrary command, binary URL,
or reverse-tunnel field.

## Carrier latency versus Probe Nodes

Hostpin provides two outbound-only latency views with different directions:

| Feature | Measurement direction | Display |
| --- | --- | --- |
| Carrier latency | each regular monitor Agent → Telecom/Unicom/Mobile targets | that server's node detail |
| Latency Probe Node | home router or public probe → monitored servers | public latency matrix and route history |

Carrier latency runs every 120 seconds by default. Each route takes four
samples and stores the average successful TCP connect time plus packet loss.
The defaults follow
[CF-Server-Monitor's target settings](https://github.com/huilang-me/CF-Server-Monitor/blob/main/src/utils/settings.js)
for Telecom, Unicom, and Mobile. Use **Admin → Service probes → Carrier
latency** to change a target, switch to ICMP, or pause one route. Only fixed
ICMP/TCP structures are distributed; the public node API redacts target
addresses.

Both modes initiate connections from the Agent, so neither requires a public
IP. Install a router as a Probe Node to see that router's path to each server.
Open a regular server's node detail to see that server's path to the three
carrier targets.

## Use a monitored node as a measurement point

A node running the regular monitoring Agent can also measure latency. Open
**Admin → Nodes**, edit the node, and enable **Use as a latency measurement
point**. No Agent reinstall or credential change is required. The node keeps
collecting CPU, memory, disk, and traffic metrics while also receiving the
latency matrix's fixed ICMP/TCP tasks.

Disabling the option stops future latency measurements without deleting host
monitoring, node identity, or existing latency history. **Disable latency** on
the latency-node page has the same effect. Use `--probe-node` for routers and
other devices that only need latency measurement, because probe-only mode does
not initialize the full metric collectors and has a lower footprint.

## Install

Unix-like systems, including OpenWrt and common NAS distributions:

```sh
curl -fsSL https://monitor.example.com/install.sh | sh -s -- --probe-node
```

The Agent asks for the PIN, display name, group, region, tags, and public or
private visibility. Private visibility is stored as the node's hidden flag.
Advanced setup can set remarks, country, the parallel probe limit, and signed
automatic updates. The generated installation identity and per-node
credential are stored locally; the PIN is used only for enrollment.

Windows:

```powershell
Invoke-WebRequest -UseBasicParsing 'https://monitor.example.com/install.ps1' -OutFile .\hostpin-install.ps1
.\hostpin-install.ps1 -ProbeNode
```

## Configure targets

In **Admin → Latency nodes**:

1. Confirm the measurement node is online and choose public or private
   visibility.
2. Add a monitored server as a target.
3. Choose ICMP or TCP, target address, samples per round, interval, and timeout.
4. Choose whether the route may appear on the public matrix.

One latency target is attached to each monitored server and distributed to all
connected measurement points, including probe-only nodes and regular nodes
with latency measurement enabled. Three samples per round is the default.
Hostpin stores the average of successful samples and the percentage lost; a
fully failed round is recorded as 100% loss.

The target address does not have to equal the server's detected public IP, but
v1 distributes the same address to every Probe Node. A site containing only
measurement points that can reach the same LAN may use an internal address. A
deployment mixing public and home Probe Nodes should use a public hostname or
IP reachable from all points; unreachable points will record a failure.
Separate per-Probe address overrides are intentionally deferred beyond v1.

## Public and private data

Private measurement-node visibility and the node's hidden flag are the same setting.
A route appears on `/latency` only when all three conditions hold:

- the measurement node is not hidden;
- the monitored server is not hidden;
- the latency target is marked public.

Unauthenticated responses omit target addresses and replace detailed network
errors with a generic failure. Administrators can see hidden measurement
points, private targets, exact addresses, and errors.

## Platform notes

- ICMP uses the local `ping` program. Install it on minimal OpenWrt images and
  ensure the Agent service account is allowed to run it.
- TCP targets use `host:port`; use brackets for IPv6, such as `[2001:db8::1]:443`.
- TCP RTT is connection-establishment time and is often the best choice when
  ICMP is filtered.
- Probe-only mode does not initialize Hostpin's CPU, disk, GPU, or network
  metric collector, keeping its memory and CPU footprint below a full Agent.
- If WebSocket is blocked, the Agent automatically submits heartbeats and
  results through outbound HTTP POST with exponential reconnect backoff.
