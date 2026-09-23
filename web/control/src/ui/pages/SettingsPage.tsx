import { useEffect, useState } from "react"
import { addModel, applyUpdate, changePassword, checkUpdate, clearTelegram, loadJob, loadSettings, saveSettings, saveTelegram, startRebuild, startReembed, syncModels, verify } from "../../application/console"
import type { Settings, UpdateInfo, VerifyItem } from "../../domain/types"
import { Button } from "../components/Button"
import { Card } from "../components/Card"
import { Check, Field, SelectInput, TextInput } from "../components/Field"
import { PageHeader } from "../components/PageHeader"
import { num } from "../format"

const numberKeys = [
  "backfill_floor", "max_indexed", "backfill_workers", "backfill_range_size",
  "concurrency", "min_concurrency", "max_concurrency", "rate_per_sec", "rate_burst",
  "poll_secs", "thumb_size", "thumb_quality", "fragment_parts", "fragment_delay_ms",
  "embed_batch_size", "embed_batch_timeout_ms", "heartbeat_secs", "node_timeout_secs",
] as const

type NumberKey = (typeof numberKeys)[number]

function jobText(settings: Settings | null, live: { running: string; last: string; error: string } | null) {
  const running = live?.running || settings?.job_running
  const error = live?.error || settings?.job_error
  const last = live?.last || settings?.job_last
  if (running) return `${running} 중입니다.`
  if (error) return error
  if (last) return last
  return ""
}

export function SettingsPage({ onFlash }: { onFlash: (message: string) => void }) {
  const [draft, setDraft] = useState<Settings | null>(null)
  const [apiKey, setApiKey] = useState("")
  const [clearApiKey, setClearApiKey] = useState(false)
  const [proxy, setProxy] = useState("")
  const [clearProxy, setClearProxy] = useState(false)
  const [token, setToken] = useState("")
  const [showToken, setShowToken] = useState(false)
  const [botToken, setBotToken] = useState("")
  const [model, setModel] = useState({ id: "", kind: "copy", vector_size: "", input_size: "224", backend: "", collection: "" })
  const [password, setPassword] = useState({ current: "", next: "" })
  const [checks, setChecks] = useState<VerifyItem[]>([])
  const [update, setUpdate] = useState<UpdateInfo | null>(null)
  const [updating, setUpdating] = useState(false)
  const [panel, setPanel] = useState<"crawl" | "link" | "ops" | "account">("crawl")
  const [liveJob, setLiveJob] = useState<{ running: string; last: string; error: string } | null>(null)

  useEffect(() => {
    loadSettings().then(setDraft).catch((err: Error) => onFlash(err.message))
    checkUpdate().then(setUpdate).catch((err: Error) => onFlash(err.message))
  }, [onFlash])

  useEffect(() => {
    const pull = () => loadJob().then(setLiveJob).catch(() => {})
    pull()
    const id = setInterval(pull, 5000)
    return () => clearInterval(id)
  }, [])

  function setText(key: keyof Settings, value: string) {
    setDraft((current) => current ? { ...current, [key]: value } : current)
  }
  function setNum(key: NumberKey, value: string) {
    setDraft((current) => current ? { ...current, [key]: Number(value) } : current)
  }
  function setBool(key: keyof Settings, value: boolean) {
    setDraft((current) => current ? { ...current, [key]: value } : current)
  }

  function body(): Record<string, unknown> {
    if (!draft) return {}
    const out: Record<string, unknown> = {}
    for (const key of numberKeys) out[key] = draft[key]
    out.adaptive = draft.adaptive
    out.allow_private = draft.allow_private
    out.direct_fallback = draft.direct_fallback
    out.thumb_dir = draft.thumb_dir
    out.index_tags = draft.index_tags
    out.user_agent = draft.user_agent
    out.danbooru_login = draft.danbooru_login
    out.telegram_allowed = draft.telegram_allowed
    out.net_order = draft.net_order
    out.control_bind = draft.control_bind
    if (clearApiKey) out.danbooru_api_key = ""
    else if (apiKey.trim()) out.danbooru_api_key = apiKey.trim()
    if (clearProxy) out.proxy_url = ""
    else if (proxy.trim()) out.proxy_url = proxy.trim()
    if (token.trim()) out.control_token = token.trim()
    return out
  }

  const note = jobText(draft, liveJob)

  const panels = [
    ["crawl", "수집"],
    ["link", "연결"],
    ["ops", "운영"],
    ["account", "계정"],
  ] as const

  return (
    <div className="stage">
      <PageHeader title="설정" lead="저장하면 서버가 다시 시작됩니다. 칸을 바꿔 나머지를 보십시오." />
      <div className="seg">
        {panels.map(([id, label]) => (
          <Button key={id} type="button" aria-current={panel === id ? "true" : undefined} onClick={() => setPanel(id)}>{label}</Button>
        ))}
      </div>
      {note ? <p className="sub">{note}</p> : null}
      <div className="pane">
      <form hidden={panel !== "crawl" && panel !== "link"} onSubmit={(e) => {
        e.preventDefault()
        saveSettings(body()).then(() => {
          onFlash("저장했습니다. 서버가 다시 켜질 때까지 기다립니다.")
          let n = 0
          const timer = setInterval(() => {
            n += 1
            fetch("/health").then((res) => {
              if (res.ok && n > 1) { clearInterval(timer); location.reload() }
            }).catch(() => {})
            if (n > 40) { clearInterval(timer); onFlash("다시 시작이 늦습니다. 잠시 뒤 새로고침하십시오.") }
          }, 500)
        }).catch((err: Error) => onFlash(err.message))
      }}>
        <div hidden={panel !== "crawl"}>
        <Card>
          <fieldset>
            <legend>수집</legend>
            <div className="fields">
              <Field label="과거 하한"><TextInput type="number" min={0} value={draft?.backfill_floor ?? ""} onChange={(e) => setNum("backfill_floor", e.target.value)} /></Field>
              <Field label="색인 상한"><TextInput type="number" min={0} value={draft?.max_indexed ?? ""} onChange={(e) => setNum("max_indexed", e.target.value)} /></Field>
              <Field label="과거 일꾼"><TextInput type="number" min={1} value={draft?.backfill_workers ?? ""} onChange={(e) => setNum("backfill_workers", e.target.value)} /></Field>
              <Field label="구간 크기"><TextInput type="number" min={1} value={draft?.backfill_range_size ?? ""} onChange={(e) => setNum("backfill_range_size", e.target.value)} /></Field>
              <Field label="동시 수"><TextInput type="number" min={1} value={draft?.concurrency ?? ""} onChange={(e) => setNum("concurrency", e.target.value)} /></Field>
              <Field label="동시 최소"><TextInput type="number" min={1} value={draft?.min_concurrency ?? ""} onChange={(e) => setNum("min_concurrency", e.target.value)} /></Field>
              <Field label="동시 최대"><TextInput type="number" min={1} value={draft?.max_concurrency ?? ""} onChange={(e) => setNum("max_concurrency", e.target.value)} /></Field>
              <Field label="초당 요청"><TextInput type="number" min={0} step="0.1" value={draft?.rate_per_sec ?? ""} onChange={(e) => setNum("rate_per_sec", e.target.value)} /></Field>
              <Field label="순간 허용"><TextInput type="number" min={1} value={draft?.rate_burst ?? ""} onChange={(e) => setNum("rate_burst", e.target.value)} /></Field>
              <Field label="확인 주기(초)"><TextInput type="number" min={1} value={draft?.poll_secs ?? ""} onChange={(e) => setNum("poll_secs", e.target.value)} /></Field>
              <Field label="태그 필터"><TextInput value={draft?.index_tags ?? ""} onChange={(e) => setText("index_tags", e.target.value)} /></Field>
              <Field label="식별 문자열"><TextInput value={draft?.user_agent ?? ""} onChange={(e) => setText("user_agent", e.target.value)} /></Field>
              <Field label="사이트 계정"><TextInput autoComplete="off" value={draft?.danbooru_login ?? ""} onChange={(e) => setText("danbooru_login", e.target.value)} /></Field>
              <Check label="동시 수 자동 조절" checked={!!draft?.adaptive} onChange={(v) => setBool("adaptive", v)} />
            </div>
            <Field label="사이트 키"><TextInput type="password" autoComplete="off" placeholder="비우면 그대로 둡니다" value={apiKey} onChange={(e) => setApiKey(e.target.value)} /></Field>
            <Check label="사이트 키 지우기" checked={clearApiKey} onChange={setClearApiKey} />
          </fieldset>
          <Button tone="primary" type="submit">저장하고 다시 시작</Button>
        </Card>
        </div>
        <div hidden={panel !== "link"}>
        <Card>
          <fieldset>
            <legend>축소본</legend>
            <div className="fields">
              <Field label="한 변(픽셀)"><TextInput type="number" min={64} value={draft?.thumb_size ?? ""} onChange={(e) => setNum("thumb_size", e.target.value)} /></Field>
              <Field label="품질"><TextInput type="number" min={1} max={100} value={draft?.thumb_quality ?? ""} onChange={(e) => setNum("thumb_quality", e.target.value)} /></Field>
            </div>
            <Field label="폴더"><TextInput value={draft?.thumb_dir ?? ""} onChange={(e) => setText("thumb_dir", e.target.value)} /></Field>
          </fieldset>
        </Card>
        <Card>
          <fieldset>
            <legend>네트워크</legend>
            <div className="fields">
              <Field label="경로 순서"><TextInput value={draft?.net_order ?? ""} onChange={(e) => setText("net_order", e.target.value)} /></Field>
              <Field label="조각 수"><TextInput type="number" min={1} value={draft?.fragment_parts ?? ""} onChange={(e) => setNum("fragment_parts", e.target.value)} /></Field>
              <Field label="조각 간격(ms)"><TextInput type="number" min={0} value={draft?.fragment_delay_ms ?? ""} onChange={(e) => setNum("fragment_delay_ms", e.target.value)} /></Field>
              <Field label="심장박동(초)"><TextInput type="number" min={1} value={draft?.heartbeat_secs ?? ""} onChange={(e) => setNum("heartbeat_secs", e.target.value)} /></Field>
              <Field label="끊김 판정(초)"><TextInput type="number" min={1} value={draft?.node_timeout_secs ?? ""} onChange={(e) => setNum("node_timeout_secs", e.target.value)} /></Field>
              <Check label="사설 주소 허용" checked={!!draft?.allow_private} onChange={(v) => setBool("allow_private", v)} />
              <Check label="실패하면 직접 연결" checked={!!draft?.direct_fallback} onChange={(v) => setBool("direct_fallback", v)} />
            </div>
            <Field label="프록시"><TextInput type="password" autoComplete="off" placeholder="비우면 그대로 둡니다" value={proxy} onChange={(e) => setProxy(e.target.value)} /></Field>
            <Check label="프록시 지우기" checked={clearProxy} onChange={setClearProxy} />
          </fieldset>
        </Card>
        <Card>
          <fieldset>
            <legend>임베딩</legend>
            <div className="fields">
              <Field label="묶음"><TextInput type="number" min={1} value={draft?.embed_batch_size ?? ""} onChange={(e) => setNum("embed_batch_size", e.target.value)} /></Field>
              <Field label="대기(ms)"><TextInput type="number" min={1} value={draft?.embed_batch_timeout_ms ?? ""} onChange={(e) => setNum("embed_batch_timeout_ms", e.target.value)} /></Field>
            </div>
          </fieldset>
        </Card>
        <Card>
          <fieldset>
            <legend>접속</legend>
            <p className="sub">주소를 잘못 적으면 이 화면이 열리지 않습니다. 그때는 자료 폴더의 web-config.json을 고쳐야 합니다.</p>
            <Field label="제어 화면 주소"><TextInput value={draft?.control_bind ?? ""} onChange={(e) => setText("control_bind", e.target.value)} /></Field>
            <Field label="작업 노드 접속 토큰">
              <TextInput type={showToken ? "text" : "password"} autoComplete="off" placeholder="비우면 그대로 둡니다" value={token} onChange={(e) => setToken(e.target.value)} />
            </Field>
            <div className="actions">
              <Button type="button" onClick={() => {
                if (!draft?.control_token) { onFlash("저장된 접속 토큰이 없습니다"); return }
                setToken(draft.control_token)
                setShowToken(true)
              }}>토큰 보기</Button>
            </div>
            <Field label="텔레그램을 쓸 사용자 번호">
              <TextInput placeholder="쉼표로 구분. 비우면 제한 없음" value={draft?.telegram_allowed ?? ""} onChange={(e) => setText("telegram_allowed", e.target.value)} />
            </Field>
          </fieldset>
          <Button tone="primary" type="submit">저장하고 다시 시작</Button>
        </Card>
        </div>
      </form>
      <div hidden={panel !== "account"}>
      <Card title="텔레그램 봇">
        <p className="sub">{draft?.telegram_set ? `봇이 켜져 있습니다. 토큰 끝자리는 ${draft.telegram_hint || ""} 입니다. 새 값을 저장하면 바로 다시 붙습니다.` : "토큰이 없습니다. 넣으면 봇이 바로 붙습니다."}</p>
        <form onSubmit={(e) => {
          e.preventDefault()
          saveTelegram(botToken.trim()).then(() => { setBotToken(""); onFlash("텔레그램 봇 토큰을 저장했습니다"); loadSettings().then(setDraft) }).catch((err: Error) => onFlash(err.message))
        }}>
          <Field label="봇 토큰"><TextInput type="password" autoComplete="off" placeholder="BotFather가 준 토큰" value={botToken} onChange={(e) => setBotToken(e.target.value)} /></Field>
          <div className="actions">
            <Button tone="primary" type="submit">토큰 저장</Button>
            <Button tone="danger" type="button" onClick={() => clearTelegram().then(() => { onFlash("텔레그램 봇을 껐습니다"); loadSettings().then(setDraft) }).catch((err: Error) => onFlash(err.message))}>봇 끄기</Button>
          </div>
        </form>
      </Card>
      <Card title="로그인">
        <p className="sub">제어 화면 비밀번호를 바꿉니다. 8자 이상입니다.</p>
        <form onSubmit={(e) => {
          e.preventDefault()
          changePassword(password.current, password.next).then(() => { setPassword({ current: "", next: "" }); onFlash("비밀번호를 바꿨습니다") }).catch((err: Error) => onFlash(err.message))
        }}>
          <TextInput name="username" autoComplete="username" value="admin" readOnly hidden />
          <Field label="현재 비밀번호"><TextInput type="password" autoComplete="current-password" required value={password.current} onChange={(e) => setPassword({ ...password, current: e.target.value })} /></Field>
          <Field label="새 비밀번호"><TextInput type="password" autoComplete="new-password" minLength={8} required value={password.next} onChange={(e) => setPassword({ ...password, next: e.target.value })} /></Field>
          <Button tone="primary" type="submit">비밀번호 바꾸기</Button>
        </form>
      </Card>
      </div>
      <div hidden={panel !== "ops"}>
      <Card title="모델">
        <div className="actions">
          <Button type="button" onClick={() => syncModels().then((b) => onFlash(`${num(b.count)}개 모델을 등록했습니다`)).catch((err: Error) => onFlash(err.message))}>워커 모델 그대로 등록</Button>
        </div>
        <form className="fields" onSubmit={(e) => {
          e.preventDefault()
          addModel({
            id: model.id.trim(), kind: model.kind, vector_size: Number(model.vector_size),
            input_size: Number(model.input_size), backend: model.backend.trim(), collection: model.collection.trim(),
          }).then(() => { onFlash("모델을 등록했습니다"); setModel({ id: "", kind: "copy", vector_size: "", input_size: "224", backend: "", collection: "" }) }).catch((err: Error) => onFlash(err.message))
        }}>
          <Field label="이름"><TextInput required value={model.id} onChange={(e) => setModel({ ...model, id: e.target.value })} /></Field>
          <Field label="용도">
            <SelectInput value={model.kind} onChange={(e) => setModel({ ...model, kind: e.target.value })}>
              <option value="copy">원본 찾기</option>
              <option value="semantic">비슷한 그림</option>
            </SelectInput>
          </Field>
          <Field label="차원"><TextInput type="number" min={1} required value={model.vector_size} onChange={(e) => setModel({ ...model, vector_size: e.target.value })} /></Field>
          <Field label="입력 픽셀"><TextInput type="number" min={1} value={model.input_size} onChange={(e) => setModel({ ...model, input_size: e.target.value })} /></Field>
          <Field label="백엔드"><TextInput value={model.backend} onChange={(e) => setModel({ ...model, backend: e.target.value })} /></Field>
          <Field label="컬렉션"><TextInput value={model.collection} onChange={(e) => setModel({ ...model, collection: e.target.value })} /></Field>
          <Button tone="primary" type="submit">모델 등록</Button>
        </form>
      </Card>
      <Card title="업데이트">
        <p className="sub">
          {update
            ? `현재 ${update.current || "알 수 없음"} · 최신 ${update.latest || "확인 전"}`
            : "최신 릴리스를 확인합니다."}
        </p>
        {update?.notes ? <p className="sub">{update.notes}</p> : null}
        {update?.error ? <p className="err">{update.error}</p> : null}
        <div className="actions">
          <Button type="button" onClick={() => checkUpdate().then(setUpdate).catch((err: Error) => onFlash(err.message))}>업데이트 확인</Button>
          {update?.available ? (
            <Button tone="primary" type="button" disabled={updating} onClick={() => {
              setUpdating(true)
              applyUpdate().then(() => {
                onFlash("새 버전을 받았습니다. 서버가 다시 켜질 때까지 기다립니다.")
                let n = 0
                const timer = setInterval(() => {
                  n += 1
                  fetch("/health").then((res) => {
                    if (res.ok && n > 1) { clearInterval(timer); location.reload() }
                  }).catch(() => {})
                  if (n > 40) { clearInterval(timer); setUpdating(false); onFlash("다시 시작이 늦습니다. 잠시 뒤 새로고침하십시오.") }
                }, 500)
              }).catch((err: Error) => { setUpdating(false); onFlash(err.message) })
            }}>{updating ? "받는 중" : `${update.latest}로 업데이트`}</Button>
          ) : null}
        </div>
      </Card>
      <Card title="유지보수">
        {note ? <p className="sub">{note}</p> : null}
        <div className="actions">
          <Button type="button" onClick={() => verify().then(setChecks).catch((err: Error) => onFlash(err.message))}>점검</Button>
          <Button type="button" onClick={() => startRebuild().then(() => onFlash("색인을 다시 채우기 시작했습니다")).catch((err: Error) => onFlash(err.message))}>색인 다시 채우기</Button>
          <Button type="button" onClick={() => startReembed(true).then(() => onFlash("양을 세고 있습니다")).catch((err: Error) => onFlash(err.message))}>다시 계산할 양 보기</Button>
          <Button tone="danger" type="button" onClick={() => startReembed(false).then(() => onFlash("벡터를 다시 계산하기 시작했습니다")).catch((err: Error) => onFlash(err.message))}>축소본으로 벡터 다시 계산</Button>
        </div>
        {checks.length > 0 ? (
          <table>
            <tbody>
              {checks.map((item) => (
                <tr key={item.name}><td>{item.name}</td><td>{item.ok === "true" ? "정상" : "실패"}</td><td>{item.note}</td></tr>
              ))}
            </tbody>
          </table>
        ) : null}
      </Card>
      </div>
      </div>
    </div>
  )
}
