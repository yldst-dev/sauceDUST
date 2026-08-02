#!/usr/bin/env bash
# 코드를 고친 뒤 이것 하나만 돌리면 됩니다.
#
# 저장소나 컨테이너가 필요한 검사는 환경값이 있을 때만 돌고, 없으면 건너뛰었다고
# 분명히 알립니다. 조용히 넘어가면 검증했다고 착각하게 됩니다.
set -uo pipefail

cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"

FAILED=0
SKIPPED=()

step() {
  local name="$1"; shift
  printf '\n\033[1m== %s ==\033[0m\n' "$name"
  if "$@"; then
    printf '\033[32m통과\033[0m\n'
  else
    printf '\033[31m실패\033[0m\n'
    FAILED=$((FAILED + 1))
  fi
}

skip() {
  SKIPPED+=("$1")
  printf '\n\033[1m== %s ==\033[0m\n\033[33m건너뜀: %s\033[0m\n' "$1" "$2"
}

need() { command -v "$1" >/dev/null 2>&1; }

# Go ------------------------------------------------------------------------

check_fmt() {
  local out
  out=$(gofmt -l ./cmd ./internal)
  if [ -n "$out" ]; then
    echo "서식이 어긋난 파일:"
    echo "$out"
    return 1
  fi
}

step "Go 서식" check_fmt
step "Go 빌드" go build ./...
step "go vet" go vet ./...

if need staticcheck; then
  step "staticcheck" staticcheck ./...
else
  skip "staticcheck" "go install honnef.co/go/tools/cmd/staticcheck@latest"
fi

if need gosec; then
  step "gosec 보안 검사" gosec -quiet ./...
else
  skip "gosec" "go install github.com/securego/gosec/v2/cmd/gosec@latest"
fi

if need govulncheck; then
  step "govulncheck 취약점 검사" govulncheck ./...
else
  skip "govulncheck" "go install golang.org/x/vuln/cmd/govulncheck@latest"
fi

# 저장소가 붙는 시험은 환경값이 있을 때만 실제로 돕니다.
if [ -n "${SAUCEDUST_TEST_DATABASE_URL:-}" ]; then
  step "Go 시험 (저장소 포함, race)" go test ./... -race -count=1
else
  step "Go 시험 (race)" go test ./... -race -count=1
  SKIPPED+=("저장소 시험: SAUCEDUST_TEST_DATABASE_URL를 넣으십시오")
fi

if [ -z "${SAUCEDUST_TEST_QDRANT_URL:-}" ]; then
  SKIPPED+=("실제 Qdrant 시험: docker run -d -p 6333:6333 qdrant/qdrant 뒤 SAUCEDUST_TEST_QDRANT_URL를 넣으십시오")
fi

# Python --------------------------------------------------------------------

WORKER="python/worker"
VENV="$WORKER/.venv/bin"

if [ -x "$VENV/python" ]; then
  if [ -x "$VENV/ruff" ]; then
    step "Python 린트" "$VENV/ruff" check "$WORKER"
  else
    skip "Python 린트" "$VENV/pip install ruff"
  fi

  if [ -x "$VENV/mypy" ]; then
    step "Python 타입 검사" bash -c "cd $WORKER && .venv/bin/mypy ."
  else
    skip "Python 타입 검사" "$VENV/pip install mypy"
  fi

  step "Python 시험" bash -c "cd $WORKER && .venv/bin/python -m pytest tests/ -q"
else
  skip "Python 검사 전체" "saucedust setup으로 가상 환경을 만드십시오"
fi

# 정리 ----------------------------------------------------------------------

printf '\n\033[1m== 요약 ==\033[0m\n'
for item in "${SKIPPED[@]:-}"; do
  [ -n "$item" ] && printf '\033[33m건너뜀\033[0m %s\n' "$item"
done

if [ "$FAILED" -gt 0 ]; then
  printf '\033[31m%d개 검사가 실패했습니다.\033[0m\n' "$FAILED"
  exit 1
fi
printf '\033[32m모든 검사를 통과했습니다.\033[0m\n'
