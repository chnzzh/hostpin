# Third-party notices

Hostpin includes the following third-party assets in the server binary.

## Apache ECharts

The embedded dashboard uses Apache ECharts 6.1.0 and zrender 6.1.0.

Apache ECharts
Copyright 2017-2026 The Apache Software Foundation

ECharts is licensed under Apache License 2.0 and includes BSD-licensed d3.js
subcomponents. The required texts are stored under `third_party/echarts/`.

## Vue runtime

The embedded dashboard includes Vue 3.5.41, Vue Router 5.2.0, Pinia 4.0.3,
and tslib 2.3.0. Their license texts are stored under
`third_party/frontend/`.

## flag-icons

The country flag SVG files under `internal/httpapi/assets/flags` are from
[flag-icons](https://github.com/lipis/flag-icons), copyright (c) 2013
Panayiotis Lipiridis, and are used under the MIT License. A copy of that
license is stored at `internal/httpapi/assets/flags/LICENSE.flag-icons`.

## CF-Server-Monitor OS icons

The operating-system images under `internal/httpapi/assets/logos` are from
[CF-Server-Monitor](https://github.com/huilang-me/CF-Server-Monitor) at commit
`eb346cf4e924cfa42a3a40924e87a4c6a42c66ea`. The upstream project declares
the package under the MIT License. Distribution names and logos may also be
trademarks of their respective owners.

## Go modules

The server and Agent use the Go modules pinned in `go.mod` and `go.sum`.
Release builds attach `hostpin-third-party-licenses.tar.gz`, containing the
license, notice, copyright, and patent texts discovered for every pinned Go
module, plus the Go toolchain license. `go version -m <binary>` also reports
the modules linked into an individual Go binary.
