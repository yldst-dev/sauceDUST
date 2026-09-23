import { useState } from "react"
import { login } from "../../application/console"
import { Brand } from "../components/Brand"
import { Button } from "../components/Button"
import { Field, TextInput } from "../components/Field"

export function LoginPage({ onSuccess }: { onSuccess: () => void }) {
  const [user, setUser] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")

  return (
    <section className="login">
      <div className="login-card">
        <Brand />
        <h1>제어 화면</h1>
        <p className="sub">수집 현황을 보려면 계정으로 들어가십시오.</p>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setError("")
            login(user.trim(), password)
              .then(() => { setPassword(""); onSuccess() })
              .catch((err: Error) => setError(err.message))
          }}
        >
          <Field label="사용자 이름">
            <TextInput name="username" autoComplete="username" required value={user} onChange={(e) => setUser(e.target.value)} />
          </Field>
          <Field label="비밀번호">
            <TextInput name="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <p className="err" role="alert">{error}</p>
          <Button tone="primary" type="submit">들어가기</Button>
        </form>
      </div>
    </section>
  )
}
