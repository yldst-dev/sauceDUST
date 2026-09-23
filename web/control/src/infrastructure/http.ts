export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

let onUnauthorized = () => {}

export function setUnauthorized(fn: () => void) {
  onUnauthorized = fn
}

export function unauthorized() {
  onUnauthorized()
}

export async function request<T>(path: string, opt?: { method?: string; json?: unknown; body?: BodyInit }): Promise<T> {
  const headers: Record<string, string> = {}
  let body: BodyInit | undefined
  if (opt?.json !== undefined) {
    headers["content-type"] = "application/json"
    body = JSON.stringify(opt.json)
  } else {
    body = opt?.body
  }
  const res = await fetch(path, { method: opt?.method ?? "GET", headers, body })
  const text = await res.text()
  let parsed: { error?: string } | null = null
  if (text) {
    try {
      parsed = JSON.parse(text) as { error?: string }
    } catch {
      parsed = { error: text }
    }
  }
  if (res.status === 401 && !path.startsWith("/v1/login") && path !== "/v1/session") {
    onUnauthorized()
    throw new ApiError("로그인이 필요합니다", 401)
  }
  if (!res.ok) throw new ApiError(parsed?.error || `요청에 실패했습니다 (${res.status})`, res.status)
  return parsed as T
}
