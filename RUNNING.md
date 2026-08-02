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

## 컴퓨터 한 대에서 써 보기

어느 운영체제든 순서는 같습니다. **실행 파일 하나를 빈 폴더에 두고 `setup`을
부르면 나머지를 스스로 갖춥니다.**

미리 있어야 하는 것은 두 가지뿐입니다.

| | Python 3.11 이상 | PostgreSQL 클라이언트 |
|---|---|---|
| **Windows** | `winget install Python.Python.3.12` | `winget install PostgreSQL.PostgreSQL` |
| **macOS** | `brew install python@3.12` | `brew install postgresql@16` |
| **Linux** | `apt install python3.12-venv` | `apt install postgresql-client` |

없으면 `setup`이 그 운영체제에 맞는 명령을 알려 주고 멈춥니다.

### Windows

`dist\saucedust-windows-amd64.exe`를 빈 폴더에 두고 그 폴더에서 명령 프롬프트를
엽니다.

```
saucedust-windows-amd64.exe setup
saucedust-windows-amd64.exe doctor
```

### macOS와 Linux

```bash
chmod +x saucedust
./saucedust setup
./saucedust doctor
```

`setup`은 실행 파일 안에서 Python 워커와 `.env`를 풀어 놓고, 가상 환경을 만들고,
torch와 모델 가중치를 받습니다. 처음 한 번만 몇 분 걸립니다.
`doctor`는 확인만 하고 아무것도 바꾸지 않습니다.

워커는 **호스트에서 그대로 돕니다.** 컨테이너에 넣지 마십시오. macOS에서는
컨테이너가 GPU에 닿지 못해 CPU로 떨어집니다. 아래에서 Docker를 쓰는 곳은
PostgreSQL과 Qdrant뿐이고, 그쪽은 넣어도 손해가 없습니다. 잰 값은
[README](README.md#워커는-컨테이너에-넣지-않습니다)에 있습니다.

그다음 저장소를 띄우고 `.env`를 채운 뒤 돌립니다.

```bash
docker run -d -p 6333:6333 qdrant/qdrant
./saucedust migrate
./saucedust model add -id siglip-b16 -kind copy \
  -backend transformers -checkpoint google/siglip-base-patch16-224 -vector-size 768
./saucedust control -limit 20
```

`-limit 20`은 스무 장만 모으고 멈춥니다. 처음에는 이렇게 확인하십시오.

Windows에서는 `./saucedust` 대신 `saucedust-windows-amd64.exe`로 읽으십시오.
나머지는 같습니다.

## 여러 대에서 중앙으로 모으기

컴퓨터를 늘리는 이유는 **Danbooru가 IP 단위로 속도를 제한하기 때문**입니다.
한 대에서 더 세게 밀어도 소용이 없고, 대수를 늘려야 빨라집니다.

역할을 나눕니다.

```
작업 노드 A ─┐
작업 노드 B ─┼─ HTTP ─→ 중앙 노드 ─→ PostgreSQL + Qdrant + 축소본
작업 노드 C ─┘              ↑
                            └─ 구간 임대 (작업 노드가 직접 조회)
```

**중앙 노드는 한 대**입니다. PostgreSQL, Qdrant, 축소본을 모두 가집니다.
작업 노드는 수집과 계산만 하고 결과를 HTTP로 보냅니다. 이미지도 축소본도
작업 노드에는 남지 않습니다.

### 먼저: 어디까지 열지 정합니다

작업 노드가 중앙에 닿으려면 포트가 열려야 하는데, **무엇에게 열지**를 먼저
정해야 합니다. 이 결정이 아래 모든 주소를 바꿉니다.

| | 어떻게 | 언제 |
|---|---|---|
| 같은 랜 | 사설 주소에 붙이고 방화벽으로 막기 | 컴퓨터가 전부 한 건물에 있을 때 |
| **메시 VPN** | **초대한 사람만 들어오는 사설망을 만들기** | **집이나 사무실이 흩어져 있을 때** |
| 공개 | 도메인, 인증서, 역방향 프록시 | 권하지 않습니다 |

**흩어져 있다면 메시 VPN을 쓰십시오.** [메시 VPN으로 묶기](#메시-vpn으로-묶기)에
설정이 있습니다. 초대 기반이라 원하는 사람만 들어오고, 포트 포워딩이 필요 없고,
무엇보다 **saucedust는 TLS를 하지 않으므로** 그 암호화가 필요합니다. 토큰이
평문으로 오갑니다.

세 경우 모두 아래 설정은 같고, 주소만 다릅니다. `중앙주소` 자리에 랜 주소나
VPN 주소를 넣으십시오.

### 중앙 컴퓨터에서

PostgreSQL과 Qdrant를 다른 컴퓨터에서도 닿을 수 있게 열어 둡니다.
**모든 곳이 아니라 그 사설망 주소에만** 붙이십시오.

```bash
# .env
SAUCEDUST_NODE_ID=central
SAUCEDUST_NODE_ROLE=control
DATABASE_URL=postgres://sauce:비밀번호@localhost:5432/sauce
QDRANT_URL=http://localhost:6333
SAUCEDUST_CONTROL_BIND=중앙주소:8000
SAUCEDUST_CONTROL_TOKEN=길고-무작위인-문자열
```

토큰을 만듭니다. 16자 미만이면 시작을 거부합니다.

```bash
openssl rand -base64 24
```

Windows PowerShell에는 `openssl`이 없을 수 있습니다.

```powershell
[Convert]::ToBase64String((1..24|%{Get-Random -Max 256}))
```

```bash
./saucedust migrate
./saucedust model add ...        # 모델 등록은 중앙에서 한 번만
./saucedust control -no-crawl    # 받기만 하고 수집은 작업 노드에 맡깁니다
```

`-no-crawl`을 빼면 중앙도 함께 수집합니다. 중앙 컴퓨터가 넉넉하면 그렇게 하십시오.

### 각 작업 컴퓨터에서

```bash
# .env
SAUCEDUST_NODE_ID=worker-1          # 노드마다 다르게
SAUCEDUST_NODE_ROLE=worker
DATABASE_URL=postgres://sauce:비밀번호@중앙주소:5432/sauce
SAUCEDUST_CONTROL_URL=http://중앙주소:8000
SAUCEDUST_CONTROL_TOKEN=길고-무작위인-문자열
```

```bash
./saucedust setup
./saucedust worker -limit 20    # 먼저 스무 장으로 확인
./saucedust worker              # 확인됐으면 그냥 돌립니다
```

**챙길 것 네 가지입니다.**

- `SAUCEDUST_NODE_ID`는 노드마다 달라야 합니다. 같으면 서로를 덮어씁니다.
- `SAUCEDUST_CONTROL_TOKEN`은 모든 노드가 **같은 값**이어야 합니다.
- 모델은 중앙에서 한 번만 등록합니다. 작업 노드는 시작할 때 자기 워커가 올린
  모델을 그 기준과 대조하고, 다르면 아예 일을 받지 않습니다. 노드마다 다른
  모델을 쓰면 벡터를 비교할 수 없는데, 그건 오류 없이 검색 결과만 조용히
  이상해지는 형태라 미리 막습니다.
- 되돌아오는 주소가 아닌 곳에 열면서 토큰이 없거나 16자 미만이면 **시작을
  거부합니다.** 토큰이 없는 동안에는 인증 검사 자체가 없어서, 주소만 바꾸고
  토큰을 빠뜨리면 닿는 누구나 자료를 밀어 넣을 수 있기 때문입니다.

작업 노드가 PostgreSQL에도 붙는 이유는 구간을 빌리기 위해서입니다. 어느 ID
대역을 누가 맡을지 정하는 데만 씁니다. 이미지와 벡터는 HTTP로 갑니다.

### 잘 되고 있는지 보기

```bash
./saucedust node ls    # 어느 노드가 살아 있고 얼마나 처리하는지
./saucedust stats      # 얼마나 쌓였는지
```

중앙 노드의 `SAUCEDUST_CONTROL_BIND` 주소를 브라우저로 열면 대시보드가 나옵니다.

노드가 죽으면 30초마다 오던 신호가 끊기고, 90초 뒤 중앙이 그 노드의 구간을
회수해 다른 노드가 이어받습니다. 하던 일은 사라지지 않습니다.

### 실측으로 확인한 것

같은 컴퓨터에서 폴더를 나눠 중앙과 작업 노드를 따로 띄워 봤습니다.
2026년 8월 2일 측정입니다.

| 확인한 것 | 결과 |
|---|---|
| 작업 노드가 20장 수집 | 4.9초 |
| 중앙의 PostgreSQL | 이미지 20행, 모델별 벡터 20개씩 |
| 중앙의 Qdrant | 컬렉션마다 20점 |
| 중앙의 축소본 | 20장 |
| **작업 노드에 남은 이미지** | **0장** |
| 중앙에서 검색 | 망가뜨린 사본 10건 모두 1등 정답 |

## 디스크를 나눠 두기

쌓이는 440 GB 중 성격이 다른 두 덩어리가 섞여 있습니다.

| | 크기 | 어떻게 읽히나 | 어디에 |
|---|---|---|---|
| 축소본 | 300 GB | 한 번 쓰고 거의 안 읽음 | **HDD로 충분** |
| Qdrant, PostgreSQL | 140 GB | 검색할 때마다 임의 읽기 | **SSD여야 함** |

검색은 Qdrant에서 후보를 뽑고 PostgreSQL에서 메타데이터와 해시를 꺼내는
흐름이라 전부 임의 읽기입니다. HDD는 초당 100~200회쯤 하고 NVMe는 그
수백 배입니다. 적재는 순차 쓰기라 HDD로도 되지만 **검색이 못 쓰게 됩니다.**

`SAUCEDUST_THUMB_DIR`로 축소본만 떼어 냅니다.

```bash
# .env
SAUCEDUST_THUMB_DIR=/mnt/hdd/saucedust/thumbs
SAUCEDUST_DATA_DIR=/var/lib/saucedust
```

PostgreSQL과 Qdrant의 자료 위치는 각자의 설정에서 SSD로 잡으십시오.

## 메모리가 8 GB뿐일 때

Danbooru 전체 1,190만 장은 들어가지 않습니다. Qdrant가 붙들어야 하는 양이
장수에 정비례하고, **다 모으고 나서 줄일 수가 없습니다.** 담을 만큼만
모으도록 처음부터 막아야 합니다.

8 GB를 나누면 이렇습니다.

| | |
|---|---|
| Rocky 최소 설치 | 0.7 GB |
| Python 워커 (torch, 모델 2개) | 0.5 GB (실측 435 MB) |
| saucedust | 0.2 GB |
| PostgreSQL | 1.5 GB |
| Qdrant 기본 | 0.15 GB |
| **Qdrant 벡터에 남는 몫** | **약 4 GB** |

장당 상주 비용이 SigLIP 1.02 KB, CLIP 0.73 KB이므로 이렇게 됩니다.

| 쓰는 모델 | 담을 수 있는 장수 |
|---|---|
| SigLIP + CLIP | 약 240만 장 |
| **SigLIP 하나만** | **약 410만 장** |

### 모델을 하나로 줄이기

CLIP은 "비슷한 분위기의 그림"을 찾는 용도입니다. 출처를 찾는 것이 목적이면
빼도 됩니다. SigLIP 단독으로 10퍼센트만 남긴 조각에서 98.5퍼센트, 전체
99.3퍼센트였습니다. 빼면 메모리와 디스크가 42퍼센트 줄고 수집도 빨라집니다.

`models.json`에서 `clip-b32` 항목을 지우고 중앙에도 하나만 등록하십시오.

### 수집 범위를 막기

`CRAWL_BACKFILL_FLOOR`에 게시물 번호 하한을 둡니다. 백필은 최신부터 과거로
내려가므로, 하한을 두면 최근 구간만 남습니다.

```bash
# 최신 게시물이 11,907,690일 때 최근 400만 장만
CRAWL_BACKFILL_FLOOR=7900000
```

나중에 여유가 생기면 값을 낮추십시오. 이미 모은 것은 그대로 두고 그 아래로
이어서 내려갑니다.

**도움이 되지 않는 것도 적어 둡니다.** Qdrant의 `always_ram`을 꺼서 int8까지
디스크로 내려 봤는데 하한이 내려가기는커녕 조금 올라갔습니다. 손대지
마십시오.

## Proxmox 가상 기계에 올릴 때

GPU를 PCIe로 넘기면 계산 성능은 그대로입니다. 걸리는 곳은 따로 있습니다.

**GPU 패스스루.** BIOS에서 IOMMU(Intel VT-d, AMD-Vi)를 켜고 커널 인자에
`intel_iommu=on` 또는 `amd_iommu=on`을 넣습니다. GPU가 자기 IOMMU 그룹에
혼자 있어야 하고, 호스트가 그 카드를 잡지 않도록 `nouveau`와 `nvidia`를
막고 `vfio-pci`에 묶습니다. 게스트에서는 Secure Boot가 켜져 있으면 서명되지
않은 드라이버 모듈이 막히므로 끄거나 서명하십시오.

넘어갔는지는 워커를 띄우기 전에 확인하십시오.

```bash
nvidia-smi
./saucedust doctor
```

**디스크 패스스루는 두 가지가 다릅니다.** 컨트롤러를 통째로 넘기는 PCIe
패스스루는 게스트가 장치를 직접 가집니다. `qm set`으로 디스크 하나를
붙이는 것은 QEMU 블록 계층을 지나므로 같지 않습니다. 후자를 쓴다면
**캐시 방식을 반드시 `none`으로** 두십시오. `writeback`은 빠른 대신
호스트가 갑자기 꺼졌을 때 PostgreSQL이 지켰다고 믿은 쓰기가 사라집니다.

```bash
qm set 100 -scsi1 /dev/disk/by-id/ata-... ,cache=none
```

**메모리는 32 GB면 됩니다.** 1,000만 장 기준 Qdrant 하한이 17 GB이고
여유를 봐도 25 GB입니다. 자료 전체를 메모리에 올릴 필요가 없습니다.
더 얹어도 검색이 빨라지지 않는 것을 재서 확인했습니다
([README](README.md#메모리는-디스크의-3분의-1이면-됩니다)).

풍선(ballooning)은 꺼 두십시오. Qdrant는 상한 아래로 내려가면 뜨지 않고
그냥 죽습니다. 회수해 갈 여지를 두면 안 됩니다.

## 메시 VPN으로 묶기

컴퓨터가 여러 집이나 사무실에 흩어져 있을 때 쓰는 방법입니다. 모든 노드가
사설망 하나에 들어오고, 그 안에서만 서로 보입니다.

**이 방식을 권하는 이유:**

- **saucedust는 TLS를 하지 않습니다.** 토큰과 자료가 평문으로 오갑니다.
  VPN이 그 구간을 대신 암호화합니다.
- 초대한 사람만 들어옵니다. 나가면 바로 끊깁니다.
- 공유기 포트 포워딩도, 공인 IP도, 도메인도, 인증서도 필요 없습니다.
- PostgreSQL을 인터넷에 노출하지 않아도 됩니다. 이게 제일 큽니다.

Tailscale, Netbird, ZeroTier 중 아무거나 됩니다. 아래는 Tailscale 기준이고
나머지도 개념은 같습니다. 남의 서버를 거치기 싫으면 Headscale로 통제 서버를
직접 돌리거나 WireGuard를 손으로 설정해도 됩니다.

### 1. 모든 컴퓨터를 넣습니다

```bash
# macOS
brew install tailscale && sudo tailscale up

# Linux
curl -fsSL https://tailscale.com/install.sh | sh && sudo tailscale up

# Windows
winget install tailscale.tailscale
```

브라우저가 열리며 로그인하라고 합니다. 끝나면 컴퓨터마다 `100.`으로 시작하는
주소가 생깁니다. 이 주소는 그 사설망 안에서만 통합니다.

```bash
tailscale ip -4        # 이 컴퓨터의 주소
tailscale status       # 들어와 있는 컴퓨터 목록
```

### 2. 사람을 초대합니다

관리 화면(admin console)의 Users에서 이메일로 부릅니다. 받은 사람이 수락하면
그 사람 컴퓨터도 같은 사설망에 들어옵니다.

컴퓨터 한 대만 보여 주고 싶으면 사람을 부르는 대신 **그 기기만 공유**하십시오.
Machines 목록에서 중앙 노드를 골라 Share를 누르면, 상대는 그 한 대에만 닿고
다른 노드는 보지 못합니다. 검색만 하는 사람에게는 이쪽이 맞습니다.

### 3. 중앙 노드를 그 주소에만 붙입니다

```bash
tailscale ip -4
# 100.101.102.103
```

```bash
# 중앙의 .env
SAUCEDUST_CONTROL_BIND=100.101.102.103:8000
SAUCEDUST_CONTROL_TOKEN=길고-무작위인-문자열
```

```bash
# 각 작업 노드의 .env
SAUCEDUST_CONTROL_URL=http://100.101.102.103:8000
DATABASE_URL=postgres://sauce:비밀번호@100.101.102.103:5432/sauce
SAUCEDUST_CONTROL_TOKEN=길고-무작위인-문자열
```

`0.0.0.0`이 아니라 **VPN 주소를 그대로** 적는 것이 핵심입니다. `0.0.0.0`으로
두면 카페 와이파이에 붙었을 때 옆자리에서도 닿습니다.

`tailscale status`에 나오는 이름을 주소 대신 써도 됩니다. 다만
`SAUCEDUST_CONTROL_BIND`에는 실제 IP를 적으십시오. 이름은 붙일 때가 아니라
찾을 때 쓰는 것입니다.

### 4. PostgreSQL도 그 주소에만 붙입니다

작업 노드가 구간을 빌리려고 PostgreSQL에 직접 붙기 때문에 이것도 열어야
합니다. **여기가 가장 위험한 지점입니다.** 인터넷에 열린 PostgreSQL은
비밀번호가 있어도 계속 두들겨 맞습니다.

`postgresql.conf`:

```
listen_addresses = 'localhost,100.101.102.103'
```

`pg_hba.conf` — 그 사설망 대역에서 오는 것만, 암호화된 방식으로 받습니다.

```
host    sauce    sauce    100.64.0.0/10    scram-sha-256
```

Docker로 돌린다면 주소를 묶어서 띄웁니다.

```bash
docker run -d --name sauce-pg \
  -p 100.101.102.103:5432:5432 -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=sauce -e POSTGRES_PASSWORD=비밀번호 \
  -e POSTGRES_DB=sauce postgres:16
```

`-p 5432:5432`라고만 쓰면 모든 곳에 열립니다. **주소를 앞에 붙이십시오.**

주소를 지정하는 방식에는 대가가 하나 있습니다. VPN이 올라오기 전에
PostgreSQL이 먼저 뜨면 그 주소가 아직 없어서 **시작에 실패합니다.** 재부팅
뒤에 PostgreSQL이 죽어 있다면 대개 이것이고, 다시 띄우면 됩니다. 서비스로
등록할 때는 VPN 다음에 오도록 순서를 잡으십시오.

Qdrant는 중앙 노드만 쓰므로 `127.0.0.1:6333`에 그대로 두십시오. 작업 노드는
Qdrant에 붙지 않습니다.

### 5. 확인합니다

작업 노드에서:

```bash
tailscale ping 중앙노드이름
curl http://100.101.102.103:8000/health
```

바깥 회선(휴대폰 테더링 등)에서 같은 주소를 열어 보십시오. **닿으면 안
됩니다.** 닿는다면 `0.0.0.0`으로 붙어 있는 것입니다.

### 대시보드를 브라우저로 볼 때

`http://100.101.102.103:8000`을 열면 됩니다. 처음에 토큰을 한 번 넣으면
브라우저가 기억합니다.

화면 자체는 인증 없이 나옵니다. 자료를 부르는 순간 토큰을 검사하므로 토큰이
없으면 빈 화면입니다. `/health`와 `/ready`도 인증 없이 열려 있습니다. 부품이
살아 있는지만 알려 주고, 감시 도구가 봐야 하므로 그렇게 두었습니다.

주소 대신 이름으로 열고 싶고 브라우저 경고도 없애고 싶으면 `tailscale serve`가
사설망 안에서만 통하는 HTTPS 이름을 만들어 줍니다. 명령 형태가 판마다 다르니
`tailscale serve --help`를 보십시오. 통신은 이미 VPN이 암호화하므로 이건
편의를 위한 것입니다.

**`tailscale funnel`은 쓰지 마십시오.** 그건 인터넷 전체에 여는 기능이라
지금 하려는 것의 정반대입니다.

### 랜 안에서만 쓸 때

컴퓨터가 전부 한 건물에 있으면 VPN 없이도 됩니다.

```bash
SAUCEDUST_CONTROL_BIND=192.168.0.10:8000    # 랜 주소를 그대로
```

공유기에서 포트 포워딩을 **하지 마십시오.** 하는 순간 인터넷에 열립니다.
같은 랜에 있는 다른 사람은 다 닿으므로 토큰은 여전히 필요합니다.

## 준비 (저장소에서 직접 빌드할 때)

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
