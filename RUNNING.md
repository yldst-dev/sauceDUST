# saucedust 실행 안내

여러 대의 컴퓨터에서 나눠 돌리는 이미지 역검색 시스템입니다.
무엇이 어떻게 생겼고 왜 그렇게 정했는지는 [README.md](README.md)를 보십시오.

## 구성

노드는 두 가지 역할 중 하나를 맡습니다.

| 역할 | 하는 일 | 필요한 것 |
|---|---|---|
| control | 결과 수신, 검색, 대시보드, 죽은 노드 정리 | PostgreSQL, Qdrant, 임베딩 워커 |
| worker | 구간을 임대받아 수집하고 중앙으로 전송 | 임베딩 워커, PostgreSQL 접속 |

작업 노드는 구간 조정을 위해 PostgreSQL에 붙지만, 이미지와 벡터는 HTTP로 중앙에 보냅니다.
그래서 Qdrant와 축소본 저장소는 중앙 노드만 알면 됩니다.

```
worker A ─┐  결과(HTTP)
worker B ─┼─→ control ─→ PostgreSQL + Qdrant + 축소본
worker C ─┘      ↑
                 └─ 구간 임대 (worker들이 직접 조회)
```

## 준비

새 노드에서는 이것만 하면 됩니다.

```bash
go build -o saucedust ./cmd/saucedust
./saucedust setup     # Python 가상 환경, 의존성, 모델 가중치까지 준비합니다
./saucedust doctor    # 준비 상태만 확인합니다. 아무것도 바꾸지 않습니다
```

## 노드에 배포하기

노드에는 **실행 파일 하나만** 올리면 됩니다.

```bash
./scripts/build_nodes.sh
scp dist/saucedust-linux-amd64 node:/usr/local/bin/saucedust
ssh node 'mkdir -p ~/saucedust && cd ~/saucedust && saucedust setup'
```

macOS와 리눅스, arm64와 amd64용 정적 바이너리가 `dist/`에 나옵니다.
각각 11에서 12메가바이트이고, 안에 이런 것이 들어 있습니다.

- Python 워커 소스 전부
- 설정 본보기
- 데이터베이스 스키마
- 웹 대시보드

`setup`이 실행 파일에서 이것들을 풀어 놓고, 이어서 아래를 준비합니다.

| 단계 | 크기 | 왜 실행 파일에 못 넣는지 |
|---|---|---|
| Python 가상 환경 | — | Python 자체는 노드에 깔려 있어야 합니다 |
| torch와 의존성 | 약 3GB | CPU 종류와 CUDA 판마다 다른 것을 받습니다 |
| 모델 가중치 | 약 1.5GB | 모델을 바꾸면 달라지므로 설정을 보고 받습니다 |

즉 **나르는 것은 파일 하나**이고, 나머지 4.5기가바이트는 노드가 인터넷에서
직접 받습니다. 노드마다 같은 것을 scp로 밀어 넣지 않아도 됩니다.

노드에 미리 필요한 것은 Python 3.11 이상과 `psql` 두 가지뿐입니다.

`.env`를 열어 최소한 아래 값을 채우십시오.

```bash
SAUCEDUST_NODE_ID=mac-mini-1
SAUCEDUST_NODE_ROLE=control
DATABASE_URL=postgres://sauce:saucepass@10.0.0.5:5432/sauce
QDRANT_URL=http://10.0.0.5:6333
SAUCEDUST_CONTROL_TOKEN=아무거나-긴-무작위-문자열
```

`SAUCEDUST_CONTROL_TOKEN`을 비워두면 API가 인증 없이 열립니다.
집 안 네트워크라도 채우시길 권합니다.

## 임베딩 워커

노드마다 하나씩 로컬로 띄웁니다. 이미지 바이트가 네트워크를 타지 않고,
각 컴퓨터의 GPU를 모두 쓰게 됩니다.

```bash
cd python/worker
python3.12 -m venv .venv
./.venv/bin/pip install -r requirements.txt
./.venv/bin/uvicorn main:app --host 127.0.0.1 --port 8100
```

장치는 CUDA, MPS, CPU 순으로 자동으로 고릅니다.
쓸 모델은 `models.json`에 적습니다.

## 모델 등록

모든 노드가 완전히 같은 모델을 써야 합니다. 하나만 달라도 벡터를 비교할 수
없고, 오류 없이 검색 결과만 조용히 이상해집니다.
그래서 노드가 시작할 때 워커가 올린 모델을 기준과 대조하고, 다르면 작업을 거부합니다.

```bash
./saucedust migrate
./saucedust model add -id siglip-b16 -kind copy \
  -backend transformers -checkpoint google/siglip-base-patch16-224 -vector-size 768
./saucedust model add -id clip-b32 -kind semantic \
  -backend transformers -checkpoint laion/CLIP-ViT-B-32-laion2B-s34B-b79K -vector-size 512
./saucedust model ls
```

`kind=copy`는 원본 찾기용, `kind=semantic`은 비슷한 그림 찾기용입니다.
용도별로 활성 모델은 하나뿐이며, 새로 등록하면 이전 것이 자동으로 내려갑니다.
내려간 모델의 벡터는 남아 있어 되돌릴 수 있습니다.

## 어느 모델을 쓸지 정하기

인터넷 벤치마크는 대부분 실사 사진 기준이라 애니 그림에서 어떨지는 다릅니다.
직접 재보십시오.

```bash
cd python/worker
./.venv/bin/python benchmark.py \
  --images ~/.saucedust/thumbs --limit 20000 --queries 2000
```

원본을 망가뜨린 복사본으로 검색해서 원본이 1등에 나오는 비율을 잽니다.
비교 기준선으로 지각 해시만 쓴 결과도 함께 나옵니다.

`--limit`은 후보 수, `--queries`는 검색해 볼 횟수입니다. 어려움을 정하는
것은 후보 수입니다. 후보가 적으면 어느 모델이든 다 맞혀서 우열을 가릴 수
없습니다. 실측에서 후보 200장으로는 세 모델이 모두 99.8퍼센트를 넘었습니다.
수만 장으로 잡으십시오.

## 모델을 바꾸기

바꾸면 쌓인 벡터가 전부 쓸모없어집니다. 다만 축소본을 남겨 두므로
Danbooru를 다시 훑을 필요는 없습니다.

```bash
saucedust model add -id 새모델 -kind copy -vector-size 768
saucedust reembed -model 새모델 -dry-run
saucedust reembed -model 새모델
```

`-dry-run`은 할 일이 몇 건인지만 세어 봅니다. 며칠 걸릴 수 있으니 먼저
확인하십시오. 중간에 멈춰도 다음에 부르면 아직 벡터가 없는 것부터 이어서
합니다.

워커의 `models.json`도 함께 고쳐야 합니다. 고치지 않으면 워커가 새 모델을
올리지 않아 벡터를 주지 못합니다.

`rebuild`와 헷갈리지 마십시오. `rebuild`는 PostgreSQL에 이미 있는 벡터를
Qdrant로 옮기는 복구용입니다. Qdrant 저장소를 잃었을 때 씁니다.
`reembed`는 벡터 자체를 새 모델로 다시 만듭니다.

축소본 파일이 없어진 건수는 따로 셉니다. 그것만은 다시 내려받아야 합니다.

## 실행

중앙 노드:

```bash
./saucedust control
./saucedust control -no-crawl   # 수집 없이 서버 역할만
```

작업 노드:

```bash
SAUCEDUST_NODE_ROLE=worker SAUCEDUST_CONTROL_URL=http://10.0.0.5:8000 ./saucedust worker
```

새 노드가 정말 도는지 확인할 때는 조금만 모아 보고 멈추게 하십시오.

```bash
./saucedust control -limit 20
./saucedust worker -limit 20
```

끝없이 도는 것을 띄웠다가 손으로 죽이면 어디까지 갔는지도 얼마나 걸렸는지도
남지 않습니다. 상한을 정하면 다 채우는 즉시 스스로 멈추고 로그에 남깁니다.

멈춘 시점에 처리 중이던 구간은 실패로 남지만 재시도가 줄지 않아 다음
실행에서 다시 배정됩니다. 빠지는 이미지는 없습니다.

대시보드는 중앙 노드의 `SAUCEDUST_CONTROL_BIND` 주소에서 열립니다.

## 네트워크 경로

외부 요청은 아래 순서로 시도하고, 실패하면 다음으로 내려갑니다.

| 순서 | 방식 | 특징 |
|---|---|---|
| direct | 그대로 나감 | 가장 빠름 |
| ech | TLS 인사말의 도메인 이름을 암호화 | 상대 서버 지원 필요 |
| frag | 인사말을 여러 조각으로 나눠 전송 | 서버 지원 불필요, 조각을 다시 합치는 장비에는 안 통함 |
| vpn | 프록시 경유 | 느리지만 확실함 |

호스트마다 통하는 경로를 기억해 다음부터는 그것부터 씁니다.
어느 경로가 되는지 미리 재보려면:

```bash
./saucedust probe
./saucedust probe -samples 5 -parallel 16
```

`probe`는 API 한 곳과 실제 이미지 한 장을 받아봅니다. `-parallel`을 주면
동시 다운로드를 올려가며 이 노드의 상한도 함께 잽니다.

`SAUCEDUST_NET_ORDER`로 순서를 바꿀 수 있습니다. `sni`라고 적으면 `ech,frag`로 펼쳐집니다.

### 한국 회선 실측 (2026-08-02, LG유플러스)

| 호스트 | direct | ech | frag |
|---|---|---|---|
| `danbooru.donmai.us` | 차단됨 | 설정 없음 | **성공 197ms, HTTP/2** |
| `cdn.donmai.us` | 성공 65ms, HTTP/2 | 설정 없음 | 성공 58ms, HTTP/2 |

- API 호스트는 SNI 차단을 받지만 **조각내기로 통과합니다.**
- 이미지 CDN은 **애초에 막혀 있지 않습니다.** 트래픽의 대부분이 여기입니다.
- ECH는 Danbooru가 DNS에 설정을 올리지 않아 쓸 수 없습니다. 빠르게 실패하고
  다음 경로로 넘어가므로 남겨 두어도 해가 없습니다.
- **VPN 없이 동작합니다.** 프록시는 위 두 경로가 모두 막혔을 때를 위한 대비책입니다.

통했던 경로는 `net_probes`에 남아 다음 실행에서 바로 쓰입니다.
막힌 경로부터 다시 더듬지 않으므로 첫 요청이 빨라집니다.

조각내기가 통하지 않게 되면 조각 수와 지연을 올려보십시오.

```bash
SAUCEDUST_FRAGMENT_PARTS=5
SAUCEDUST_FRAGMENT_DELAY_MS=10
```

회선 사정은 지역과 통신사, 그리고 차단 장비 업데이트에 따라 달라집니다.
노드를 새로 놓을 때마다 그 노드에서 직접 재보십시오.

## 수집 속도

Danbooru는 **IP 단위로 속도를 제한합니다.** 실측에서 동시 다운로드를 32로
올렸더니 128건 중 29건이 429로 돌아왔고, 그 뒤로는 동시 1에서도 한동안
429가 이어졌습니다.

그래서 동시 수와 별개로 초당 요청 수 자체에 상한을 둡니다.

```bash
CRAWL_RATE_PER_SEC=5     # API 조회와 이미지 내려받기를 합친 초당 상한
CRAWL_RATE_BURST=8       # 잠깐 몰아서 보낼 수 있는 양
CRAWL_MAX_CONCURRENCY=16 # 동시 처리 상한
```

429를 받으면 `Retry-After`를 읽어 그만큼 쉬고, 동시성 조절기도 한도를
절반으로 낮춥니다.

**동시 수를 올려서 얻을 것이 없습니다.** 제한이 IP에 걸리므로,
빠르게 모으려면 노드를 늘려 IP를 늘리는 것이 유일한 방법입니다.
계정을 만들어 API 키를 쓰면 한도가 올라가는지도 확인해 볼 만합니다.

## 저장되는 것

| 대상 | 위치 | 1,000만 장 기준 |
|---|---|---|
| 메타데이터 | PostgreSQL `images` | 약 30GB |
| 벡터 백업 | PostgreSQL `image_vectors` | 모델당 약 30GB |
| 검색 색인 | Qdrant (int8 압축) | 모델당 약 7GB |
| 축소본 384px | `$SAUCEDUST_DATA_DIR/thumbs` | 약 250GB |

원본 이미지는 저장하지 않습니다.
축소본을 남기는 이유는 모델을 바꿀 때 다시 내려받지 않기 위해서입니다.

벡터를 PostgreSQL에도 두는 이유는 Qdrant를 잃어도 복구하기 위해서입니다.

```bash
./saucedust rebuild
```

## 빠짐없이 모으기

수집 구간은 시도 상한을 넘으면 더 이상 배정되지 않습니다. 일시적인 통신
장애로 상한을 소진하면 그 ID 대역은 영영 들어오지 않습니다. 오래 돌릴수록
이런 구멍이 쌓이므로 가끔 확인하십시오.

```bash
./saucedust ranges status    # 완료, 실패, 소진 개수
./saucedust ranges failed    # 소진된 구간과 마지막 오류
./saucedust ranges reset     # 그 구간을 다시 배정되게 합니다
./saucedust ranges gaps      # 구간이 아예 만들어지지 않은 대역
./saucedust ranges fill      # 그 대역을 수집 대상에 넣습니다
```

특정 게시물이 들어왔는지 확인하고, 없으면 왜 없는지 알려줍니다.

```bash
./saucedust check-post 11905042
```

## 텔레그램 봇

토큰을 넣으면 중앙 노드가 봇도 함께 띄웁니다.

```bash
TELEGRAM_BOT_TOKEN=...
TELEGRAM_ALLOWED_USERS=123456789,987654321
```

`TELEGRAM_ALLOWED_USERS`를 비워 두면 토큰을 아는 누구나 검색할 수 있습니다.
그림을 보내면 원본을 찾아 게시물 링크와 함께 답합니다.

## 노드 관리

```bash
./saucedust node ls        # 상태, 장치, 경로, 처리량
./saucedust node reclaim   # 응답 없는 노드의 임대를 즉시 회수
./saucedust verify         # 각 부품이 실제로 응답하는지 확인
./saucedust stats          # 얼마나 쌓였는지 확인
```

노드는 30초마다 살아있음을 알립니다. 90초 동안 소식이 없으면 중앙 노드가
그 노드의 구간을 회수해 다른 노드가 이어받습니다.

노드가 재시작할 때는 자기 임대만 회수합니다. 다른 노드가 처리 중인 구간은
건드리지 않습니다.

## 검증

한 번에 다 돌립니다.

```bash
./scripts/verify.sh
```

서식, 빌드, vet, staticcheck, gosec 보안 검사, govulncheck 취약점 검사,
Go 시험(race), Python 린트와 타입 검사와 시험을 차례로 돌립니다.
저장소나 도구가 없으면 건너뛰었다고 분명히 알립니다.

전부 돌리려면 아래를 준비하십시오.

검사 도구를 깝니다.

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/securego/gosec/v2/cmd/gosec@latest
go install golang.org/x/vuln/cmd/govulncheck@latest
```

시험용 계정과 저장소를 만듭니다. 계정 없이 주소만 넣으면 연결하지 못하고
통합 시험이 통째로 실패합니다.

```bash
psql -d postgres -c "CREATE ROLE sauce LOGIN PASSWORD 'saucepass'"
psql -d postgres -c "CREATE DATABASE sauce OWNER sauce"
docker run -d -p 6333:6333 qdrant/qdrant
```

그다음 주소를 넣고 돌립니다.

```bash
SAUCEDUST_TEST_DATABASE_URL=postgres://sauce:saucepass@localhost:5432/sauce \
SAUCEDUST_TEST_QDRANT_URL=http://localhost:6333 \
  ./scripts/verify.sh
```

시험은 패키지마다 별도 PostgreSQL 스키마를 씁니다. 병렬로 돌아도 서로의
표를 지우지 않습니다.

두 줄 다 넣고 돌렸을 때 검사 11개가 모두 통과하고 "건너뜀"이 하나도
나오지 않아야 합니다.
