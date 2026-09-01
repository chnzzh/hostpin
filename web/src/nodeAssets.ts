const systemLogos: Array<{ terms: string[]; asset: string }> = [
  { terms: ['alibaba', 'anolis'], asset: 'os-alibaba.svg' },
  { terms: ['alma'], asset: 'os-alma.svg' },
  { terms: ['alpine'], asset: 'os-alpine.webp' },
  { terms: ['armbian'], asset: 'os-armbian.png' },
  { terms: ['arch'], asset: 'os-arch.svg' },
  { terms: ['centos'], asset: 'os-centos.svg' },
  { terms: ['debian'], asset: 'os-debian.svg' },
  { terms: ['fedora'], asset: 'os-fedora.svg' },
  { terms: ['gentoo'], asset: 'os-gentoo.svg' },
  { terms: ['istore'], asset: 'os-istore.png' },
  { terms: ['kali'], asset: 'os-kail.svg' },
  { terms: ['macos', 'mac os', 'darwin'], asset: 'os-macos.svg' },
  { terms: ['manjaro'], asset: 'os-manjaro-.svg' },
  { terms: ['mint'], asset: 'os-mint.svg' },
  { terms: ['nixos', 'nix os'], asset: 'os-nix.svg' },
  { terms: ['opencloudos', 'opencloud'], asset: 'os-opencloud.svg' },
  { terms: ['opensuse', 'suse'], asset: 'os-opensuse.svg' },
  { terms: ['openwrt'], asset: 'os-openwrt.svg' },
  { terms: ['oracle linux', 'oracle'], asset: 'os-oracle.svg' },
  { terms: ['proxmox'], asset: 'os-proxmox.ico' },
  { terms: ['red hat', 'redhat', 'rhel'], asset: 'os-redhat.svg' },
  { terms: ['rocky'], asset: 'os-rocky.svg' },
  { terms: ['synology'], asset: 'os-synology.ico' },
  { terms: ['ubuntu'], asset: 'os-ubuntu.svg' },
  { terms: ['windows'], asset: 'os-windows.svg' },
  { terms: ['freebsd'], asset: 'os-freebsd.svg' },
  { terms: ['linux'], asset: 'linux.svg' },
]

export function countryFlagSource(value?: string): string {
  const code = value?.trim().toUpperCase() ?? ''
  return /^[A-Z]{2}$/.test(code) ? `/assets/flags/${code}.svg?v=flag-icons-v1` : ''
}

export function systemLogoSource(value?: string): string {
  const normalized = value?.trim().toLowerCase() ?? ''
  const match = systemLogos.find((item) => item.terms.some((term) => normalized.includes(term)))
  return `/assets/logo/${match?.asset ?? 'os-unknown.svg'}?v=cf-monitor-v1`
}
