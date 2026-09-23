import type { ImageView, JobStatus, Jobs, ModelRow, NodeRow, SearchHit, Settings, Stats, UpdateInfo, VerifyItem } from "../domain/types"
import { request, unauthorized } from "../infrastructure/http"

export function login(user: string, password: string) {
  return request<{ user: string }>("/v1/login", { method: "POST", json: { user, password } })
}

export function logout() {
  return request<{ ok: boolean }>("/v1/logout", { method: "POST", json: {} })
}

export function session() {
  return request<{ authenticated?: boolean; open?: boolean; setup?: boolean }>("/v1/session")
}

export function setupAccount(user: string, password: string, confirm: string) {
  return request<{ user: string }>("/v1/setup", { method: "POST", json: { user, password, confirm } })
}

export function loadStats() {
  return request<Stats>("/v1/stats")
}

export function loadNodes() {
  return request<NodeRow[]>("/v1/nodes")
}

export function loadJobs() {
  return request<Jobs>("/v1/jobs")
}

export function resetRange(id?: number, retries?: boolean) {
  return request<{ count: number; retries: number }>("/v1/jobs/reset", { method: "POST", json: { id: id ?? 0, retries: !!retries } })
}

export function fillGaps() {
  return request<{ count: number }>("/v1/jobs/fill", { method: "POST", json: {} })
}

export function reclaimNodes() {
  return request<{ count: number }>("/v1/nodes/reclaim", { method: "POST", json: {} })
}

export function loadModels() {
  return request<ModelRow[]>("/v1/models")
}

export function syncModels() {
  return request<{ count: number }>("/v1/models/sync", { method: "POST", json: {} })
}

export function addModel(body: { id: string; kind: string; vector_size: number; input_size: number; backend: string; collection: string }) {
  return request<{ ok: boolean }>("/v1/models", { method: "POST", json: body })
}

export async function lookupPost(site: string, id: string): Promise<ImageView | null> {
  const path = `/v1/images/source/${encodeURIComponent(site)}/${encodeURIComponent(id)}`
  const res = await fetch(path)
  const img = (await res.json()) as ImageView & { error?: string }
  if (res.status === 401) {
    unauthorized()
    throw new Error("로그인이 필요합니다")
  }
  if (res.status === 404 || !img.indexed) return null
  if (!res.ok) throw new Error(img.error || "조회에 실패했습니다")
  return img
}

export function searchImage(file: File) {
  const body = new FormData()
  body.append("file", file)
  return request<{ hits: SearchHit[] }>("/v1/search", { method: "POST", body })
}

export function loadSettings() {
  return request<Settings>("/v1/settings")
}

export function saveSettings(body: Record<string, unknown>) {
  return request<{ restarting: boolean }>("/v1/settings", { method: "POST", json: body })
}

export function saveTelegram(token: string) {
  return request("/v1/settings/telegram", { method: "POST", json: { token } })
}

export function clearTelegram() {
  return request("/v1/settings/telegram", { method: "POST", json: { clear: true } })
}

export function changePassword(current: string, next: string) {
  return request("/v1/password", { method: "POST", json: { current, next } })
}

export function loadJob() {
  return request<JobStatus>("/v1/maintenance")
}

export function startRebuild() {
  return request("/v1/maintenance/rebuild", { method: "POST", json: { batch: 512 } })
}

export function startReembed(dry: boolean) {
  return request("/v1/maintenance/reembed", { method: "POST", json: dry ? { dry_run: true } : {} })
}

export function verify() {
  return request<VerifyItem[]>("/v1/verify")
}

export function checkUpdate() {
  return request<UpdateInfo>("/v1/update")
}

export function applyUpdate() {
  return request<{ restarting: boolean; version: string }>("/v1/update", { method: "POST", json: {} })
}
