# saucedust

개인 서버용 ACG 이미지 역검색 MVP입니다. Rust API 서버와 Danbooru 인덱서, Python OpenCLIP 임베딩 워커, PostgreSQL, Qdrant로 구성됩니다.

## 범위

- Danbooru 이미지 1,000장 내외 인덱싱
- 업로드 이미지 기준 Qdrant cosine Top 5 검색
- 원본 이미지 영구 저장 없음
- PostgreSQL에는 메타데이터, pHash, dHash 저장
- Qdrant에는 512차원 CLIP 벡터와 검색 payload 저장
- Telegram Bot은 이미지 업로드 검색과 inline button 응답을 지원

## 요구 사항

- Rust stable
- Python 3.12
- Homebrew
- PostgreSQL 16 Homebrew package
- Git
- Apple Silicon에서는 Python 워커가 MPS를 자동 감지하며, 불가능하면 CPU로 실행됩니다.

## 환경 설정

```bash
cp .env.example .env
```

주요 기본값:

```bash
DATABASE_URL=postgres://sauce:saucepass@localhost:5432/sauce
POSTGRES_MAX_CONNECTIONS=
POSTGRES_ACQUIRE_TIMEOUT_SECS=120
QDRANT_URL=http://localhost:6333
EMBEDDING_WORKER_URL=http://localhost:8100
EMBEDDING_BATCH_SIZE=16
EMBEDDING_BATCH_TIMEOUT_MS=50
SAUCE_API_BIND=0.0.0.0:8000
SAUCE_API_URL=http://localhost:8000
SAUCEDUST_PLAIN_LOGS=0
TELEGRAM_BOT_TOKEN=
TELEGRAM_POLL_TIMEOUT_SECS=30
SAUCEDUST_OUTBOUND_PROXY=
SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE=1
SAUCEDUST_VPN_AUTOSTART=1
SAUCEDUST_VPN_DATA_DIR=
EXPRESSVPN_OVPN_DIR=
EXPRESSVPN_OVPN_PATH=
EXPRESSVPN_USERNAME=
EXPRESSVPN_PASSWORD=
EXPRESSVPN_PROXY_PORT=1080
SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR=1
SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR=1
SAUCEDUST_VPN_ROTATE_COOLDOWN_SECS=10
SAUCEDUST_VPN_ROTATE_ATTEMPTS=3
SAUCEDUST_VPN_ROTATE_STATE_PATH=
SAUCEDUST_VPN_HEALTHCHECK_URL=
SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS=3600
INDEX_LIMIT=1000
INDEX_TAGS=rating:g
VECTOR_SIZE=512
REQUEST_TIMEOUT_SECS=30
INDEX_BATCH_PREP_TIMEOUT_SECS=120
INDEX_DATABASE_OP_TIMEOUT_SECS=90
INDEX_EMBEDDING_OP_TIMEOUT_SECS=120
SAUCEDUST_POLL_SECS=15
CRAWL_PIPELINE_CONCURRENCY=2
CRAWL_EMBEDDING_CONCURRENCY=
CRAWL_DATABASE_CONCURRENCY=
CRAWL_BACKFILL_WORKERS=2
CRAWL_BACKFILL_RANGE_SIZE=10000
CRAWL_BACKFILL_IDLE_SECS=30
```

`SAUCEDUST_POLL_SECS`는 최신 post 재확인 주기이고, `CRAWL_BACKFILL_IDLE_SECS`는 과거 backfill worker가 할당 가능한 구간을 못 받았을 때 다시 확인하기 전 쉬는 시간입니다. `CRAWL_PIPELINE_CONCURRENCY`는 연속 크롤링 전체의 이미지 다운로드/해시/임베딩/DB 저장 전역 동시 처리 상한입니다. `CRAWL_EMBEDDING_CONCURRENCY`와 `CRAWL_DATABASE_CONCURRENCY`를 비워두면 `CRAWL_PIPELINE_CONCURRENCY`와 같은 값으로 자동 동기화됩니다. `CRAWL_DATABASE_CONCURRENCY`는 PostgreSQL pool 보호를 위해 최대 64로 제한됩니다. 특정 병목이 반복될 때만 두 값을 따로 낮추십시오. Danbooru 목록 조회와 backfill range lease 할당도 별도 전역 제한을 통과하므로 `CRAWL_BACKFILL_WORKERS`를 1000 이상으로 늘려도 같은 ID 구간을 중복 처리하지 않습니다. `INDEX_BATCH_PREP_TIMEOUT_SECS`, `INDEX_DATABASE_OP_TIMEOUT_SECS`, `INDEX_EMBEDDING_OP_TIMEOUT_SECS`는 실제 DB/Qdrant/embedding 요청이 실행된 뒤 멈추는 경우를 실패 큐로 넘겨 전체 인덱싱 정지를 막는 안전장치입니다. 파이프라인, 임베딩, DB 슬롯 대기는 대량 worker 환경에서 정상적인 큐잉이므로 timeout을 두지 않습니다. `POSTGRES_MAX_CONNECTIONS`를 비워두면 설정값 기준으로 자동 산정하고, DB 연결 대기는 최대 `POSTGRES_ACQUIRE_TIMEOUT_SECS` 동안 유지됩니다. 오래된 `.env`의 `CRAWL_CONCURRENCY` 값은 자동 동기화 때 `CRAWL_PIPELINE_CONCURRENCY`로 옮겨집니다.

foreground 실행은 TUI 대시보드를 사용합니다. 터미널 호환 문제가 있거나 기존 줄 단위 로그가 필요하면 `SAUCEDUST_PLAIN_LOGS=1`로 실행하십시오. 기본 foreground/TUI 실행과 `./saucedust -d` 백그라운드 실행 모두 `logs/saucedust_YYYY-MM-DD_HH-MM-SS_0001.log` 형식의 실행별 로그에 자식 프로세스 로그, 종료 상태, panic, 최상위 오류를 기록합니다. `logs/saucedust.latest.log`는 최신 실행 로그를 가리킵니다.

## ExpressVPN OpenVPN 프록시

ExpressVPN Manual Configuration의 `.ovpn`, username, password를 `.env`에 넣으면 `saucedust`가 macOS 전체 네트워크를 바꾸지 않고 전용 VPN 프록시 VM을 자동 구성할 수 있습니다. 내부적으로 Alpine 기반 Lima VM을 만들고, VM 안에서 OpenVPN과 HTTP proxy를 실행합니다. `saucedust`의 Danbooru API, 이미지 다운로드, Telegram 외부 요청만 `SAUCEDUST_OUTBOUND_PROXY`를 통해 나갑니다.

여러 ExpressVPN 서버를 자동 전환하려면 디렉터리 하나에 `.ovpn` 파일들을 넣고 `EXPRESSVPN_OVPN_DIR`만 지정합니다. 파일 이름은 상관없고 확장자가 `.ovpn`이면 자동 발견됩니다. `EXPRESSVPN_OVPN_DIR`가 비어 있으면 `EXPRESSVPN_OVPN_PATH` 단일 파일을 사용합니다.

```bash
EXPRESSVPN_OVPN_DIR=/Users/example/Downloads/expressvpn-ovpn
EXPRESSVPN_OVPN_PATH=
EXPRESSVPN_USERNAME=manual-config-username
EXPRESSVPN_PASSWORD=manual-config-password
EXPRESSVPN_PROXY_PORT=1080
SAUCEDUST_OUTBOUND_PROXY=http://127.0.0.1:1080
SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE=1
SAUCEDUST_VPN_AUTOSTART=1
SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR=1
SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR=1
SAUCEDUST_VPN_ROTATE_COOLDOWN_SECS=10
SAUCEDUST_VPN_ROTATE_ATTEMPTS=3
SAUCEDUST_VPN_HEALTHCHECK_URL=
SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS=3600
```

기본 구성에서는 `EXPRESSVPN_OVPN_DIR`, `EXPRESSVPN_USERNAME`, `EXPRESSVPN_PASSWORD`를 입력하면 됩니다. `.ovpn`을 하나만 쓸 경우에는 `EXPRESSVPN_OVPN_PATH`를 입력합니다.

초기 구성:

```bash
./saucedust vpn setup
```

시작과 테스트:

```bash
./saucedust vpn start
./saucedust vpn rotate
./saucedust vpn test
./saucedust vpn status
```

중지:

```bash
./saucedust vpn stop
```

`SAUCEDUST_VPN_AUTOSTART=1`이면 `./saucedust` 또는 `./saucedust -d` 실행 시 DB/API/크롤러를 시작하기 전에 VPN 프록시를 먼저 준비합니다. `SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE=1`이면 VPN 프록시 시작, 전환, 다운로드 요청이 실패했을 때 `SAUCEDUST_OUTBOUND_PROXY`를 제거하고 현재 네트워크로 직접 재시도합니다. 현재 네트워크로 절대 요청하지 않으려면 이 값을 `0`으로 바꾸십시오. OpenVPN 인증 파일은 `SAUCEDUST_VPN_DATA_DIR` 또는 `SAUCE_ENGINE_DATA_DIR/vpn` 아래 `auth.txt`에 `0600` 권한으로 저장됩니다.

`EXPRESSVPN_PROXY_PORT` 기본값은 `1080`입니다. 해당 포트가 이미 사용 중이면 `saucedust`가 이번 실행에서 사용할 빈 localhost 포트를 자동으로 고르고, 자식 프로세스에는 새 `SAUCEDUST_OUTBOUND_PROXY` 값을 전달합니다.

이미지 CDN 다운로드에서 `error sending request`, `Proxy CONNECT aborted`, body decode 오류, timeout 같은 네트워크성 문제가 발생하면 인덱서가 `SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR=1` 설정을 보고 `./saucedust vpn rotate`를 호출합니다. 이때 `EXPRESSVPN_OVPN_DIR` 안의 다음 `.ovpn`으로 OpenVPN을 다시 연결하고 같은 localhost proxy 포트로 다운로드를 재시도합니다. 여러 worker가 동시에 실패해도 전환 명령은 하나씩만 실행됩니다. 최근 VPN 전환 이후 만들어진 오래된 HTTP client는 먼저 버리고 새 연결로 재시도하며, 그래도 실패하면 `SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR=1` 설정에 따라 쿨다운보다 다른 리전 전환을 우선합니다. 모든 회전이 실패하고 `SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE=1`이면 직접망으로 전환해 같은 이미지를 한 번 더 재시도합니다. `./saucedust vpn rotate`는 단순 IP 확인뿐 아니라 실패한 CDN URL 또는 `SAUCEDUST_VPN_HEALTHCHECK_URL`, 기본 Danbooru API URL까지 proxy로 검증한 뒤 성공한 리전만 채택합니다. `.ovpn`별 OK/FAIL 상태는 `SAUCEDUST_VPN_DATA_DIR` 아래 `ovpn-health.json`에 기록되며, `SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS=3600` 기준으로 한 시간마다 다시 점검합니다. 실행 중 네트워크 오류로 회전이 발생하면 현재 active `.ovpn`은 즉시 FAIL로 기록되고, 이후 스위칭은 OK 기록이 있는 후보를 먼저 사용합니다.

바이너리를 업데이트한 뒤 새 환경값이 추가되거나 더 이상 쓰지 않는 환경값이 제거되면 `.env`가 자동 동기화됩니다. 이 경우 서비스는 바로 시작하지 않고 중단되며, 아래 명령으로 값을 점검한 뒤 다시 실행합니다.

```bash
./saucedust env show --show-secrets
./saucedust env setup --advanced
```

## 단일 런처 실행

이 프로젝트는 macOS 네이티브 프로세스로 실행합니다. `saucedust` 런처가 PostgreSQL 데이터 디렉터리 생성, Qdrant 빌드/실행, embedding-worker, API, 인덱서를 관리합니다.

릴리스 번들 생성:

```bash
./scripts/build_release_bundle.sh
```

생성된 `dist/saucedust-release` 디렉터리를 Mac mini로 옮긴 뒤 아래 순서로 실행합니다.

```bash
cd saucedust-release
./saucedust
```

인자 없이 `./saucedust`를 실행하면 자동 실행 플로우가 시작됩니다. 시스템 의존성이 없으면 자동 설치를 시도하고, `.env`가 있으면 DB, Qdrant, embedding worker, API, continuous crawler, Telegram 봇을 바로 시작합니다.

백그라운드 전용으로 시작하려면 `-d`를 사용합니다. 이 경우 `run/saucedust.pid`와 실행별 로그 파일이 생성되고 터미널은 바로 반환됩니다. 일반 foreground 실행에서도 같은 규칙으로 로그 파일이 생성됩니다.

```bash
./saucedust -d
```

자동 설치 중 실행되는 `brew install`, `pip install`, Qdrant 빌드 같은 외부 명령은 saucedust가 감싸서 표시합니다. 진행 중에는 진행바와 경과 시간이 표시되고, stdout/stderr 로그는 줄 단위로 정리되어 출력됩니다. 실패하면 마지막 로그가 오류 박스로 묶여 표시됩니다.

macOS Gatekeeper/quarantine 때문에 실행이 막히면 같은 폴더의 실행 스크립트를 사용하십시오. 이 스크립트는 실행 권한, quarantine 제거, ad-hoc signing을 처리한 뒤 `saucedust`를 실행합니다.

```bash
./run_saucedust.sh
```

권한 보정 스크립트를 통해 백그라운드로 시작하려면 아래처럼 실행합니다.

```bash
./run_saucedust.sh -d
```

스크립트 실행 권한까지 사라진 상태라면 아래처럼 실행할 수 있습니다.

```bash
sh run_saucedust.sh
```

`.env`가 없으면 자동 실행은 프로젝트를 시작하지 않고 설정 안내를 표시합니다. 먼저 대화형 설정을 실행하십시오.

```bash
./run_saucedust.sh env setup
```

기존 번호 선택 메뉴가 필요하면 아래 명령을 사용하십시오.

```bash
./run_saucedust.sh menu
```

백그라운드 시작 명령은 같은 `saucedust` 바이너리를 `serve` 모드로 재실행하고 `run/saucedust.pid`, `logs/saucedust_YYYY-MM-DD_HH-MM-SS_0001.log`를 생성합니다. 최신 로그는 `logs/saucedust.latest.log`로 확인할 수 있습니다.

재부팅 후에도 자동 실행되게 하려면 macOS `launchd` 서비스로 등록합니다.

메뉴에서 `launchd 서비스 관리`를 선택한 뒤 `서비스 설치 및 시작`을 고르면 됩니다. 테스트용으로 API만 실행하려면 `서비스 설치 및 시작 API만`을 선택하십시오.

릴리스 번들 내부 구조:

```text
saucedust-release/
├─ saucedust
└─ run_saucedust.sh
```

배포 시 필요한 핵심 실행 파일은 `saucedust` 하나입니다. `run_saucedust.sh`는 macOS 권한/격리 문제를 우회하기 위한 보조 실행 스크립트입니다. 실행 파일이 있는 디렉터리에는 `.env`, `.env.example`, `logs`, `run`만 생성될 수 있습니다. PostgreSQL, Qdrant, Qdrant 소스, Python worker와 venv는 기본적으로 `~/.saucedust` 또는 `SAUCE_ENGINE_DATA_DIR`로 지정한 외장 SSD 경로 아래에 저장됩니다.

처음 세팅:

```bash
./saucedust bootstrap
```

`bootstrap`은 `.env`가 없으면 내장된 기본값에서 생성하고, 실행 환경을 자동 감지합니다. PATH에 Homebrew/Rust/Python/PostgreSQL 경로가 빠져 있으면 런처가 자동 보정하고, Homebrew가 없으면 설치를 시도합니다. 이후 `git`, `rust`, `python@3.12`, `postgresql@16`을 Homebrew로 확인/설치하고, Qdrant 공식 소스를 받아 release binary를 빌드하며, 데이터 디렉터리 아래 Python worker venv를 만들고 Python 패키지를 설치합니다. 이미 세팅된 항목은 재사용합니다.

Xcode Command Line Tools가 없는 새 macOS에서는 설치 확인 창이 뜰 수 있습니다. 이 경우 설치를 완료한 뒤 아래 명령을 다시 실행하십시오.

```bash
./saucedust bootstrap
```

사전 점검:

```bash
./saucedust doctor
```

`doctor`는 Python, Homebrew, git, cargo를 확인합니다. `bootstrap`은 `doctor`보다 먼저 자동 셋업을 수행하므로, 새 Mac에서는 `bootstrap`을 먼저 실행하는 편이 편합니다.

환경값 입력/수정:

```bash
./saucedust env setup
```

`env setup`은 `.env`가 없으면 생성하고 주요 값을 순서대로 물어봅니다. 각 항목마다 역할, 필수 여부, 기본값을 그대로 써도 되는 조건, 예시를 표시합니다. Enter를 누르면 기존 값을 유지합니다. 추가 환경값까지 모두 입력하려면 `--advanced`를 붙이십시오.

```bash
./saucedust env setup --advanced
```

외장 SSD를 사용할 경우 `SAUCE_ENGINE_DATA_DIR`에 전체 데이터 루트 경로를 입력하면 됩니다.

```text
/Volumes/ExternalSSD/saucedust
```

PostgreSQL과 Qdrant를 서로 다른 위치에 둘 때만 `SAUCE_ENGINE_POSTGRES_DATA_DIR`, `SAUCE_ENGINE_QDRANT_STORAGE_DIR`를 따로 입력하십시오.

단일 값을 CLI에서 바로 바꾸려면 `env set`을 사용합니다.

```bash
./saucedust env set SAUCE_ENGINE_DATA_DIR /Volumes/ExternalSSD/saucedust
./saucedust env set TELEGRAM_BOT_TOKEN 123456:example
```

현재 설정 확인:

```bash
./saucedust env show
```

`TOKEN`, `PASS`, `PASSWORD`, `KEY`가 포함된 값은 기본적으로 마스킹됩니다. 실제 값을 확인해야 할 때만 `--show-secrets`를 사용하십시오.

DB만 기동:

```bash
./saucedust db-up
```

`db-up`은 PostgreSQL 네이티브 데이터 디렉터리만 기동합니다. Qdrant는 장시간 실행을 위해 `serve` 명령의 자식 프로세스로 관리됩니다.

전체 실행:

```bash
./saucedust serve --index-limit 200 --poll-secs 60
```

백필 worker 수와 worker별 ID range 크기를 조절할 수 있습니다. 같은 ID 구간을 중복 처리하지 않도록 PostgreSQL에서 range lease를 원자적으로 할당합니다.

```bash
./saucedust serve \
  --index-limit 200 \
  --poll-secs 60 \
  --backfill-workers 4 \
  --backfill-range-size 20000
```

`TELEGRAM_BOT_TOKEN`이 설정되어 있으면 `serve`가 Telegram 봇도 함께 실행합니다. 봇을 빼고 실행하려면 `--no-telegram`을 사용하십시오.

인덱서 없이 API와 embedding-worker만 실행:

```bash
./saucedust serve --no-indexer
```

종료는 `Ctrl-C`로 수행합니다. 런처는 자식 프로세스인 Qdrant, embedding-worker, sauce-api, sauce-indexer를 함께 종료합니다. PostgreSQL을 내리려면 아래 명령을 사용하십시오.

```bash
./saucedust db-down
```

외장 SSD를 데이터 경로로 쓰려면 `.env`에서 `SAUCE_ENGINE_DATA_DIR`를 지정하십시오. 절대경로, `~/...`, 프로젝트 기준 상대경로를 사용할 수 있습니다.

```bash
SAUCE_ENGINE_DATA_DIR=/Volumes/ExternalSSD/saucedust
./saucedust bootstrap
./saucedust serve
```

네이티브 모드에서 런처가 관리하는 데이터:

```text
$SAUCE_ENGINE_DATA_DIR/qdrant-src
$SAUCE_ENGINE_DATA_DIR/qdrant-storage
$SAUCE_ENGINE_DATA_DIR/postgres-data
$SAUCE_ENGINE_DATA_DIR/postgres.log
$SAUCE_ENGINE_DATA_DIR/runtime/python/embedding-worker
```

PostgreSQL과 Qdrant 저장소를 서로 다른 디스크에 둘 때는 `.env`에 아래 값을 따로 지정할 수 있습니다.

```bash
SAUCE_ENGINE_POSTGRES_DATA_DIR=/Volumes/ExternalSSD/saucedust/postgres-data
SAUCE_ENGINE_QDRANT_STORAGE_DIR=/Volumes/ExternalSSD/saucedust/qdrant-storage
```

PostgreSQL만 시작:

```bash
./saucedust db-up
```

PostgreSQL 종료:

```bash
./saucedust db-down
```

## Python embedding-worker 실행

```bash
cd python/embedding-worker
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8100
```

검증:

```bash
curl http://localhost:8100/health
curl -X POST http://localhost:8100/embed -F "file=@/path/to/image.jpg"
```

embedding-worker는 `/embed` 요청을 내부 큐에서 짧게 모아 micro-batch로 OpenCLIP에 전달합니다. Rust 인덱서가 여러 이미지를 동시에 요청하면 worker가 최대 `EMBEDDING_BATCH_SIZE`개까지 묶고, `EMBEDDING_BATCH_TIMEOUT_MS`만큼 기다린 뒤 batch 추론합니다. M4 16GB 인덱싱 기준 시작값은 `16`, `50ms`입니다. 검색 API 지연을 더 낮추고 싶으면 `8`, `20ms`로 낮출 수 있습니다.

## Rust 검증

```bash
cargo fmt --check
cargo clippy --workspace -- -D warnings
cargo test --workspace
```

## 인덱서

Danbooru 메타데이터만 확인:

```bash
./saucedust index danbooru --limit 10 --dry-run
```

Danbooru 수집은 페이지 번호를 기준으로 삼지 않습니다. 첫 요청에서 최신 결과 묶음을 받은 뒤, 다음 요청부터는 직전 묶음의 가장 작은 post id보다 더 작은 `id:<...` 조건을 붙여 최신순에서 과거순으로 내려갑니다. 크롤링 중 새 post가 올라와도 진행 중인 과거 방향 백필이 페이지 밀림으로 흔들리지 않도록 하기 위한 방식입니다.

Danbooru 검색 결과를 정규화 JSON으로 저장:

```bash
./saucedust normalize danbooru \
  --tags "mari_(blue_archive) rating:safe" \
  --limit 200 \
  --out ./danbooru_records.json
```

연결 상태 확인:

```bash
./saucedust verify
```

저장소 통계 확인:

```bash
./saucedust stats
```

이 명령은 원본 이미지 파일 수가 아니라 정상 인덱싱되어 `images` 테이블에 저장된 이미지 수, Qdrant vector point 수, PostgreSQL 논리 DB 용량, PostgreSQL/Qdrant 데이터 디렉터리의 실제 디스크 사용량을 출력합니다.

특정 Danbooru post가 실제로 인덱싱됐는지 확인:

```bash
./saucedust index check-post --post-id 10797049
curl http://localhost:8000/images/source/danbooru/10797049
```

검색 결과가 예상 post와 다르면 먼저 해당 post가 인덱싱됐는지 확인하십시오. `indexed=false`이면 아직 검색 대상에 없어서 Qdrant가 이미 저장된 다른 유사 이미지를 반환합니다.

100개 인덱싱:

```bash
./saucedust index danbooru --limit 100
```

장시간 실행:

```bash
./saucedust index danbooru --continuous --limit 200 --poll-secs 15
```

백필 worker를 직접 지정:

```bash
./saucedust index danbooru \
  --continuous \
  --limit 200 \
  --poll-secs 15 \
  --backfill-workers 4 \
  --backfill-range-size 20000
```

`--continuous`는 태그 조건별 수집 상태를 PostgreSQL `crawl_states`에 저장합니다. 최신 catch-up 워커는 마지막 `high_watermark_id`보다 큰 새 post 구간을 따라잡습니다. 과거 backfill 워커는 여러 개를 동시에 실행할 수 있고, 각 worker는 PostgreSQL `crawl_ranges`에 기록되는 고유 ID range lease를 할당받아 처리합니다. 따라서 worker 수를 늘려도 같은 ID 구간을 동시에 처리하지 않습니다. 실제 다운로드/해시/임베딩/DB 저장은 `CRAWL_PIPELINE_CONCURRENCY` 전역 limiter 안에서만 실행됩니다. 임베딩 호출과 PostgreSQL/Qdrant 저장은 기본적으로 pipeline concurrency와 동기화되며, `CRAWL_EMBEDDING_CONCURRENCY`, `CRAWL_DATABASE_CONCURRENCY` 값을 따로 넣은 경우에만 별도 상한을 사용합니다. Danbooru 목록 조회는 최대 `min(CRAWL_PIPELINE_CONCURRENCY, 16)`개, range lease 할당은 최대 `min(max(CRAWL_PIPELINE_CONCURRENCY, 4), 64)`개만 동시에 실행됩니다. 각 묶음은 PostgreSQL에 이미 존재하는 `source_post_id`를 한 번에 확인해서, 이미 인덱싱된 post는 이미지 다운로드와 임베딩 생성을 건너뜁니다.

1,000개 인덱싱:

```bash
./saucedust
```

메뉴에서 `Danbooru 인덱싱 실행`을 선택하고 목표 수에 `1000`을 입력하십시오. CLI로 바로 실행하려면 아래 명령을 사용하십시오.

```bash
./saucedust index danbooru --limit 1000
```

개발 데이터 초기화:

```bash
CONFIRM_RESET=1 ./saucedust reset-dev
```

## API 서버

```bash
./saucedust serve --no-indexer --no-telegram
```

엔드포인트:

- `GET /health`
- `GET /ready`
- `POST /search`
- `GET /images/:id`

검색:

```bash
curl -X POST http://localhost:8000/search \
  -F "file=@/path/to/query.jpg"
```

응답:

```json
{
  "results": [
    {
      "score": 0.98,
      "source_site": "danbooru",
      "source_post_id": "123",
      "source_url": "...",
      "canonical_url": "https://danbooru.donmai.us/posts/123",
      "preview_url": "...",
      "rating": "g",
      "tags": ["..."]
    }
  ]
}
```

## Telegram 봇

BotFather에서 발급받은 토큰을 `.env`에 설정하십시오.

```bash
TELEGRAM_BOT_TOKEN=...
SAUCE_API_URL=http://localhost:8000
```

전체 런처로 실행하면 토큰이 있을 때 봇이 자동으로 같이 실행됩니다.

```bash
./saucedust serve
```

API와 봇만 실행하려면 인덱서를 끄고 실행하십시오.

```bash
./saucedust serve --no-indexer
```

Telegram 대화방에 이미지를 사진 또는 이미지 파일로 보내면 봇은 `/search` API에 이미지를 전달하고 Top 5 결과를 응답합니다. 본문에는 점수, artist tag, rating, 주요 tag를 표시하고, Danbooru post URL, Danbooru `source` 필드에 있던 Pixiv/X 등 원본 출처 URL, preview URL은 inline button으로 표시합니다.

## 문제 해결

- `/ready`에서 `embedding_worker=false`이면 `http://localhost:8100/health`를 먼저 확인하십시오.
- OpenCLIP 첫 실행은 모델 다운로드 때문에 오래 걸릴 수 있습니다.
- Danbooru 요청 실패가 반복되면 `CRAWL_DELAY_MS`를 늘리십시오.
- Qdrant collection이 없으면 API와 인덱서가 자동 생성합니다.
- `reset-dev`는 `CONFIRM_RESET=1`이 없으면 실행되지 않습니다.
- 인덱서는 `large_file_url`, `file_url`, `preview_file_url` 순서로 다운로드 URL을 선택합니다.
- `.jpg`, `.jpeg`, `.png`, `.webp` 외 URL은 인덱싱 전에 제외됩니다.
- 32MiB 초과 다운로드와 64MP 초과 이미지는 안전하게 실패 처리됩니다.
- 검색 API는 업로드 이미지 SHA256 기준으로 query embedding을 캐시합니다.
- 검색 API는 embedding-worker 전송 전에 업로드 이미지를 최대 512px JPEG로 축소합니다.
- Danbooru의 `source` 필드는 `source_url`로 저장됩니다. Pixiv/X 링크가 Danbooru post에 있으면 검색 API와 Telegram 봇 응답에도 표시됩니다.
- Danbooru artist tag는 `artist_tags`로 별도 저장됩니다.
- `danbooru.donmai.us`에서 connection reset이 나면 ExpressVPN 프록시를 시작하고 `SAUCEDUST_OUTBOUND_PROXY=http://127.0.0.1:1080`을 사용하십시오.

## Next steps

- pHash/dHash 기반 재랭킹 추가
- Gelbooru, Yande.re, Konachan, Pixiv source 매핑 추가
- 썸네일 저장 옵션 추가
- Telegram Bot 권한 제한과 운영자 명령 추가
