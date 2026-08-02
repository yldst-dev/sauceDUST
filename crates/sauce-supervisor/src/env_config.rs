use std::collections::{BTreeMap, BTreeSet};
use std::io::{self, Write};
use std::path::{Path, PathBuf};

use crate::cli::EnvCommand;

const BASIC_KEYS: &[&str] = &[
    "DATABASE_URL",
    "POSTGRES_MAX_CONNECTIONS",
    "POSTGRES_ACQUIRE_TIMEOUT_SECS",
    "QDRANT_URL",
    "EMBEDDING_WORKER_URL",
    "EMBEDDING_BATCH_SIZE",
    "EMBEDDING_BATCH_TIMEOUT_MS",
    "SAUCE_API_BIND",
    "SAUCE_API_URL",
    "SAUCEDUST_PLAIN_LOGS",
    "TELEGRAM_BOT_TOKEN",
    "TELEGRAM_POLL_TIMEOUT_SECS",
    "SAUCEDUST_POLL_SECS",
    "SAUCEDUST_OUTBOUND_PROXY",
    "SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE",
    "SAUCEDUST_VPN_AUTOSTART",
    "SAUCEDUST_VPN_DATA_DIR",
    "EXPRESSVPN_OVPN_DIR",
    "EXPRESSVPN_OVPN_PATH",
    "EXPRESSVPN_USERNAME",
    "EXPRESSVPN_PASSWORD",
    "EXPRESSVPN_PROXY_PORT",
    "SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR",
    "SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR",
    "SAUCEDUST_VPN_ROTATE_COOLDOWN_SECS",
    "SAUCEDUST_VPN_ROTATE_ATTEMPTS",
    "SAUCEDUST_VPN_ROTATE_STATE_PATH",
    "SAUCEDUST_VPN_HEALTHCHECK_URL",
    "SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS",
    "SAUCE_ENGINE_DATA_DIR",
    "SAUCE_ENGINE_POSTGRES_DATA_DIR",
    "SAUCE_ENGINE_QDRANT_STORAGE_DIR",
    "DANBOORU_BASE_URL",
    "DANBOORU_CONNECT_BASE_URL",
    "DANBOORU_HOST_HEADER",
    "DANBOORU_USER_AGENT",
    "DANBOORU_LATEST_TAGS",
    "SAUCEDUST_DNS_RESOLVER",
    "INDEX_LIMIT",
    "INDEX_TAGS",
    "VECTOR_SIZE",
    "REQUEST_TIMEOUT_SECS",
    "INDEX_BATCH_PREP_TIMEOUT_SECS",
    "INDEX_DATABASE_OP_TIMEOUT_SECS",
    "INDEX_EMBEDDING_OP_TIMEOUT_SECS",
    "CRAWL_DELAY_MS",
    "CRAWL_PIPELINE_CONCURRENCY",
    "CRAWL_EMBEDDING_CONCURRENCY",
    "CRAWL_DATABASE_CONCURRENCY",
    "CRAWL_BACKFILL_WORKERS",
    "CRAWL_BACKFILL_ACTIVE_RANGES",
    "CRAWL_BACKFILL_RANGE_SIZE",
    "CRAWL_BACKFILL_IDLE_SECS",
    "SAUCEDUST_VERBOSE_INDEX_LOGS",
];

pub async fn run(command: EnvCommand) -> anyhow::Result<()> {
    match command {
        EnvCommand::Setup { advanced } => setup(advanced).await,
        EnvCommand::Set { key, value } => set(key, value).await,
        EnvCommand::Show { show_secrets } => show(show_secrets).await,
    }
}

#[derive(Debug, Clone, Copy, Default)]
pub struct EnvSyncReport {
    pub added: usize,
    pub removed: usize,
}

impl EnvSyncReport {
    pub fn changed(self) -> bool {
        self.added > 0 || self.removed > 0
    }
}

pub async fn sync_existing(root: &Path) -> anyhow::Result<EnvSyncReport> {
    crate::runtime_assets::ensure_env_example(root).await?;
    let env_path = root.join(".env");
    if !env_path.exists() {
        return Ok(EnvSyncReport::default());
    }
    let example_path = root.join(".env.example");
    let mut file = EnvFile::load(&env_path).await?;
    let example = EnvFile::load(&example_path).await?;
    let report = file.sync_with_defaults(&example);
    if report.changed() {
        file.write(&env_path).await?;
        crate::ui::info(&format!(
            ".env 동기화 완료: 추가 {}개, 제거 {}개",
            report.added, report.removed
        ));
    }
    Ok(report)
}

struct EnvHelp {
    role: &'static str,
    required: &'static str,
    default_condition: &'static str,
    example: Option<&'static str>,
}

async fn setup(advanced: bool) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure_env_example(&root).await?;
    let env_path = ensure_env_file(&root).await?;
    let example_path = root.join(".env.example");
    let mut file = EnvFile::load(&env_path).await?;
    let example = EnvFile::load(&example_path).await?;
    file.sync_with_defaults(&example);

    let keys = if advanced {
        file.keys.clone()
    } else {
        BASIC_KEYS.iter().map(|key| (*key).to_owned()).collect()
    };

    print_setup_intro(&env_path, advanced);

    for key in keys {
        print_key_help(&key);
        let current = file.values.get(&key).cloned().unwrap_or_default();
        let display = if is_secret_key(&key) && !current.is_empty() {
            mask_value(&current)
        } else {
            current.clone()
        };
        let prompt = if display.is_empty() {
            format!("{key}: ")
        } else {
            format!("{key} [{display}]: ")
        };
        let value = read_line(&prompt)?;
        if !value.trim().is_empty() {
            file.set(key, value.trim().to_owned());
        }
    }

    file.write(&env_path).await?;
    println!("updated {}", env_path.display());
    Ok(())
}

fn print_setup_intro(env_path: &Path, advanced: bool) {
    println!("configuring {}", env_path.display());
    println!("Enter를 누르면 현재 값을 유지합니다.");
    println!("값을 비우고 싶으면 env set KEY \"\" 형식으로 따로 실행하십시오.");
    println!();
    println!("외장 SSD 사용 가이드");
    println!(
        "  전체 데이터를 외장 SSD에 둘 경우 SAUCE_ENGINE_DATA_DIR에 디렉터리 경로를 입력하십시오."
    );
    println!("  예: /Volumes/ExternalSSD/saucedust");
    println!("  PostgreSQL과 Qdrant를 다른 위치에 두고 싶을 때만 개별 경로를 따로 입력하십시오.");
    println!("  예: SAUCE_ENGINE_POSTGRES_DATA_DIR=/Volumes/ExternalSSD/saucedust/postgres-data");
    println!("  예: SAUCE_ENGINE_QDRANT_STORAGE_DIR=/Volumes/ExternalSSD/saucedust/qdrant-storage");
    if !advanced {
        println!();
        println!("기본 설정 모드입니다. 추가 환경값까지 설정하려면 --advanced를 사용하십시오.");
    }
}

fn print_key_help(key: &str) {
    let help = env_help(key);
    println!();
    println!("{key}");
    println!("  역할: {}", help.role);
    println!("  필수 여부: {}", help.required);
    println!("  기본값 사용 조건: {}", help.default_condition);
    if let Some(example) = help.example {
        println!("  예시: {example}");
    }
}

fn env_help(key: &str) -> EnvHelp {
    match key {
        "DATABASE_URL" => EnvHelp {
            role: "API와 인덱서가 사용할 PostgreSQL 접속 문자열입니다.",
            required: "필수입니다.",
            default_condition: "런처가 로컬 PostgreSQL을 관리하는 기본 구성이라면 그대로 사용 가능합니다.",
            example: Some("postgres://sauce:saucepass@localhost:5432/sauce"),
        },
        "POSTGRES_MAX_CONNECTIONS" => EnvHelp {
            role: "Rust 프로세스 안에서 사용할 PostgreSQL connection pool 최대 크기입니다.",
            required: "선택입니다.",
            default_condition: "비워두면 CRAWL_PIPELINE_CONCURRENCY 기준으로 자동 산정합니다.",
            example: Some("80"),
        },
        "POSTGRES_ACQUIRE_TIMEOUT_SECS" => EnvHelp {
            role: "DB 연결 풀이 꽉 찼을 때 연결을 기다릴 최대 시간입니다.",
            required: "선택입니다.",
            default_condition: "고동시성 크롤링에서는 기본값 120초를 권장합니다.",
            example: Some("120"),
        },
        "QDRANT_URL" => EnvHelp {
            role: "Qdrant HTTP API 주소입니다.",
            required: "필수입니다.",
            default_condition: "런처가 로컬 Qdrant를 띄우는 기본 구성이라면 그대로 사용 가능합니다.",
            example: Some("http://localhost:6333"),
        },
        "EMBEDDING_WORKER_URL" => EnvHelp {
            role: "Rust API와 인덱서가 Python OpenCLIP worker에 요청할 주소입니다.",
            required: "필수입니다.",
            default_condition: "런처가 로컬 embedding-worker를 띄우는 기본 구성이라면 그대로 사용 가능합니다.",
            example: Some("http://localhost:8100"),
        },
        "EMBEDDING_BATCH_SIZE" => EnvHelp {
            role: "embedding-worker가 한 번에 묶어 처리할 최대 이미지 수입니다.",
            required: "선택입니다.",
            default_condition: "M4 16GB 인덱싱 처리량 기준으로 16을 권장합니다. 메모리가 부족하면 8로 낮추십시오.",
            example: Some("16"),
        },
        "EMBEDDING_BATCH_TIMEOUT_MS" => EnvHelp {
            role: "batch를 만들기 위해 worker가 짧게 기다리는 시간입니다.",
            required: "선택입니다.",
            default_condition: "인덱싱 처리량 기준으로 50ms를 권장합니다. 검색 응답 지연을 낮추려면 20ms로 낮추십시오.",
            example: Some("50"),
        },
        "SAUCE_API_BIND" => EnvHelp {
            role: "검색 API 서버가 listen할 주소입니다.",
            required: "필수입니다.",
            default_condition: "같은 기기 또는 LAN에서 접근하려면 기본값을 사용할 수 있습니다.",
            example: Some("0.0.0.0:8000"),
        },
        "SAUCE_API_URL" => EnvHelp {
            role: "Telegram 봇이 호출할 검색 API 주소입니다.",
            required: "Telegram 봇을 쓰면 필수입니다.",
            default_condition: "봇과 API가 같은 Mac에서 실행되면 기본값을 사용할 수 있습니다.",
            example: Some("http://localhost:8000"),
        },
        "SAUCEDUST_PLAIN_LOGS" => EnvHelp {
            role: "foreground 실행에서 TUI 대신 기존 줄 단위 로그 출력을 사용할지 정합니다.",
            required: "선택입니다.",
            default_condition: "기본값 0을 권장합니다. 터미널 호환 문제가 있으면 1로 바꾸십시오.",
            example: Some("0"),
        },
        "TELEGRAM_BOT_TOKEN" => EnvHelp {
            role: "Telegram BotFather에서 받은 봇 토큰입니다.",
            required: "Telegram 봇을 실행하려면 필수입니다. 비워두면 런처가 봇을 실행하지 않습니다.",
            default_condition: "Telegram 연동을 나중에 테스트할 경우 비워둬도 됩니다.",
            example: Some("1234567890:AA..."),
        },
        "TELEGRAM_POLL_TIMEOUT_SECS" => EnvHelp {
            role: "Telegram long polling 요청 대기 시간입니다.",
            required: "선택입니다.",
            default_condition: "기본값 30초를 권장합니다.",
            example: Some("30"),
        },
        "SAUCEDUST_POLL_SECS" => EnvHelp {
            role: "연속 인덱싱에서 최신 post 확인과 일반 대기 재확인에 사용하는 기본 주기입니다.",
            required: "선택입니다.",
            default_condition: "기본값 15초를 권장합니다. Danbooru 요청을 더 줄이려면 30~60으로 늘리십시오.",
            example: Some("15"),
        },
        "SAUCEDUST_OUTBOUND_PROXY" => EnvHelp {
            role: "saucedust의 외부 HTTP 요청이 사용할 proxy 주소입니다.",
            required: "VPN 프록시를 쓰려면 필요합니다.",
            default_condition: "SAUCEDUST_VPN_AUTOSTART=1이면 런처가 현재 실행 프로세스에 자동 지정합니다.",
            example: Some("http://127.0.0.1:1080"),
        },
        "SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE" => EnvHelp {
            role: "VPN 프록시 시작, 전환, 다운로드 요청이 실패했을 때 현재 네트워크 직접 요청으로 전환할지 정합니다.",
            required: "선택입니다.",
            default_condition: "기본값 1입니다. 현재 네트워크로 절대 요청하지 않으려면 0으로 바꾸십시오.",
            example: Some("1"),
        },
        "SAUCEDUST_VPN_AUTOSTART" => EnvHelp {
            role: "saucedust 실행 시 ExpressVPN OpenVPN 프록시를 자동 시작할지 정합니다.",
            required: "Danbooru 크롤링을 현재 네트워크로 직접 보내지 않으려면 필수입니다.",
            default_condition: "기본값 1을 권장합니다. 이미 별도 proxy를 띄우고 SAUCEDUST_OUTBOUND_PROXY를 직접 넣은 경우에만 0으로 둘 수 있습니다.",
            example: Some("1"),
        },
        "SAUCEDUST_VPN_DATA_DIR" => EnvHelp {
            role: "ExpressVPN OpenVPN 프록시 런타임 파일을 저장할 디렉터리입니다.",
            required: "선택입니다.",
            default_condition: "비워두면 SAUCE_ENGINE_DATA_DIR 아래 vpn 디렉터리를 사용합니다.",
            example: Some("/Volumes/ExternalSSD/saucedust/vpn"),
        },
        "EXPRESSVPN_OVPN_DIR" => EnvHelp {
            role: "ExpressVPN Manual Configuration에서 받은 .ovpn 파일 여러 개를 넣어둘 디렉터리입니다.",
            required: "여러 ExpressVPN 서버를 자동 전환하려면 필수입니다.",
            default_condition: "비워두면 EXPRESSVPN_OVPN_PATH 단일 파일만 사용합니다.",
            example: Some("/Users/user/Downloads/expressvpn-ovpn"),
        },
        "EXPRESSVPN_OVPN_PATH" => EnvHelp {
            role: "ExpressVPN Manual Configuration에서 받은 .ovpn 파일 경로입니다.",
            required: "EXPRESSVPN_OVPN_DIR를 비워두고 SAUCEDUST_VPN_AUTOSTART=1이면 필수입니다.",
            default_condition: "EXPRESSVPN_OVPN_DIR에 .ovpn 파일들이 있으면 비워둘 수 있습니다.",
            example: Some("/Users/user/Downloads/my_expressvpn_location_udp.ovpn"),
        },
        "EXPRESSVPN_USERNAME" => EnvHelp {
            role: "ExpressVPN Manual Configuration의 OpenVPN username입니다.",
            required: "SAUCEDUST_VPN_AUTOSTART=1이면 필수입니다.",
            default_condition: "이미 별도 proxy를 띄우고 SAUCEDUST_OUTBOUND_PROXY를 직접 넣은 경우에만 비워둘 수 있습니다.",
            example: Some("expressvpn-manual-username"),
        },
        "EXPRESSVPN_PASSWORD" => EnvHelp {
            role: "ExpressVPN Manual Configuration의 OpenVPN password입니다.",
            required: "SAUCEDUST_VPN_AUTOSTART=1이면 필수입니다.",
            default_condition: "이미 별도 proxy를 띄우고 SAUCEDUST_OUTBOUND_PROXY를 직접 넣은 경우에만 비워둘 수 있습니다.",
            example: Some("expressvpn-manual-password"),
        },
        "EXPRESSVPN_PROXY_PORT" => EnvHelp {
            role: "macOS localhost에 노출할 VPN HTTP proxy 포트입니다.",
            required: "선택입니다.",
            default_condition: "기본값 1080을 권장합니다. 이미 사용 중이면 다른 포트로 바꾸십시오.",
            example: Some("1080"),
        },
        "SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR" => EnvHelp {
            role: "Danbooru 이미지 CDN 다운로드 연결 오류가 반복될 때 ExpressVPN .ovpn을 자동 전환할지 정합니다.",
            required: "선택입니다.",
            default_condition: "EXPRESSVPN_OVPN_DIR에 여러 .ovpn을 넣어둘 경우 기본값 1을 권장합니다.",
            example: Some("1"),
        },
        "SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR" => EnvHelp {
            role: "다운로드 연결 오류가 발생했을 때 최근 VPN 전환 쿨다운보다 다른 .ovpn 재연결을 우선할지 정합니다.",
            required: "선택입니다.",
            default_condition: "기본값 1을 권장합니다. 대량 worker에서 VPN 전환이 너무 잦으면 0으로 낮추십시오.",
            example: Some("1"),
        },
        "SAUCEDUST_VPN_ROTATE_COOLDOWN_SECS" => EnvHelp {
            role: "여러 다운로드 worker가 동시에 실패할 때 VPN 전환을 너무 자주 하지 않도록 막는 최소 간격입니다.",
            required: "선택입니다.",
            default_condition: "기본값 10초를 권장합니다. CDN 연결 오류가 잦으면 5~30 사이로 조정하십시오.",
            example: Some("10"),
        },
        "SAUCEDUST_VPN_ROTATE_ATTEMPTS" => EnvHelp {
            role: "이미지 다운로드 1건이 CDN 연결 오류를 만났을 때 다른 ExpressVPN 리전으로 바꿔가며 재시도할 최대 횟수입니다.",
            required: "선택입니다.",
            default_condition: "기본값 3을 권장합니다. .ovpn 파일이 많고 CDN 오류가 잦으면 4~5까지 올릴 수 있습니다.",
            example: Some("3"),
        },
        "SAUCEDUST_VPN_ROTATE_STATE_PATH" => EnvHelp {
            role: "VPN 자동 전환 쿨다운 상태 파일 경로입니다.",
            required: "선택입니다.",
            default_condition: "비워두면 SAUCEDUST_VPN_DATA_DIR 또는 SAUCE_ENGINE_DATA_DIR 아래에 자동 저장합니다.",
            example: Some("/Volumes/ExternalSSD/saucedust/vpn/last-rotate-at"),
        },
        "SAUCEDUST_VPN_HEALTHCHECK_URL" => EnvHelp {
            role: "VPN 리전 전환 시 프록시로 실제 연결 검증할 URL입니다. 다운로드 실패 시 인덱서가 실패한 CDN URL을 임시로 넘깁니다.",
            required: "선택입니다.",
            default_condition: "비워두면 DANBOORU_BASE_URL/posts.json?limit=1을 기본 검증합니다.",
            example: Some("https://danbooru.donmai.us/posts.json?limit=1"),
        },
        "SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS" => EnvHelp {
            role: ".ovpn별 Danbooru 통신 가능 여부를 다시 점검할 주기입니다.",
            required: "선택입니다.",
            default_condition: "기본값 3600초입니다. 너무 낮추면 VPN 점검 시간이 길어질 수 있습니다.",
            example: Some("3600"),
        },
        "SAUCE_ENGINE_DATA_DIR" => EnvHelp {
            role: "PostgreSQL 데이터, Qdrant 저장소, Qdrant 소스 빌드 위치의 기본 루트입니다.",
            required: "선택입니다.",
            default_condition: "내장 SSD를 쓰면 비워둬도 됩니다. 외장 SSD를 쓰면 절대경로를 입력하십시오.",
            example: Some("/Volumes/ExternalSSD/saucedust"),
        },
        "SAUCE_ENGINE_POSTGRES_DATA_DIR" => EnvHelp {
            role: "PostgreSQL 데이터 디렉터리만 따로 지정합니다.",
            required: "선택입니다.",
            default_condition: "SAUCE_ENGINE_DATA_DIR 아래 postgres-data를 써도 되면 비워두십시오.",
            example: Some("/Volumes/ExternalSSD/saucedust/postgres-data"),
        },
        "SAUCE_ENGINE_QDRANT_STORAGE_DIR" => EnvHelp {
            role: "Qdrant 벡터 DB 저장소만 따로 지정합니다.",
            required: "선택입니다.",
            default_condition: "SAUCE_ENGINE_DATA_DIR 아래 qdrant-storage를 써도 되면 비워두십시오.",
            example: Some("/Volumes/ExternalSSD/saucedust/qdrant-storage"),
        },
        "DANBOORU_BASE_URL" => EnvHelp {
            role: "저장되는 canonical Danbooru URL 기준 주소입니다.",
            required: "Danbooru 인덱싱을 하면 필수입니다.",
            default_condition: "일반 Danbooru를 수집하면 기본값을 사용하십시오.",
            example: Some("https://danbooru.donmai.us"),
        },
        "DANBOORU_CONNECT_BASE_URL" => EnvHelp {
            role: "실제 TLS 접속에 사용할 Danbooru 호환 접속 주소입니다.",
            required: "Danbooru 직접 접속이 차단되는 환경에서는 필수입니다.",
            default_condition: "danbooru.donmai.us SNI가 차단되는 환경이면 https://donmai.us를 사용하십시오.",
            example: Some("https://donmai.us"),
        },
        "DANBOORU_HOST_HEADER" => EnvHelp {
            role: "접속 주소와 API 라우팅 호스트를 분리할 때 사용할 HTTP Host 값입니다.",
            required: "DANBOORU_CONNECT_BASE_URL을 https://donmai.us로 쓰면 필수입니다.",
            default_condition: "일반 Danbooru API 라우팅은 danbooru.donmai.us를 사용하십시오.",
            example: Some("danbooru.donmai.us"),
        },
        "DANBOORU_USER_AGENT" => EnvHelp {
            role: "Danbooru 요청에 넣을 User-Agent입니다.",
            required: "필수입니다.",
            default_condition: "개인 서버 MVP라면 기본값을 써도 됩니다. 운영 시 식별 가능한 앱 이름으로 바꾸는 것을 권장합니다.",
            example: Some("saucedust/0.1.0"),
        },
        "DANBOORU_LATEST_TAGS" => EnvHelp {
            role: "최신 post id를 확인할 때만 사용하는 Danbooru 태그 조건입니다. 실제 저장 대상은 INDEX_TAGS로 다시 필터링됩니다.",
            required: "선택입니다.",
            default_condition: "최신 safe 이미지 흐름을 따라가려면 rating:g를 권장합니다.",
            example: Some("rating:g"),
        },
        "SAUCEDUST_DNS_RESOLVER" => EnvHelp {
            role: "saucedust 내부 HTTP 요청에서 사용할 DNS resolver 모드입니다.",
            required: "선택입니다.",
            default_condition: "OS DNS를 건드리지 않고 프로그램 내부 요청만 Cloudflare DNS로 조회하려면 cloudflare를 권장합니다. google 또는 system도 가능합니다.",
            example: Some("cloudflare"),
        },
        "INDEX_LIMIT" => EnvHelp {
            role: "기본 인덱싱 요청에서 한 번에 목표로 삼을 이미지 수입니다.",
            required: "선택입니다.",
            default_condition: "MVP 검증은 100~1000 사이를 권장합니다.",
            example: Some("1000"),
        },
        "INDEX_TAGS" => EnvHelp {
            role: "Danbooru 검색 조건입니다.",
            required: "Danbooru 인덱싱을 하면 필수입니다.",
            default_condition: "최신 safe 이미지 전체를 계속 수집하려면 기본값 rating:g를 사용하십시오. score/source 필터를 넣으면 최신 이미지 대부분을 건너뜁니다.",
            example: Some("rating:g"),
        },
        "VECTOR_SIZE" => EnvHelp {
            role: "Qdrant collection의 벡터 차원입니다.",
            required: "필수입니다.",
            default_condition: "OpenCLIP ViT-B-32 기본 출력은 512이므로 그대로 사용하십시오.",
            example: Some("512"),
        },
        "REQUEST_TIMEOUT_SECS" => EnvHelp {
            role: "외부 HTTP 요청 timeout입니다.",
            required: "선택입니다.",
            default_condition: "일반 네트워크에서는 30초를 권장합니다.",
            example: Some("30"),
        },
        "INDEX_BATCH_PREP_TIMEOUT_SECS" => EnvHelp {
            role: "배치 시작 전 기존 PostgreSQL/Qdrant 중복 확인에 허용할 최대 시간입니다.",
            required: "선택입니다.",
            default_condition: "기본값 120초입니다. Qdrant나 DB가 멈춘 요청을 계속 기다리지 않게 합니다.",
            example: Some("120"),
        },
        "INDEX_DATABASE_OP_TIMEOUT_SECS" => EnvHelp {
            role: "PostgreSQL/Qdrant 저장 및 조회 1회에 허용할 최대 시간입니다.",
            required: "선택입니다.",
            default_condition: "기본값 90초입니다. DB 저장 단계에서 permit이 영구 점유되는 것을 막습니다.",
            example: Some("90"),
        },
        "INDEX_EMBEDDING_OP_TIMEOUT_SECS" => EnvHelp {
            role: "embedding-worker 요청 1회에 허용할 최대 시간입니다.",
            required: "선택입니다.",
            default_condition: "기본값 120초입니다. MPS/worker가 멈춘 요청을 실패 처리합니다.",
            example: Some("120"),
        },
        "CRAWL_DELAY_MS" => EnvHelp {
            role: "Danbooru API 요청 사이 기본 delay입니다.",
            required: "선택입니다.",
            default_condition: "사이트 부하를 줄이기 위해 500ms 기본값을 권장합니다.",
            example: Some("500"),
        },
        "CRAWL_PIPELINE_CONCURRENCY" => EnvHelp {
            role: "이미지 다운로드/해시/임베딩/DB 저장 파이프라인의 전체 전역 동시 처리 상한입니다. worker 수를 1000 이상으로 늘려도 실제 무거운 처리와 목록 조회는 제한된 수만 동시에 실행됩니다.",
            required: "선택입니다.",
            default_condition: "M4 16GB에서는 안정 시작값 2를 권장합니다. worker 수를 늘려도 연속 크롤링 전체가 이 값 안에서 처리됩니다.",
            example: Some("2"),
        },
        "CRAWL_EMBEDDING_CONCURRENCY" => EnvHelp {
            role: "Python OpenCLIP embedding-worker에 동시에 보낼 이미지 수 상한입니다.",
            required: "선택입니다.",
            default_condition: "비워두면 CRAWL_PIPELINE_CONCURRENCY와 같은 값으로 자동 동기화됩니다. 임베딩 오류가 보일 때만 낮추십시오.",
            example: Some("16"),
        },
        "CRAWL_DATABASE_CONCURRENCY" => EnvHelp {
            role: "PostgreSQL 메타데이터 저장과 Qdrant 벡터 저장을 동시에 실행할 수 있는 수입니다.",
            required: "선택입니다.",
            default_condition: "비워두면 CRAWL_PIPELINE_CONCURRENCY와 같은 값으로 자동 동기화됩니다. PostgreSQL pool 보호를 위해 최대 64로 제한됩니다.",
            example: Some("32"),
        },
        "CRAWL_BACKFILL_WORKERS" => EnvHelp {
            role: "과거 ID 구간을 병렬로 훑는 backfill worker 수입니다.",
            required: "선택입니다.",
            default_condition: "M4 16GB에서는 안정 시작값 2를 권장합니다.",
            example: Some("2"),
        },
        "CRAWL_BACKFILL_ACTIVE_RANGES" => EnvHelp {
            role: "동시에 실제 처리 중일 수 있는 과거 backfill ID 구간 수입니다. worker를 크게 늘려도 이 값만큼만 구간을 예약합니다.",
            required: "선택입니다.",
            default_condition: "비워두면 CRAWL_PIPELINE_CONCURRENCY와 CRAWL_BACKFILL_WORKERS 기준으로 자동 산정됩니다. worker 1000개를 써도 16~64 사이를 권장합니다.",
            example: Some("32"),
        },
        "CRAWL_BACKFILL_RANGE_SIZE" => EnvHelp {
            role: "backfill worker 하나가 lease로 가져갈 ID 구간 크기입니다.",
            required: "선택입니다.",
            default_condition: "기본값 10000으로 시작하고, 실패가 많으면 낮추십시오.",
            example: Some("10000"),
        },
        "CRAWL_BACKFILL_IDLE_SECS" => EnvHelp {
            role: "과거 backfill worker가 당장 할당 가능한 ID 구간을 못 받았을 때 다시 확인하기 전 쉬는 시간입니다.",
            required: "선택입니다.",
            default_condition: "기본값 30초를 권장합니다. 이전 버전의 내부 300초 대기보다 빠르게 재확인합니다.",
            example: Some("30"),
        },
        "SAUCEDUST_VERBOSE_INDEX_LOGS" => EnvHelp {
            role: "이미지별 다운로드/해시/임베딩/DB 저장 단계 로그를 모두 출력할지 정합니다.",
            required: "선택입니다.",
            default_condition: "최고 처리량을 원하면 0을 권장합니다. 문제 분석이 필요할 때만 1로 켜십시오.",
            example: Some("0"),
        },
        _ => EnvHelp {
            role: "사용자 정의 환경값입니다.",
            required: "프로젝트에서 참조할 때만 필요합니다.",
            default_condition: "사용처를 알고 있을 때만 값을 입력하십시오.",
            example: None,
        },
    }
}

async fn set(key: String, value: String) -> anyhow::Result<()> {
    let normalized = normalize_key(&key)?;
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure_env_example(&root).await?;
    let env_path = ensure_env_file(&root).await?;
    let example_path = root.join(".env.example");
    let mut file = EnvFile::load(&env_path).await?;
    let example = EnvFile::load(&example_path).await?;
    file.sync_with_defaults(&example);
    file.set(normalized.clone(), value);
    file.write(&env_path).await?;
    if is_secret_key(&normalized) {
        println!("updated {normalized}=<hidden>");
    } else {
        println!("updated {normalized}");
    }
    Ok(())
}

async fn show(show_secrets: bool) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure_env_example(&root).await?;
    let env_path = ensure_env_file(&root).await?;
    let example_path = root.join(".env.example");
    let mut file = EnvFile::load(&env_path).await?;
    let example = EnvFile::load(&example_path).await?;
    file.sync_with_defaults(&example);
    for key in &file.keys {
        let value = file.values.get(key).cloned().unwrap_or_default();
        let value = if show_secrets || !is_secret_key(key) {
            value
        } else {
            mask_value(&value)
        };
        println!("{key}={value}");
    }
    Ok(())
}

async fn ensure_env_file(root: &Path) -> anyhow::Result<PathBuf> {
    let env_path = root.join(".env");
    if !env_path.exists() {
        tokio::fs::write(&env_path, crate::runtime_assets::ENV_EXAMPLE).await?;
        println!("created {}", env_path.display());
    }
    Ok(env_path)
}

fn read_line(prompt: &str) -> anyhow::Result<String> {
    print!("{prompt}");
    io::stdout().flush()?;
    let mut input = String::new();
    io::stdin().read_line(&mut input)?;
    Ok(input.trim_end_matches(['\r', '\n']).to_owned())
}

fn normalize_key(key: &str) -> anyhow::Result<String> {
    let key = key.trim();
    if key.is_empty() {
        anyhow::bail!("env key is empty");
    }
    if !key
        .chars()
        .all(|ch| ch.is_ascii_uppercase() || ch.is_ascii_digit() || ch == '_')
    {
        anyhow::bail!("env key must contain only A-Z, 0-9, and _");
    }
    Ok(key.to_owned())
}

fn is_secret_key(key: &str) -> bool {
    key == "DATABASE_URL"
        || key.contains("TOKEN")
        || key.contains("PASSWORD")
        || key.contains("PASS")
        || key.contains("KEY")
}

fn mask_value(value: &str) -> String {
    if value.is_empty() {
        return String::new();
    }
    if value.len() <= 8 {
        return "<hidden>".to_owned();
    }
    format!("{}...{}", &value[..4], &value[value.len() - 4..])
}

#[derive(Debug, Clone)]
struct EnvFile {
    keys: Vec<String>,
    values: BTreeMap<String, String>,
}

impl EnvFile {
    async fn load(path: &Path) -> anyhow::Result<Self> {
        let raw = tokio::fs::read_to_string(path).await.unwrap_or_default();
        Ok(parse_env_file(&raw))
    }

    fn sync_with_defaults(&mut self, defaults: &EnvFile) -> EnvSyncReport {
        let old_keys = self.keys.iter().cloned().collect::<BTreeSet<_>>();
        let default_keys = defaults.keys.iter().cloned().collect::<BTreeSet<_>>();
        let mut values = BTreeMap::new();
        for key in &defaults.keys {
            let value = migrated_value(key, &self.values)
                .or_else(|| self.values.get(key).cloned())
                .or_else(|| defaults.values.get(key).cloned())
                .unwrap_or_default();
            values.insert(key.clone(), value);
        }
        let added = default_keys.difference(&old_keys).count();
        let removed = old_keys.difference(&default_keys).count();
        self.keys = defaults.keys.clone();
        self.values = values;
        EnvSyncReport { added, removed }
    }

    fn set(&mut self, key: String, value: String) {
        if !self.keys.contains(&key) {
            self.keys.push(key.clone());
        }
        self.values.insert(key, value);
    }

    async fn write(&self, path: &Path) -> anyhow::Result<()> {
        let mut output = String::new();
        for key in &self.keys {
            let value = self.values.get(key).cloned().unwrap_or_default();
            output.push_str(key);
            output.push('=');
            output.push_str(&format_value(&value));
            output.push('\n');
        }
        tokio::fs::write(path, output).await?;
        Ok(())
    }
}

fn migrated_value(key: &str, values: &BTreeMap<String, String>) -> Option<String> {
    match key {
        "CRAWL_PIPELINE_CONCURRENCY" => values.get("CRAWL_CONCURRENCY").cloned(),
        "CRAWL_EMBEDDING_CONCURRENCY" | "CRAWL_DATABASE_CONCURRENCY"
            if values.get(key).is_some_and(|value| value.trim() == "2") =>
        {
            Some(String::new())
        }
        _ => None,
    }
}

fn parse_env_file(raw: &str) -> EnvFile {
    let mut keys = Vec::new();
    let mut values = BTreeMap::new();
    for line in raw.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        let Some((key, value)) = trimmed.split_once('=') else {
            continue;
        };
        let key = key.trim().to_owned();
        if key.is_empty() {
            continue;
        }
        if !keys.contains(&key) {
            keys.push(key.clone());
        }
        values.insert(key, unquote_value(value.trim()));
    }
    EnvFile { keys, values }
}

fn unquote_value(value: &str) -> String {
    if value.len() >= 2 && value.starts_with('"') && value.ends_with('"') {
        return value[1..value.len() - 1].replace("\\\"", "\"");
    }
    value.to_owned()
}

fn format_value(value: &str) -> String {
    if value.is_empty()
        || value
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || matches!(ch, ':' | '/' | '.' | '_' | '-' | '@'))
    {
        return value.to_owned();
    }
    format!("\"{}\"", value.replace('"', "\\\""))
}

#[cfg(test)]
mod tests {
    use super::{format_value, mask_value, parse_env_file};

    #[test]
    fn parses_quoted_env_values() {
        let env = parse_env_file("INDEX_TAGS=\"rating:g source:* score:>20\"\nVECTOR_SIZE=512\n");
        assert_eq!(
            env.values.get("INDEX_TAGS").map(String::as_str),
            Some("rating:g source:* score:>20")
        );
        assert_eq!(
            env.values.get("VECTOR_SIZE").map(String::as_str),
            Some("512")
        );
    }

    #[test]
    fn quotes_values_with_spaces() {
        assert_eq!(
            format_value("rating:g source:* score:>20"),
            "\"rating:g source:* score:>20\""
        );
        assert_eq!(
            format_value("http://localhost:8000"),
            "http://localhost:8000"
        );
    }

    #[test]
    fn masks_secret_values() {
        assert_eq!(mask_value(""), "");
        assert_eq!(mask_value("12345678"), "<hidden>");
        assert_eq!(mask_value("1234567890abcdef"), "1234...cdef");
    }

    #[test]
    fn sync_adds_missing_and_removes_obsolete_keys() {
        let mut env = parse_env_file("A=old\nREMOVED=value\n");
        let defaults = parse_env_file("A=default\nB=new\n");
        let report = env.sync_with_defaults(&defaults);

        assert_eq!(report.added, 1);
        assert_eq!(report.removed, 1);
        assert_eq!(env.keys, vec!["A".to_owned(), "B".to_owned()]);
        assert_eq!(env.values.get("A").map(String::as_str), Some("old"));
        assert_eq!(env.values.get("B").map(String::as_str), Some("new"));
        assert!(!env.values.contains_key("REMOVED"));
    }

    #[test]
    fn sync_migrates_crawl_concurrency_to_pipeline_concurrency() {
        let mut env = parse_env_file("CRAWL_CONCURRENCY=7\n");
        let defaults = parse_env_file("CRAWL_PIPELINE_CONCURRENCY=2\n");
        let report = env.sync_with_defaults(&defaults);

        assert_eq!(report.added, 1);
        assert_eq!(report.removed, 1);
        assert_eq!(
            env.values
                .get("CRAWL_PIPELINE_CONCURRENCY")
                .map(String::as_str),
            Some("7")
        );
        assert!(!env.values.contains_key("CRAWL_CONCURRENCY"));
    }

    #[test]
    fn sync_clears_old_stage_concurrency_defaults() {
        let mut env =
            parse_env_file("CRAWL_EMBEDDING_CONCURRENCY=2\nCRAWL_DATABASE_CONCURRENCY=2\n");
        let defaults =
            parse_env_file("CRAWL_EMBEDDING_CONCURRENCY=\nCRAWL_DATABASE_CONCURRENCY=\n");
        env.sync_with_defaults(&defaults);

        assert_eq!(
            env.values
                .get("CRAWL_EMBEDDING_CONCURRENCY")
                .map(String::as_str),
            Some("")
        );
        assert_eq!(
            env.values
                .get("CRAWL_DATABASE_CONCURRENCY")
                .map(String::as_str),
            Some("")
        );
    }
}
