#!/usr/bin/env bash
# 여러 종류의 노드에 올릴 바이너리를 한 번에 만듭니다.
#
# Go는 어느 컴퓨터에서든 다른 운영체제와 CPU용 바이너리를 뽑을 수 있습니다.
# 맥에서 만들어 리눅스 노드에 그대로 올려도 됩니다.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${OUT:-dist}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# 노드로 쓸 만한 조합입니다. 필요하면 여기에 더하십시오.
TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
  "windows/arm64"
)

# OUT을 밖에서 넘길 수 있으므로 그대로 지우면 안 됩니다.
# OUT=$HOME 한 번이면 홈이 날아갑니다.
case "$OUT" in
  ""|"/"|"$HOME"|"$HOME/")
    echo "지울 수 없는 경로입니다: ${OUT:-비어 있음}" >&2
    exit 1
    ;;
esac
if [ -e "$OUT" ] && [ ! -f "$OUT/.saucedust-build" ]; then
  echo "$OUT는 이 스크립트가 만든 폴더가 아닙니다. 직접 지우고 다시 부르십시오." >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$OUT"
touch "$OUT/.saucedust-build"

echo "판 $VERSION"
echo

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  name="saucedust-$os-$arch"
  [ "$os" = "windows" ] && name="$name.exe"

  # CGO를 끄면 어느 배포판에도 그대로 올라가는 정적 바이너리가 나옵니다.
  # 라이브러리 버전이 달라 실행이 안 되는 일이 없습니다.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath \
      -ldflags "-s -w -X main.buildVersion=$VERSION" \
      -o "$OUT/$name" ./cmd/saucedust

  size=$(du -h "$OUT/$name" | cut -f1)
  printf "  %-26s %s\n" "$name" "$size"
done

# Python 워커와 설정 본보기는 실행 파일 안에 들어 있습니다.
# setup이 알아서 풀어 놓으므로 따로 나르지 않습니다.
echo
echo "$OUT 에 만들었습니다. 노드에는 파일 하나만 올리면 됩니다."
echo
echo "노드에 올리는 방법"
echo "  리눅스   scp $OUT/saucedust-linux-amd64 node:/usr/local/bin/saucedust"
echo "           ssh node 'mkdir -p ~/saucedust && cd ~/saucedust && saucedust setup'"
echo "  macOS    같습니다. saucedust-darwin-arm64 또는 -amd64를 쓰십시오"
echo "  Windows  saucedust-windows-amd64.exe를 빈 폴더에 두고 그 폴더에서"
echo "           saucedust-windows-amd64.exe setup 을 실행하십시오"
echo
echo "setup이 하는 일"
echo "  .env와 Python 워커를 풀어 놓습니다 (실행 파일 안에 들어 있습니다)"
echo "  가상 환경을 만들고 torch를 깝니다 (약 3기가바이트)"
echo "  모델 가중치를 미리 받습니다 (약 1.5기가바이트)"
