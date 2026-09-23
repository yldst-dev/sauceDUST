import { useState } from "react"
import { setupAccount } from "../../application/console"
import { Brand } from "../components/Brand"
import { Button } from "../components/Button"
import { Field, TextInput } from "../components/Field"

export function SetupPage({ onSuccess }: { onSuccess: () => void }) {
  const [user, setUser] = useState("admin")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState("")

  return (
    <section className="login">
      <div className="login-card">
        <Brand />
        <h1>처음 설정</h1>
        <p className="sub">이 제어 화면의 계정을 만드십시오. 한 번 만들면 다음부터는 로그인입니다.</p>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setError("")
            setupAccount(user.trim(), password, confirm)
              .then(() => onSuccess())
              .catch((err: Error) => setError(err.message))
          }}
        >
          <Field label="사용자 이름">
            <TextInput name="username" autoComplete="username" required value={user} onChange={(e) => setUser(e.target.value)} />
          </Field>
          <Field label="비밀번호">
            <TextInput name="new-password" type="password" autoComplete="new-password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <Field label="비밀번호 확인">
            <TextInput name="confirm-password" type="password" autoComplete="new-password" required minLength={8} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          <p className="err" role="alert">{error}</p>
          <Button tone="primary" type="submit">계정 만들기</Button>
        </form>
      </div>
    </section>
  )
}
