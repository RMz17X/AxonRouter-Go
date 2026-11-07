// Proxy line parser for the batch import (extended past the upstream
// parseProxyLine, which knew only the first two forms).
// Supported batch-import line formats:
//   - protocol://user:pass@host:port (any URL with a scheme)
//   - host:port:user:pass
//   - user:pass@host:port (scheme-less, http assumed)
//   - host:port (no auth, http assumed)
// Bracketed IPv6 hosts ([2001:db8::1]:8080) work in every scheme-less form.
export interface ParsedProxy {
  proxyUrl: string
  name: string
}

function buildUrl(scheme: string, host: string, port: string, user?: string, pass?: string): string {
  const auth =
    user !== undefined ? `${encodeURIComponent(user)}:${encodeURIComponent(pass ?? '')}@` : ''
  const url = `${scheme}://${auth}${host}:${port}`
  // Round-trip through URL so the stored form is canonical.
  return new URL(url).toString()
}

function hostLabel(host: string, port: string): string {
  return `${host.replace(/^\[|\]$/g, '')}:${port}`
}

function assertPort(port: string): void {
  if (!/^\d+$/.test(port)) throw new Error(`Invalid port "${port}"`)
  const n = Number(port)
  if (n < 1 || n > 65535) throw new Error(`Port out of range "${port}"`)
}

export function parseProxyLine(line: string): ParsedProxy | null {
  const trimmed = line.trim()
  if (!trimmed) return null

  if (trimmed.includes('://')) {
    const parsed = new URL(trimmed)
    const label = parsed.port ? `${parsed.hostname}:${parsed.port}` : parsed.hostname
    return {
      proxyUrl: parsed.toString(),
      name: `Imported ${label}`,
    }
  }

  // user:pass@host:port — the credentials end at the LAST '@'; the host part
  // never contains one.
  if (trimmed.includes('@')) {
    const at = trimmed.lastIndexOf('@')
    const creds = trimmed.slice(0, at)
    const hostPort = trimmed.slice(at + 1)
    const credSep = creds.indexOf(':')
    if (credSep <= 0) throw new Error('Invalid user:pass@host:port format')
    const user = creds.slice(0, credSep)
    const pass = creds.slice(credSep + 1)
    const { host, port } = splitHostPort(hostPort)
    return {
      proxyUrl: buildUrl('http', host, port, user, pass),
      name: `Imported ${hostLabel(host, port)}`,
    }
  }

  // [ipv6]:port and [ipv6]:port:user:pass
  if (trimmed.startsWith('[')) {
    const close = trimmed.indexOf(']')
    if (close === -1) throw new Error('Invalid bracketed IPv6 host')
    const host = trimmed.slice(0, close + 1)
    const rest = trimmed.slice(close + 1)
    if (!rest.startsWith(':')) throw new Error('Missing port')
    const parts = rest.slice(1).split(':')
    if (parts.length === 1) {
      assertPort(parts[0])
      return {
        proxyUrl: buildUrl('http', host, parts[0]),
        name: `Imported ${hostLabel(host, parts[0])}`,
      }
    }
    if (parts.length === 3) {
      const [port, user, pass] = parts
      assertPort(port)
      if (!user || !pass) throw new Error('Invalid [ipv6]:port:user:pass format')
      return {
        proxyUrl: buildUrl('http', host, port, user, pass),
        name: `Imported ${hostLabel(host, port)}`,
      }
    }
    throw new Error('Unsupported format')
  }

  const parts = trimmed.split(':')
  if (parts.length === 2) {
    const [host, port] = parts
    if (!host) throw new Error('Invalid host:port format')
    assertPort(port)
    return {
      proxyUrl: buildUrl('http', host, port),
      name: `Imported ${host}:${port}`,
    }
  }
  if (parts.length === 4) {
    const [host, port, username, password] = parts
    if (!host || !username || !password) {
      throw new Error('Invalid host:port:user:pass format')
    }
    assertPort(port)
    return {
      proxyUrl: buildUrl('http', host, port, username, password),
      name: `Imported ${host}:${port}`,
    }
  }

  throw new Error('Unsupported format')
}

function splitHostPort(hostPort: string): { host: string; port: string } {
  if (hostPort.startsWith('[')) {
    const close = hostPort.indexOf(']')
    if (close === -1 || hostPort[close + 1] !== ':') {
      throw new Error('Invalid bracketed IPv6 host')
    }
    const host = hostPort.slice(0, close + 1)
    const port = hostPort.slice(close + 2)
    assertPort(port)
    return { host, port }
  }
  const sep = hostPort.lastIndexOf(':')
  if (sep <= 0) throw new Error('Missing port')
  const host = hostPort.slice(0, sep)
  const port = hostPort.slice(sep + 1)
  assertPort(port)
  return { host, port }
}
