# Traffic accounting

[简体中文](zh-CN/traffic-accounting.md) · English

Hostpin separates instantaneous network rate from billing-period traffic. The
Agent reports cumulative receive/transmit counters and the server derives
period totals. This keeps billing semantics centralized and consistent across
Agent platforms.

## Counter model

- `net_rx_bps` and `net_tx_bps` are current rates for charts.
- `net_rx_bytes` and `net_tx_bytes` are cumulative counters observed by the
  Agent for the configured interface set.
- `monthly_rx_bytes` and `monthly_tx_bytes` are server-derived totals for the
  active reset period. The field names are retained even when a reset day
  produces a billing period that is not a calendar month.

The first sample establishes a baseline and contributes zero bytes. Later
samples contribute only a non-negative delta while the boot ID is unchanged.
A boot ID change or a decreasing interface counter establishes a new baseline;
it never creates negative or wrapped traffic.

Hostpin restores each node's latest durable counters and period totals when the
server starts. A normal server restart therefore continues accounting from the
last persisted sample. If both the host and server restart between durable
samples, traffic that occurred after the last persisted point and before the
host reboot cannot be reconstructed from monotonic counters.

## Reset periods

Reset boundaries are calculated in UTC. A configured reset day from 1 through
31 is clamped to the last real day of shorter months. For example, day 31 uses
February 28 in 2027 and February 29 in 2028.

The first sample after a reset boundary starts the new baseline at zero. A
single counter delta spanning the boundary cannot be split reliably, so
Hostpin deliberately does not assign the whole interval to the new period.
The maximum unassigned interval is normally one live reporting interval.

## Quota modes

The configured traffic limit is compared with one of four values:

| Mode | Accounted usage |
| --- | --- |
| `sum` | received + transmitted |
| `max` | larger of received or transmitted |
| `up` | transmitted only |
| `down` | received only |

The node detail view always shows both direction totals, the selected mode,
reset day, quota percentage, and warning/critical state. Public API values are
bytes as non-negative integers. SQL storage uses signed `BIGINT`; impossible
values above its maximum are saturated rather than wrapped negative.

The node editor and advanced Agent enrollment prompt accept monthly quotas in
GiB, including decimal values; `0` means unlimited. Hostpin converts the input
to whole bytes when submitted, so existing data needs no migration and native
API, database, and Komari compatibility fields remain byte-based.

The default home page shows each node's monthly GiB usage and quota utilization
using its configured traffic mode. The same compact meter appears in grid and
table views; it warns at 85% used and becomes critical at 100%.

## Traffic correction

An administrator can enter the download and upload totals that should be shown
for the active period under **Admin → Nodes → Edit → Traffic correction**. The
editor accepts B, KiB, MiB, GiB, or TiB. Hostpin derives a signed adjustment
from the raw Agent totals; it does not rewrite the Agent counters.

- A correction is bound to the active UTC reset period and expires
  automatically at the next boundary.
- Later Agent traffic continues accumulating on top of the corrected total.
- Public node state, live updates, native history responses, and traffic alerts
  use corrected values.
- Durable metric rows retain raw totals, so clearing a correction immediately
  restores the original accounting.
- The correction, period, and audit entry are retained by SQLite backups and
  PostgreSQL migration.

A node needs at least one report in the active traffic period before it can be
corrected. This prevents an offline node's previous-period total from becoming
the new baseline after a reset boundary.

## Verification

The authoritative regression tests are:

- `internal/core/traffic_test.go` for baseline, reboot, rollback, reset-day,
  restart restoration, short-month, leap-year, and overflow behavior;
- `internal/store/sqlstore/*integration_test.go` for SQLite/PostgreSQL history,
  rollups, and database conversion;
- `web/src/traffic.test.ts` for quota-mode and utilization presentation;
- `tests/e2e/browser_smoke.py` for authenticated reports, API totals, node
  editing, and rendered quota details;
- `tests/load/main.go` for non-zero accounting under 100/1,000-Agent load.
