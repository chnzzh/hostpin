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
2. Click **Add target** and select a monitored server.
3. Choose the IP stack (**Dual stack (IPv4 + IPv6)**, **IPv4 only**, or
   **IPv6 only**) and protocol (**ICMP + TCP**, **ICMP Ping**, or
   **TCP Connect**).
4. Review or customize the **IPv4 address**, **IPv6 address**, **TCP port**
   (default `443`), samples per round, interval, timeout, and public matrix
   visibility.

Each monitored server can hold up to **four** independent latency targets at
the same time, keyed by `(server, IP stack, protocol)`:

- `IPv4 · ICMP`
- `IPv4 · TCP`
- `IPv6 · ICMP`
- `IPv6 · TCP`

When opening the drawer or switching servers, Hostpin pre-fills the IPv4 and
IPv6 address fields from the server's reported addresses and defaults the IP
stack selector to **Dual stack** if both addresses are present, **IPv6 only**
if only an IPv6 address is present, or **IPv4 only** otherwise. You can still
select **Dual stack** and **ICMP + TCP** to generate all four targets in one
click:

- For **ICMP**, addresses are normalized without a port (brackets around an
  IPv6 literal such as `[2001:db8::1]` are stripped automatically).
- For **TCP**, the configured **TCP port** is appended automatically
  (`host:port` for IPv4/hostnames and `[ipv6]:port` for IPv6 literals), unless
  you enter an explicit `host:port` or `[ipv6]:port`.
- When creating multiple combinations in one submission, any combination that
  already exists for that server is skipped automatically so remaining
  combinations are still created cleanly.
- Each target can later be edited or deleted individually (with its stack and
  protocol badge shown alongside the target address in the target table).

All enabled targets are distributed to all connected measurement points,
including probe-only nodes and regular nodes with latency measurement enabled.
Three samples per round is the default. Hostpin stores the average of successful
samples and the percentage lost; a fully failed round is recorded as 100% loss.

The target address does not have to equal the server's detected public IP, but
v1 distributes the same address to every Probe Node. A site containing only
measurement points that can reach the same LAN may use an internal address. A
deployment mixing public and home Probe Nodes should use a public hostname or
IP reachable from all points; unreachable points will record a failure.
Separate per-Probe address overrides are intentionally deferred beyond v1.

## Public matrix filtering and private data

On `/latency`, visitors can filter the matrix by **IP stack** (`All`, `IPv4`,
`IPv6`) and **Protocol** (`All`, `ICMP`, `TCP`):

- Both the matrix rows (desktop table and mobile probe-grouped cards) and the
  top summary metrics (**Healthy paths** and **Current mean RTT**) reflect the
  active filters.
- If a filter combination matches no configured targets, the matrix shows a
  clear empty state with a **Reset filters** action.
- Every path is identified and queried by its `task_id`, so IPv4/IPv6 and
  ICMP/TCP histories remain strictly separated. When the selected `Probe →
  Server` pair has multiple configured paths, quick sibling tabs in the route
  history header switch between its `IPv4`/`IPv6` and `ICMP`/`TCP` curves in
  one click.

Private measurement-node visibility and the node's hidden flag are the same setting.
A route appears on `/latency` only when all three conditions hold:

- the measurement node is not hidden;
- the monitored server is not hidden;
- the latency target is marked public.

Unauthenticated responses omit target addresses and replace detailed network
errors with a generic failure. Administrators can see hidden measurement
points, private targets, exact addresses, and errors.

## Platform notes

- ICMP uses the local `ping` (or `ping6`) program and pins the address family
  to the target's configured IP stack (`-4` / `-6` on Linux/Windows/FreeBSD,
  with `ping6` support on macOS, FreeBSD, and BusyBox builds without `ping -6`).
  Install `ping` / `ping6` on minimal OpenWrt images and ensure the Agent
  service account is allowed to run it.
- TCP targets dial `tcp4` or `tcp6` according to the configured stack and use
  `host:port` format; wrap IPv6 literals in brackets, such as `[2001:db8::1]:443`.
  When using a dual-stack hostname, pinning `IPv4` or `IPv6` ensures the Agent
  measures that specific address family rather than falling back silently.
- TCP RTT is connection-establishment time and is often the best choice when
  ICMP is filtered.
- Probe-only mode does not initialize Hostpin's CPU, disk, GPU, or network
  metric collector, keeping its memory and CPU footprint below a full Agent.
- If WebSocket is blocked, the Agent automatically submits heartbeats and
  results through outbound HTTP POST with exponential reconnect backoff.
