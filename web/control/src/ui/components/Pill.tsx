export function Pill({ children, tone }: { children: string; tone?: "ok" | "wait" | "bad" }) {
  const cls = tone === "bad" ? "pill bad" : tone === "wait" ? "pill wait" : "pill"
  return <span className={cls}>{children}</span>
}

export function statusLabel(status: string) {
  if (status === "online") return { text: "정상", tone: "ok" as const }
  if (status === "offline") return { text: "끊김", tone: "bad" as const }
  return { text: status || "알 수 없음", tone: "wait" as const }
}
