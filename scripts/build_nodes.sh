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
)

rm -rf "$OUT"
mkdir -p "$OUT"

echo "판 $VERSION"
echo

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  name="saucedust-$os-$arch"

  # CGO를 끄면 어느 배포판에도 그대로 올라가는 정적 바이너리가 나옵니다.
  # 라이브러리 버전이 달라 실행이 안 되는 일이 없습니다.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath \
      -ldflags "-s -w -X main.buildVersion=$VERSION" \
      -o "$OUT/$name" ./cmd/saucedust

  size=$(du -h "$OUT/$name" | cut -f1)
  printf "  %-26s %s\n" "$name" "$size"
done

# Python 워커는 노드마다 필요합니다. 함께 묶어 둡니다.
mkdir -p "$OUT/worker"
cp -R python/worker/domain "$OUT/worker/"
cp python/worker/*.py python/worker/*.json python/worker/requirements.txt "$OUT/worker/"
rm -rf "$OUT/worker/domain/__pycache__"

cp internal/config/env.example "$OUT/env.example"

echo
echo "$OUT 에 만들었습니다."
echo
echo "노드에 올리는 방법"
echo "  scp $OUT/saucedust-linux-amd64 node:/usr/local/bin/saucedust"
echo "  scp -r $OUT/worker $OUT/env.example node:~/saucedust/"
echo "  ssh node 'cd ~/saucedust && saucedust setup'"
