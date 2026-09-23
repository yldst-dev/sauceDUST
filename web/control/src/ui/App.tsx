import { useEffect, useState } from "react"
import { loadNodes, loadStats, session } from "../application/console"
import type { NodeRow, PageId, Stats } from "../domain/types"
import { setUnauthorized } from "../infrastructure/http"
import { clockText } from "./format"
import { DashboardPage } from "./pages/DashboardPage"
import { DataPage } from "./pages/DataPage"
import { JobsPage } from "./pages/JobsPage"
import { LoginPage } from "./pages/LoginPage"
import { SetupPage } from "./pages/SetupPage"
import { NodesPage } from "./pages/NodesPage"
import { SettingsPage } from "./pages/SettingsPage"
import { Shell } from "./shell/Shell"

function readPage(): PageId {
  const id = (location.hash || "#dash").slice(1)
  if (id === "jobs" || id === "nodes" || id === "data" || id === "settings") return id
  return "dash"
}

export function App() {
  const [gate, setGate] = useState<"loading" | "setup" | "login" | "in">("loading")
  const [page, setPage] = useState<PageId>(readPage)
  const [stats, setStats] = useState<Stats | null>(null)
  const [nodes, setNodes] = useState<NodeRow[]>([])
  const [flash, setFlash] = useState("")
  const [clock, setClock] = useState(clockText())
  const [tick, setTick] = useState(0)

  useEffect(() => {
    setUnauthorized(() => setGate("login"))
    session().then((body) => setGate(body.setup ? "setup" : "in")).catch(() => setGate("login"))
  }, [])

  useEffect(() => {
    const onHash = () => setPage(readPage())
    window.addEventListener("hashchange", onHash)
    return () => window.removeEventListener("hashchange", onHash)
  }, [])

  useEffect(() => {
    const id = setInterval(() => setClock(clockText()), 1000)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    if (gate !== "in") return
    let stop = false
    const pull = () => {
      loadStats().then((value) => { if (!stop) setStats(value) }).catch(() => {})
      loadNodes().then((value) => { if (!stop) setNodes(value ?? []) }).catch(() => {})
    }
    pull()
    const id = setInterval(pull, 5000)
    return () => { stop = true; clearInterval(id) }
  }, [gate, tick])

  if (gate === "loading") return null
  if (gate === "setup") return <SetupPage onSuccess={() => setGate("in")} />
  if (gate === "login") return <LoginPage onSuccess={() => setGate("in")} />

  let body = <DashboardPage stats={stats} nodes={nodes} onFlash={setFlash} onChanged={() => setTick((n) => n + 1)} />
  if (page === "jobs") body = <JobsPage onFlash={setFlash} />
  if (page === "nodes") body = <NodesPage nodes={nodes} onFlash={setFlash} onChanged={() => setTick((n) => n + 1)} />
  if (page === "data") body = <DataPage stats={stats} onFlash={setFlash} />
  if (page === "settings") body = <SettingsPage onFlash={setFlash} />

  return (
    <Shell page={page} stats={stats} clock={clock} flash={flash} onRefresh={() => { setFlash(""); setTick((n) => n + 1) }} onLogout={() => setGate("login")}>
      {body}
    </Shell>
  )
}
