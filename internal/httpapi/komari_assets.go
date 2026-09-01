package httpapi

import (
	"embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// komariAssets contains the country flags and CF-Server-Monitor OS logos used
// by compatible third-party themes. See the notices stored with those assets.
//
//go:embed assets/flags/*.svg assets/flags/LICENSE.flag-icons assets/logos/*
var komariAssets embed.FS

var komariLogoSources = map[string]string{
	"alibabacloud-color.svg": "os-alibaba.svg",
	"linux.svg":              "os-unknown.svg",
	"os-alibaba.svg":         "os-alibaba.svg",
	"os-alma.svg":            "os-alma.svg",
	"os-alpine.webp":         "os-alpine.webp",
	"os-arch.svg":            "os-arch.svg",
	"os-armbian.png":         "os-armbian.png",
	"os-armbian.svg":         "os-armbian.png",
	"os-astar.png":           "os-unknown.svg",
	"os-centos.svg":          "os-centos.svg",
	"os-debian.svg":          "os-debian.svg",
	"os-fedora.svg":          "os-fedora.svg",
	"os-fnos.ico":            "os-unknown.svg",
	"os-freebsd.svg":         "os-unknown.svg",
	"os-gentoo.svg":          "os-gentoo.svg",
	"os-huawei.svg":          "os-unknown.svg",
	"os-istore.png":          "os-istore.png",
	"os-kail.svg":            "os-kail.svg",
	"os-macos.svg":           "os-macos.svg",
	"os-manjaro-.svg":        "os-manjaro-.svg",
	"os-mint.svg":            "os-mint.svg",
	"os-nix.svg":             "os-nix.svg",
	"os-opencloud.svg":       "os-opencloud.svg",
	"os-opencloudos.png":     "os-opencloud.svg",
	"os-opensuse.svg":        "os-openSUSE.svg",
	"os-openwrt.svg":         "os-openwrt.svg",
	"os-oracle.svg":          "os-oracle.svg",
	"os-orange-pi.svg":       "os-unknown.svg",
	"os-proxmox.ico":         "os-proxmox.ico",
	"os-qnap.svg":            "os-unknown.svg",
	"os-redhat.svg":          "os-redhat.svg",
	"os-rocky.svg":           "os-rocky.svg",
	"os-synology.ico":        "os-synology.ico",
	"os-ubuntu.svg":          "os-ubuntu.svg",
	"os-unraid.svg":          "os-unknown.svg",
	"os-unknown.svg":         "os-unknown.svg",
	"os-windows.svg":         "os-windows.svg",
}

func normalizeCountryCode(value string) (string, bool) {
	code := strings.ToUpper(strings.TrimSpace(value))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return "", false
	}
	return code, true
}

func countryCodeFromFlagEmoji(value string) (string, bool) {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) != 2 || runes[0] < 0x1F1E6 || runes[0] > 0x1F1FF || runes[1] < 0x1F1E6 || runes[1] > 0x1F1FF {
		return "", false
	}
	return string([]byte{
		byte(runes[0]-0x1F1E6) + 'A',
		byte(runes[1]-0x1F1E6) + 'A',
	}), true
}

func komariRegion(region, countryCode string) string {
	if code, ok := normalizeCountryCode(countryCode); ok {
		return code
	}
	trimmed := strings.TrimSpace(region)
	if code, ok := countryCodeFromFlagEmoji(trimmed); ok {
		return code
	}
	if code, ok := normalizeCountryCode(trimmed); ok {
		return code
	}
	return trimmed
}

func komariFlagSVG(value string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".svg")
	code, ok := normalizeCountryCode(name)
	if !ok {
		return "", false
	}
	flag, err := komariAssets.ReadFile("assets/flags/" + strings.ToLower(code) + ".svg")
	if err != nil {
		return "", false
	}
	return string(flag), true
}

func komariLogoAsset(value string) ([]byte, string, bool) {
	source, ok := komariLogoSources[strings.ToLower(strings.TrimSpace(value))]
	if !ok {
		return nil, "", false
	}
	asset, err := komariAssets.ReadFile("assets/logos/" + source)
	if err != nil {
		return nil, "", false
	}
	contentType := http.DetectContentType(asset)
	if strings.HasSuffix(strings.ToLower(source), ".svg") {
		contentType = "image/svg+xml; charset=utf-8"
	}
	return asset, contentType, true
}

func serveKomariAsset(w http.ResponseWriter, r *http.Request, asset []byte, contentType string, ok bool) {
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// Compatibility assets can change between Hostpin releases while keeping the
	// stable paths expected by third-party themes. Keep edge/browser caches short
	// enough that a server upgrade is reflected promptly.
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=300, must-revalidate")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(asset)))
	_, _ = w.Write(asset)
}

func (a *API) handleKomariFlagAsset(w http.ResponseWriter, r *http.Request) {
	svg, ok := komariFlagSVG(chi.URLParam(r, "code"))
	serveKomariAsset(w, r, []byte(svg), "image/svg+xml; charset=utf-8", ok)
}

func (a *API) handleKomariLogoAsset(w http.ResponseWriter, r *http.Request) {
	asset, contentType, ok := komariLogoAsset(chi.URLParam(r, "name"))
	serveKomariAsset(w, r, asset, contentType, ok)
}
