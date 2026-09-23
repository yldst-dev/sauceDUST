import { useEffect, useState, type ReactNode } from "react"
import { applyUpdate, checkUpdate, logout } from "../../application/console"
import type { PageId, Stats } from "../../domain/types"
import { Brand } from "../components/Brand"
import { rate } from "../format"

const pages: { id: PageId; label: string; icon: ReactNode }[] = [
  { id: "dash", label: "대시보드", icon: <HomeIcon /> },
  { id: "jobs", label: "작업", icon: <ListIcon /> },
  { id: "nodes", label: "노드", icon: <GridIcon /> },
  { id: "data", label: "데이터", icon: <DiskIcon /> },
  { id: "settings", label: "설정", icon: <GearIcon /> },
]

export function Shell({ page, stats, clock, flash, onRefresh, onLogout, children }: {
  page: PageId
  stats: Stats | null
  clock: string
  flash: string
  onRefresh: () => void
  onLogout: () => void
  children: ReactNode
}) {
  const [version, setVersion] = useState("")
  const [latest, setLatest] = useState("")
  const [updating, setUpdating] = useState(false)
  useEffect(() => {
    checkUpdate().then((info) => {
      setVersion(info.current)
      if (info.available) setLatest(info.latest)
    }).catch(() => {})
  }, [])

  function updateNow() {
    setUpdating(true)
    applyUpdate().then(() => {
      let n = 0
      const timer = setInterval(() => {
        n += 1
        fetch("/health").then((res) => {
          if (res.ok && n > 1) { clearInterval(timer); location.reload() }
        }).catch(() => {})
        if (n > 40) { clearInterval(timer); setUpdating(false) }
      }, 500)
    }).catch(() => setUpdating(false))
  }

  return (
    <div className="app">
      <header className="top">
        <Brand />
        <div className="top-meta">
          <span className="meta-nodes"><i className={stats && stats.online_nodes > 0 ? "dot on" : "dot"} /> 연결 노드 <b>{stats ? `${stats.online_nodes}/${stats.total_nodes}` : "—"}</b></span>
          <i className="sep" />
          <span className="meta-rate">처리 속도 <b>{stats ? `${rate(stats.saved_per_sec)}/s` : "—"}</b></span>
          <i className="sep" />
          <span className="meta-ver">
            {latest
              ? <button className="primary ver-btn" type="button" disabled={updating} onClick={updateNow}>{updating ? "받는 중" : "업데이트"}</button>
              : <b>{version || "—"}</b>}
          </span>
          <i className="sep" />
          <span className="clock">{clock}</span>
          <button className="ico" type="button" aria-label="새로고침" onClick={onRefresh}>
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6"><path d="M13 8a5 5 0 1 1-1.2-3.2" /><path d="M13 2.5V5h-2.5" /></svg>
          </button>
        </div>
      </header>
      <div className="shell">
        <nav>
          <div className="nav-links">
            {pages.map((item) => (
              <a key={item.id} href={`#${item.id}`} aria-current={page === item.id ? "page" : undefined}>
                {item.icon}{item.label}
              </a>
            ))}
          </div>
          <button className="ghost nav-logout" type="button" onClick={() => logout().then(onLogout).catch(onLogout)}>로그아웃</button>
        </nav>
        <main>
          {flash ? <p className="flash">{flash}</p> : null}
          {children}
        </main>
      </div>
    </div>
  )
}

function HomeIcon() {
  return <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M2 7.5 8 2l6 5.5V14H10v-4H6v4H2z" /></svg>
}
function ListIcon() {
  return <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M3 3h10v2H3zM3 7h10v2H3zM3 11h7v2H3z" /></svg>
}
function GridIcon() {
  return <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><rect x="2" y="2" width="5" height="5" /><rect x="9" y="2" width="5" height="5" /><rect x="2" y="9" width="5" height="5" /><rect x="9" y="9" width="5" height="5" /></svg>
}
function DiskIcon() {
  return <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><ellipse cx="8" cy="4" rx="5" ry="2" /><path d="M3 4v4c0 1.1 2.2 2 5 2s5-.9 5-2V4M3 8v4c0 1.1 2.2 2 5 2s5-.9 5-2V8" /></svg>
}
function GearIcon() {
  return <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="8" cy="8" r="2" /><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M3.2 12.8l1.4-1.4M11.4 4.6l1.4-1.4" /></svg>
}
