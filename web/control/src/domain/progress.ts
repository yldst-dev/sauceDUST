import type { Stats } from "./types"

export function coverage(stats: Stats): number {
  const hi = Number(stats.high_watermark) || 0
  const before = Number(stats.backfill_before) || 0
  if (hi <= 0) return 0
  let span = hi - before
  if (span < 0) span = 0
  if (span > hi) span = hi
  return (span / hi) * 100
}

export function remainingRanges(stats: Stats): number {
  const known = (stats.ranges_done || 0) + (stats.ranges_active || 0) + (stats.ranges_empty || 0) + (stats.ranges_failed || 0)
  const left = (stats.ranges_total || 0) - known
  return left > 0 ? left : 0
}

export function phase(stats: Stats): "진행 중" | "완료" | "대기" {
  if ((stats.ranges_active || 0) > 0 || (stats.saved_per_sec || 0) > 0) return "진행 중"
  if (coverage(stats) >= 99.9 && !(stats.ranges_failed || 0) && !(stats.retry_queue || 0)) return "완료"
  return "대기"
}
