# Komari theme compatibility

[简体中文](zh-CN/theme-compatibility.md) · English

Hostpin accepts Komari theme ZIPs containing a root `komari-theme.json` and
`dist/index.html`. It supports localized metadata, previews, SPA fallback,
`managed`, `raw`, and safe site-relative `redirect` configuration. Managed
defaults are merged with saved values and exposed as `theme_settings` from
`GET /api/public`.

Public theme-facing REST, WebSocket, and RPC2 routes are implemented under the
same paths used by current Komari themes. Hostpin intentionally does not
implement the Komari Agent protocol, plugin protocol, management API, remote
terminal, or command-result RPC methods.

Compatibility is pinned to the public theme contract reviewed on 2026-08-23.
The official Komari Web bundle uses the reserved manifest short name `default`;
Hostpin accepts that archive unchanged and stores it under `komari-default` so
the built-in Hostpin interface remains available. Upstream changes should be
adopted only after the official Komari Web, Carbon, and Pulse fixtures pass the
compatibility suite.
