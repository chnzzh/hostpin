interface APIErrorPayload {
  error?: { code: string; message: string }
}

export class APIError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'APIError'
    this.status = status
    this.code = code
  }
}

function cookie(name: string): string {
  const prefix = `${name}=`
  const value = document.cookie.split('; ').find((part) => part.startsWith(prefix))
  return value ? decodeURIComponent(value.slice(prefix.length)) : ''
}

async function checkedResponse(path: string, options: RequestInit = {}): Promise<Response> {
  const headers = new Headers(options.headers)
  if (options.body && !(options.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const method = (options.method ?? 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    const csrf = cookie('hostpin_csrf')
    if (csrf) headers.set('X-CSRF-Token', csrf)
  }
  const response = await fetch(path, { ...options, headers, credentials: 'same-origin' })
  if (!response.ok) {
    let payload: APIErrorPayload = {}
    try {
      payload = (await response.json()) as APIErrorPayload
    } catch {
      // Preserve the HTTP status if a proxy returned a non-JSON error page.
    }
    throw new APIError(
      response.status,
      payload.error?.code ?? 'http_error',
      payload.error?.message ?? `Request failed with HTTP ${response.status}`,
    )
  }
  return response
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await checkedResponse(path, options)
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export async function apiDownload(path: string, options: RequestInit = {}): Promise<{ blob: Blob; filename: string }> {
  const response = await checkedResponse(path, options)
  const disposition = response.headers.get('Content-Disposition') ?? ''
  const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1]
  const plain = disposition.match(/filename="?([^";]+)"?/i)?.[1]
  let filename = 'hostpin-backup.hostpin-backup'
  try {
    filename = encoded ? decodeURIComponent(encoded) : (plain ?? filename)
  } catch {
    // Keep the safe fallback when a proxy rewrites the header incorrectly.
  }
  return { blob: await response.blob(), filename }
}

export function unwrap<T>(value: { data: T }): T {
  return value.data
}

export function socketURL(path: string): string {
  const url = new URL(path, window.location.href)
  url.protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}
