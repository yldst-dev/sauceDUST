use std::collections::BTreeMap;
use std::env;
use std::fs;
use std::net::TcpListener;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::Stdio;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::Context;
use serde::{Deserialize, Serialize};
use tokio::process::Command;

const INSTANCE: &str = "saucedust-vpn";
const GUEST_DIR: &str = "/mnt/saucedust-vpn";
const GUEST_PROXY_PORT: u16 = 8888;
const ACTIVE_LIMA_YAML: &str = "active-lima.yaml";
const ACTIVE_PROXY_PORT: &str = "active-proxy-port";
const ACTIVE_OVPN_PATH: &str = "active-ovpn-path";
const OVPN_HEALTH_CACHE: &str = "ovpn-health.json";
const HEALTH_OK: &str = "OK";
const HEALTH_FAIL: &str = "FAIL";

pub async fn autostart_if_enabled() -> anyhow::Result<()> {
    if env_bool("SAUCEDUST_VPN_AUTOSTART", false) {
        crate::ui::step(1, 1, "ExpressVPN 프록시 자동 시작");
        if let Err(error) = validate_autostart_config() {
            if direct_fallback_enabled() {
                enable_direct_fallback(&format!(
                    "ExpressVPN 설정 오류로 직접망 fallback 사용: {error:#}"
                ));
                return Ok(());
            }
            return Err(error);
        }
        if let Err(error) = start().await {
            if direct_fallback_enabled() {
                enable_direct_fallback(&format!(
                    "ExpressVPN 시작 실패로 직접망 fallback 사용: {error:#}"
                ));
                return Ok(());
            }
            return Err(error);
        }
    }
    Ok(())
}

pub fn ensure_outbound_ready_for_runtime() -> anyhow::Result<()> {
    if outbound_proxy_configured() {
        if env_bool("SAUCEDUST_VPN_AUTOSTART", false) {
            if let Err(error) = validate_autostart_config() {
                if direct_fallback_enabled() {
                    crate::ui::warn(&format!(
                        "ExpressVPN 설정 오류가 있지만 직접망 fallback이 허용되어 계속 진행합니다: {error:#}"
                    ));
                    return Ok(());
                }
                return Err(error);
            }
        }
        return Ok(());
    }
    if direct_fallback_enabled() {
        crate::ui::warn(
            "프록시가 없지만 직접망 fallback이 허용되어 현재 네트워크로 Danbooru 요청을 진행합니다.",
        );
        return Ok(());
    }
    if !env_bool("SAUCEDUST_VPN_AUTOSTART", false) {
        anyhow::bail!(
            "Danbooru 요청이 현재 네트워크로 직접 나가지 않도록 VPN 설정이 필요합니다. 현재 SAUCEDUST_OUTBOUND_PROXY={}, SAUCEDUST_VPN_AUTOSTART={} 상태입니다. .env 위치와 값을 확인하고 SAUCEDUST_VPN_AUTOSTART=1, EXPRESSVPN_OVPN_DIR 또는 EXPRESSVPN_OVPN_PATH, EXPRESSVPN_USERNAME, EXPRESSVPN_PASSWORD를 설정하십시오.",
            env_state("SAUCEDUST_OUTBOUND_PROXY"),
            env_state("SAUCEDUST_VPN_AUTOSTART")
        );
    }
    match validate_autostart_config() {
        Ok(()) => Ok(()),
        Err(error) if direct_fallback_enabled() => {
            crate::ui::warn(&format!(
                "ExpressVPN 설정 오류가 있지만 직접망 fallback이 허용되어 계속 진행합니다: {error:#}"
            ));
            Ok(())
        }
        Err(error) => Err(error),
    }
}

pub fn validate_autostart_config() -> anyhow::Result<()> {
    VpnConfig::from_env().map(|_| ()).map_err(|error| {
        anyhow::anyhow!(
            "ExpressVPN 자동 시작 설정이 완전하지 않습니다: {error:#}. ./run_saucedust.sh env setup --advanced로 값을 점검하십시오."
        )
    })
}

pub async fn setup() -> anyhow::Result<()> {
    crate::ui::title("ExpressVPN OpenVPN 프록시 설정");
    crate::ui::step(1, 4, "환경값 확인");
    let mut config = VpnConfig::from_env()?;
    config.proxy_port = resolve_proxy_port(config.proxy_port)?;
    crate::ui::ok(&format!(
        "ExpressVPN .ovpn {}개, username, password 확인 완료",
        config.ovpn_paths.len()
    ));

    crate::ui::step(2, 4, "Lima 설치 확인");
    ensure_lima().await?;
    crate::ui::ok("Lima 준비 완료");

    crate::ui::step(3, 4, "VPN 런타임 파일 생성");
    write_runtime_files(&config)?;
    crate::ui::ok(&format!("VPN 파일 생성 완료: {}", config.dir.display()));

    crate::ui::step(4, 4, "Lima VM 생성");
    ensure_instance(&config).await?;
    crate::ui::ok("saucedust-vpn VM 준비 완료");
    print_proxy_env(&config);
    Ok(())
}

pub async fn start() -> anyhow::Result<()> {
    let mut config = VpnConfig::from_env()?.with_active_proxy_port();
    if read_active_proxy_port(&config).is_none() {
        config.proxy_port = resolve_proxy_port(config.proxy_port)?;
    }
    ensure_lima().await?;
    refresh_stale_ovpn_health(&mut config, false).await?;
    let candidates = config.candidates_from_active(false);
    start_with_candidates(config, candidates).await
}

pub async fn rotate() -> anyhow::Result<()> {
    let mut config = VpnConfig::from_env()?.with_active_proxy_port();
    if read_active_proxy_port(&config).is_none() {
        config.proxy_port = resolve_proxy_port(config.proxy_port)?;
    }
    ensure_lima().await?;
    mark_active_ovpn_failed(&config, "runtime network failure triggered VPN rotation")?;
    refresh_stale_ovpn_health(&mut config, true).await?;
    let candidates = config.candidates_from_active(true);
    if candidates.len() > 1 {
        crate::ui::info("ExpressVPN 서버를 다음 .ovpn으로 전환합니다.");
    } else {
        crate::ui::warn("사용 가능한 .ovpn이 1개뿐이라 같은 서버를 재시작합니다.");
    }
    start_with_candidates(config, candidates).await
}

async fn start_with_candidates(
    mut config: VpnConfig,
    candidates: Vec<PathBuf>,
) -> anyhow::Result<()> {
    let mut last_error = None;
    let candidates = config.sort_candidates_by_health(candidates);
    for ovpn_path in candidates {
        config.ovpn_path = ovpn_path;
        crate::ui::info(&format!(
            "ExpressVPN .ovpn 적용: {}",
            config
                .ovpn_path
                .file_name()
                .and_then(|name| name.to_str())
                .unwrap_or("client.ovpn")
        ));
        match start_current(&config).await {
            Ok(()) => {
                record_ovpn_health(&config, HEALTH_OK, None)?;
                return Ok(());
            }
            Err(error) => {
                crate::ui::warn(&format!(
                    "ExpressVPN .ovpn 실패: {} - {error:#}",
                    config.ovpn_path.display()
                ));
                record_ovpn_health(&config, HEALTH_FAIL, Some(&format!("{error:#}")))?;
                last_error = Some(error);
                stop_openvpn_process().await.ok();
            }
        }
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("사용 가능한 ExpressVPN .ovpn이 없습니다")))
}

async fn start_current(config: &VpnConfig) -> anyhow::Result<()> {
    write_runtime_files(config)?;
    ensure_instance(config).await?;
    wait_instance_running().await?;
    wait_shell_ready().await?;
    run_lima_shell(&[
        "sudo",
        "mkdir",
        "-p",
        "/var/log/saucedust-vpn",
        "/run/saucedust-vpn",
    ])
    .await?;
    configure_tinyproxy().await?;
    stop_openvpn_process().await.ok();
    run_lima_shell(&[
        "sudo",
        "openvpn",
        "--config",
        &format!("{GUEST_DIR}/client.ovpn"),
        "--auth-user-pass",
        &format!("{GUEST_DIR}/auth.txt"),
        "--log",
        &format!("{GUEST_DIR}/logs/openvpn.log"),
        "--writepid",
        "/run/saucedust-vpn/openvpn.pid",
        "--daemon",
        "saucedust-vpn",
    ])
    .await?;
    wait_openvpn_tunnel(config).await?;
    wait_proxy(config).await?;
    wait_target_via_proxy(config).await?;
    write_active_proxy_port(config)?;
    write_active_ovpn_path(config)?;
    apply_proxy_env(config);
    crate::ui::ok(&format!(
        "VPN 프록시 시작 완료: http://127.0.0.1:{}",
        config.proxy_port
    ));
    print_proxy_env(config);
    Ok(())
}

pub async fn stop() -> anyhow::Result<()> {
    stop_openvpn_process().await.ok();
    let _ = Command::new(limactl_bin())
        .arg("stop")
        .arg(INSTANCE)
        .stdin(Stdio::null())
        .status()
        .await;
    crate::ui::ok("VPN VM 중지 요청 완료");
    Ok(())
}

pub async fn status() -> anyhow::Result<()> {
    let config = VpnConfig::from_env()?.with_active_proxy_port();
    let running = instance_running().await;
    if running {
        crate::ui::ok("saucedust-vpn VM 실행 중");
    } else {
        crate::ui::warn("saucedust-vpn VM 중지됨");
    }
    match tunnel_ready().await {
        Ok(()) => crate::ui::ok("OpenVPN 터널 실행 중"),
        Err(error) => crate::ui::warn(&format!("OpenVPN 터널 확인 실패: {error:#}")),
    }
    match verified_proxy_ip(&config).await {
        Ok(ip) => crate::ui::ok(&format!("VPN 프록시 응답: {ip}")),
        Err(error) => crate::ui::warn(&format!("VPN 프록시 확인 실패: {error:#}")),
    }
    crate::ui::info(&format!(
        "OpenVPN 로그: {}",
        config.dir.join("logs/openvpn.log").display()
    ));
    if let Some(path) = read_active_ovpn_path(&config) {
        crate::ui::info(&format!("현재 .ovpn: {}", path.display()));
    }
    print_ovpn_health_summary(&config);
    Ok(())
}

pub async fn test() -> anyhow::Result<()> {
    let config = VpnConfig::from_env()?.with_active_proxy_port();
    let direct = public_ip_direct().await.ok();
    tunnel_ready().await?;
    let proxied = verified_proxy_ip(&config).await?;
    if let Some(direct) = direct {
        crate::ui::info(&format!("현재 네트워크 IP: {direct}"));
    }
    crate::ui::ok(&format!("VPN 프록시 IP: {proxied}"));
    Ok(())
}

struct VpnConfig {
    ovpn_path: PathBuf,
    ovpn_paths: Vec<PathBuf>,
    username: String,
    password: String,
    dir: PathBuf,
    proxy_port: u16,
}

impl VpnConfig {
    fn from_env() -> anyhow::Result<Self> {
        dotenvy::dotenv().ok();
        let ovpn_paths = ovpn_paths_from_env()?;
        let username = required_env("EXPRESSVPN_USERNAME")?;
        let password = required_env("EXPRESSVPN_PASSWORD")?;
        let dir = optional_env("SAUCEDUST_VPN_DATA_DIR")
            .map(|value| crate::paths::expand_path(&value))
            .unwrap_or(crate::paths::data_dir()?.join("vpn"));
        let proxy_port = env::var("EXPRESSVPN_PROXY_PORT")
            .ok()
            .and_then(|value| value.parse().ok())
            .unwrap_or(1080);
        let ovpn_path =
            select_active_ovpn(&dir, &ovpn_paths).unwrap_or_else(|| ovpn_paths[0].clone());
        Ok(Self {
            ovpn_path,
            ovpn_paths,
            username,
            password,
            dir,
            proxy_port,
        })
    }

    fn with_active_proxy_port(mut self) -> Self {
        if let Some(port) = read_active_proxy_port(&self) {
            self.proxy_port = port;
        }
        self
    }

    fn clone_for_ovpn(&self, ovpn_path: PathBuf) -> Self {
        Self {
            ovpn_path,
            ovpn_paths: self.ovpn_paths.clone(),
            username: self.username.clone(),
            password: self.password.clone(),
            dir: self.dir.clone(),
            proxy_port: self.proxy_port,
        }
    }

    fn candidates_from_active(&self, rotate_first: bool) -> Vec<PathBuf> {
        let active = read_active_ovpn_path(self).unwrap_or_else(|| self.ovpn_path.clone());
        let active_index = self
            .ovpn_paths
            .iter()
            .position(|path| same_path(path, &active))
            .unwrap_or(0);
        let start = if rotate_first {
            (active_index + 1) % self.ovpn_paths.len()
        } else {
            active_index
        };
        (0..self.ovpn_paths.len())
            .map(|offset| self.ovpn_paths[(start + offset) % self.ovpn_paths.len()].clone())
            .collect()
    }

    fn sort_candidates_by_health(&self, candidates: Vec<PathBuf>) -> Vec<PathBuf> {
        let cache = load_ovpn_health_cache(self);
        let mut indexed = candidates
            .into_iter()
            .enumerate()
            .map(|(index, path)| {
                let rank = match cache
                    .entries
                    .get(&ovpn_key(&path))
                    .map(|entry| entry.status.as_str())
                {
                    Some(HEALTH_OK) => 0,
                    None => 1,
                    Some(HEALTH_FAIL) => 2,
                    _ => 1,
                };
                (rank, index, path)
            })
            .collect::<Vec<_>>();
        indexed.sort_by_key(|(rank, index, _)| (*rank, *index));
        indexed.into_iter().map(|(_, _, path)| path).collect()
    }
}

#[derive(Debug, Default, Serialize, Deserialize)]
struct OvpnHealthCache {
    entries: BTreeMap<String, OvpnHealthEntry>,
}

#[derive(Debug, Serialize, Deserialize)]
struct OvpnHealthEntry {
    status: String,
    checked_at_unix_secs: u64,
    error: Option<String>,
}

async fn refresh_stale_ovpn_health(
    config: &mut VpnConfig,
    rotate_first: bool,
) -> anyhow::Result<()> {
    if config.ovpn_paths.len() <= 1 {
        return Ok(());
    }
    let interval = ovpn_health_interval();
    let stale = stale_ovpn_paths(config, interval);
    if stale.is_empty() {
        return Ok(());
    }

    crate::ui::info(&format!(
        "ExpressVPN .ovpn 상태 점검 시작: {}개, interval={}초",
        stale.len(),
        interval.as_secs()
    ));
    let mut checked = 0usize;
    let mut ok = 0usize;
    let mut failed = 0usize;
    let active = read_active_ovpn_path(config).unwrap_or_else(|| config.ovpn_path.clone());
    let mut candidates = stale;
    if rotate_first {
        candidates = rotate_paths_after(&config.ovpn_paths, &active)
            .into_iter()
            .filter(|path| {
                candidates
                    .iter()
                    .any(|candidate| same_path(candidate, path))
            })
            .collect();
    }

    for ovpn_path in candidates {
        config.ovpn_path = ovpn_path;
        let name = ovpn_display_name(&config.ovpn_path);
        crate::ui::info(&format!("ExpressVPN .ovpn 점검: {name}"));
        match start_current(config).await {
            Ok(()) => {
                checked += 1;
                ok += 1;
                record_ovpn_health(config, HEALTH_OK, None)?;
                crate::ui::ok(&format!("ExpressVPN .ovpn OK: {name}"));
            }
            Err(error) => {
                checked += 1;
                failed += 1;
                record_ovpn_health(config, HEALTH_FAIL, Some(&format!("{error:#}")))?;
                crate::ui::warn(&format!("ExpressVPN .ovpn FAIL: {name} - {error:#}"));
                stop_openvpn_process().await.ok();
            }
        }
    }
    crate::ui::info(&format!(
        "ExpressVPN .ovpn 상태 점검 완료: 전체 {checked}개, OK {ok}개, FAIL {failed}개"
    ));
    Ok(())
}

fn stale_ovpn_paths(config: &VpnConfig, interval: Duration) -> Vec<PathBuf> {
    let cache = load_ovpn_health_cache(config);
    let now = unix_now();
    config
        .ovpn_paths
        .iter()
        .filter(|path| {
            let Some(entry) = cache.entries.get(&ovpn_key(path)) else {
                return true;
            };
            now.saturating_sub(entry.checked_at_unix_secs) >= interval.as_secs()
        })
        .cloned()
        .collect()
}

fn rotate_paths_after(paths: &[PathBuf], active: &std::path::Path) -> Vec<PathBuf> {
    if paths.is_empty() {
        return Vec::new();
    }
    let active_index = paths
        .iter()
        .position(|path| same_path(path, active))
        .unwrap_or(0);
    (0..paths.len())
        .map(|offset| paths[(active_index + 1 + offset) % paths.len()].clone())
        .collect()
}

fn mark_active_ovpn_failed(config: &VpnConfig, reason: &str) -> anyhow::Result<()> {
    if let Some(active) = read_active_ovpn_path(config) {
        let config = config.clone_for_ovpn(active);
        record_ovpn_health(&config, HEALTH_FAIL, Some(reason))?;
    }
    Ok(())
}

fn record_ovpn_health(config: &VpnConfig, status: &str, error: Option<&str>) -> anyhow::Result<()> {
    fs::create_dir_all(&config.dir)?;
    let mut cache = load_ovpn_health_cache(config);
    cache.entries.insert(
        ovpn_key(&config.ovpn_path),
        OvpnHealthEntry {
            status: status.to_owned(),
            checked_at_unix_secs: unix_now(),
            error: error.map(str::to_owned),
        },
    );
    save_ovpn_health_cache(config, &cache)
}

fn load_ovpn_health_cache(config: &VpnConfig) -> OvpnHealthCache {
    fs::read_to_string(ovpn_health_cache_path(config))
        .ok()
        .and_then(|value| serde_json::from_str(&value).ok())
        .unwrap_or_default()
}

fn save_ovpn_health_cache(config: &VpnConfig, cache: &OvpnHealthCache) -> anyhow::Result<()> {
    fs::write(
        ovpn_health_cache_path(config),
        serde_json::to_string_pretty(cache)?,
    )?;
    Ok(())
}

fn ovpn_health_cache_path(config: &VpnConfig) -> PathBuf {
    config.dir.join(OVPN_HEALTH_CACHE)
}

fn ovpn_key(path: &std::path::Path) -> String {
    path.canonicalize()
        .unwrap_or_else(|_| path.to_path_buf())
        .to_string_lossy()
        .to_string()
}

fn ovpn_display_name(path: &std::path::Path) -> String {
    path.file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("client.ovpn")
        .to_owned()
}

fn ovpn_health_interval() -> Duration {
    Duration::from_secs(env_u64("SAUCEDUST_VPN_HEALTHCHECK_INTERVAL_SECS", 3600).max(60))
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or_default()
}

fn print_ovpn_health_summary(config: &VpnConfig) {
    let cache = load_ovpn_health_cache(config);
    if cache.entries.is_empty() {
        crate::ui::info("ExpressVPN .ovpn 상태 기록 없음");
        return;
    }
    let ok = cache
        .entries
        .values()
        .filter(|entry| entry.status == HEALTH_OK)
        .count();
    let fail = cache
        .entries
        .values()
        .filter(|entry| entry.status == HEALTH_FAIL)
        .count();
    crate::ui::info(&format!(
        "ExpressVPN .ovpn 상태 기록: OK {ok}개, FAIL {fail}개, 파일 {}",
        ovpn_health_cache_path(config).display()
    ));
}

fn write_runtime_files(config: &VpnConfig) -> anyhow::Result<()> {
    fs::create_dir_all(config.dir.join("logs"))?;
    let auth_path = config.dir.join("auth.txt");
    fs::write(
        &auth_path,
        format!("{}\n{}\n", config.username, config.password),
    )?;
    fs::set_permissions(&auth_path, fs::Permissions::from_mode(0o600))?;

    let source = fs::read_to_string(&config.ovpn_path)
        .with_context(|| format!("failed to read {}", config.ovpn_path.display()))?;
    let mut ovpn = source
        .lines()
        .filter(|line| !line.trim_start().starts_with("auth-user-pass"))
        .map(str::to_owned)
        .collect::<Vec<_>>();
    ovpn.push(format!("auth-user-pass {GUEST_DIR}/auth.txt"));
    ovpn.push("auth-nocache".to_owned());
    fs::write(config.dir.join("client.ovpn"), ovpn.join("\n"))?;
    fs::write(config.dir.join("lima.yaml"), lima_yaml(config))?;
    Ok(())
}

fn lima_yaml(config: &VpnConfig) -> String {
    let dir = config.dir.display();
    let proxy_port = config.proxy_port;
    format!(
        r#"base:
- template:_images/alpine
arch: aarch64
cpus: 1
memory: 512MiB
disk: 2GiB
containerd:
  system: false
  user: false
mounts:
- location: "{dir}"
  mountPoint: "{GUEST_DIR}"
  writable: true
portForwards:
- guestPort: {GUEST_PROXY_PORT}
  hostIP: "127.0.0.1"
  hostPort: {proxy_port}
provision:
- mode: system
  script: |
    apk add --no-cache openvpn tinyproxy curl ca-certificates procps
    mkdir -p /etc/tinyproxy /var/log/tinyproxy /run/tinyproxy
    cat >/etc/tinyproxy/tinyproxy.conf <<'EOF'
    User nobody
    Group nobody
    Port {GUEST_PROXY_PORT}
    Listen 0.0.0.0
    Timeout 600
    MaxClients 200
    Allow 0.0.0.0/0
    ConnectPort 443
    ConnectPort 563
    ConnectPort 80
    LogFile "/var/log/tinyproxy/tinyproxy.log"
    LogLevel Warning
    EOF
"#
    )
}

async fn ensure_lima() -> anyhow::Result<()> {
    if crate::paths::path_in_path("limactl").is_some() {
        return Ok(());
    }
    crate::system_setup::ensure_homebrew_available().await?;
    let brew = crate::paths::brew_bin();
    crate::command::run(
        Command::new(&brew).arg("install").arg("lima"),
        "brew install lima",
    )
    .await?;
    crate::paths::augment_runtime_path();
    if crate::paths::path_in_path("limactl").is_none() {
        anyhow::bail!("limactl 설치 후 PATH에서 감지되지 않습니다");
    }
    Ok(())
}

async fn ensure_instance(config: &VpnConfig) -> anyhow::Result<()> {
    if instance_exists().await {
        if instance_config_matches(config)? {
            return Ok(());
        }
        crate::ui::warn("VPN VM 설정이 변경되어 기존 saucedust-vpn VM을 재생성합니다.");
        delete_instance().await?;
    }
    crate::command::run(
        Command::new(limactl_bin())
            .arg("--tty=false")
            .arg("start")
            .arg("--name")
            .arg(INSTANCE)
            .arg(config.dir.join("lima.yaml")),
        "limactl start saucedust-vpn",
    )
    .await?;
    fs::copy(
        config.dir.join("lima.yaml"),
        config.dir.join(ACTIVE_LIMA_YAML),
    )?;
    Ok(())
}

async fn wait_instance_running() -> anyhow::Result<()> {
    if instance_running().await {
        return Ok(());
    }
    crate::command::run(
        Command::new(limactl_bin())
            .arg("--tty=false")
            .arg("start")
            .arg(INSTANCE),
        "limactl start saucedust-vpn",
    )
    .await
}

async fn wait_shell_ready() -> anyhow::Result<()> {
    let mut last_error = None;
    for _ in 0..30 {
        match run_lima_shell_once(&["sh", "-lc", "true"]).await {
            Ok(_) => return Ok(()),
            Err(error) => last_error = Some(error),
        }
        tokio::time::sleep(Duration::from_secs(2)).await;
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("limactl shell 준비 확인 실패")))
}

async fn configure_tinyproxy() -> anyhow::Result<()> {
    run_lima_shell(&[
        "sudo",
        "sh",
        "-lc",
        "pkill -x tinyproxy || true; tinyproxy -c /etc/tinyproxy/tinyproxy.conf",
    ])
    .await
}

async fn stop_openvpn_process() -> anyhow::Result<()> {
    run_lima_shell(&[
        "sudo",
        "sh",
        "-lc",
        "pkill -f 'openvpn.*saucedust-vpn' || true",
    ])
    .await
}

async fn wait_proxy(config: &VpnConfig) -> anyhow::Result<()> {
    let mut last_error = None;
    for _ in 0..60 {
        match proxy_ip_quick(config).await {
            Ok(_) => return Ok(()),
            Err(error) => last_error = Some(error),
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("VPN 프록시가 응답하지 않습니다")))
}

async fn wait_target_via_proxy(config: &VpnConfig) -> anyhow::Result<()> {
    let mut last_error = None;
    for _ in 0..20 {
        match target_via_proxy(config).await {
            Ok(()) => return Ok(()),
            Err(error) => last_error = Some(error),
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    Err(last_error.unwrap_or_else(|| {
        anyhow::anyhow!("VPN 프록시가 Danbooru/CDN 대상 URL에 연결하지 못했습니다")
    }))
}

async fn wait_openvpn_tunnel(config: &VpnConfig) -> anyhow::Result<()> {
    let mut last_error = None;
    for _ in 0..60 {
        if openvpn_auth_failed(config) {
            anyhow::bail!(
                "ExpressVPN 인증에 실패했습니다. EXPRESSVPN_USERNAME, EXPRESSVPN_PASSWORD 값을 다시 확인하십시오."
            );
        }
        match tunnel_ready().await {
            Ok(()) => return Ok(()),
            Err(error) => last_error = Some(error),
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("OpenVPN 터널이 준비되지 않았습니다")))
}

fn openvpn_auth_failed(config: &VpnConfig) -> bool {
    fs::read_to_string(config.dir.join("logs/openvpn.log"))
        .map(|log| log.contains("AUTH_FAILED"))
        .unwrap_or(false)
}

async fn tunnel_ready() -> anyhow::Result<()> {
    run_lima_shell_once(&[
        "sh",
        "-lc",
        "pgrep -x openvpn >/dev/null && ip addr show tun0 >/dev/null 2>&1",
    ])
    .await
}

async fn verified_proxy_ip(config: &VpnConfig) -> anyhow::Result<String> {
    let proxied = proxy_ip(config).await?;
    if let Ok(direct) = public_ip_direct().await {
        if direct == proxied {
            anyhow::bail!(
                "VPN 프록시가 현재 네트워크 IP({proxied})와 동일합니다. OpenVPN 터널이 적용되지 않은 상태입니다."
            );
        }
    }
    Ok(proxied)
}

async fn proxy_ip(config: &VpnConfig) -> anyhow::Result<String> {
    curl_ip_with_timeout(
        &[
            "--proxy",
            &format!("http://127.0.0.1:{}", config.proxy_port),
            "https://api.ipify.org",
        ],
        8,
        3,
    )
    .await
}

async fn proxy_ip_quick(config: &VpnConfig) -> anyhow::Result<String> {
    curl_ip_with_timeout(
        &[
            "--proxy",
            &format!("http://127.0.0.1:{}", config.proxy_port),
            "https://api.ipify.org",
        ],
        5,
        2,
    )
    .await
}

async fn target_via_proxy(config: &VpnConfig) -> anyhow::Result<()> {
    let proxy = format!("http://127.0.0.1:{}", config.proxy_port);
    let urls = vpn_healthcheck_urls();
    for url in urls {
        curl_target_with_proxy(&proxy, &url, 8, 3).await?;
    }
    Ok(())
}

fn vpn_healthcheck_urls() -> Vec<String> {
    let mut urls = Vec::new();
    if let Some(url) = optional_env("SAUCEDUST_VPN_HEALTHCHECK_URL") {
        urls.push(url);
    }
    let base = optional_env("DANBOORU_BASE_URL")
        .unwrap_or_else(|| "https://danbooru.donmai.us".to_owned())
        .trim_end_matches('/')
        .to_owned();
    let api_url = format!("{base}/posts.json?limit=1");
    if !urls.iter().any(|url| url == &api_url) {
        urls.push(api_url);
    }
    urls
}

async fn curl_target_with_proxy(
    proxy: &str,
    url: &str,
    max_time_secs: u64,
    connect_timeout_secs: u64,
) -> anyhow::Result<()> {
    let user_agent =
        optional_env("DANBOORU_USER_AGENT").unwrap_or_else(|| "saucedust/0.1.0".to_owned());
    let mut command = Command::new("curl");
    command.args([
        "-fsS",
        "--proxy",
        proxy,
        "--connect-timeout",
        &connect_timeout_secs.max(1).to_string(),
        "--max-time",
        &max_time_secs.max(1).to_string(),
        "-A",
        &user_agent,
        "-o",
        "/dev/null",
    ]);
    if looks_like_image_url(url) {
        command.args(["-r", "0-0"]);
    }
    let output = command.arg(url).stdin(Stdio::null()).output().await?;
    if !output.status.success() {
        anyhow::bail!(
            "{}: {}",
            url,
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    Ok(())
}

fn looks_like_image_url(url: &str) -> bool {
    let path = url
        .split(['?', '#'])
        .next()
        .unwrap_or(url)
        .to_ascii_lowercase();
    [".jpg", ".jpeg", ".png", ".webp"]
        .iter()
        .any(|extension| path.ends_with(extension))
}

async fn public_ip_direct() -> anyhow::Result<String> {
    curl_ip_with_timeout(&["https://api.ipify.org"], 8, 3).await
}

async fn curl_ip_with_timeout(
    args: &[&str],
    max_time_secs: u64,
    connect_timeout_secs: u64,
) -> anyhow::Result<String> {
    let output = Command::new("curl")
        .args([
            "-fsS",
            "--connect-timeout",
            &connect_timeout_secs.max(1).to_string(),
            "--max-time",
            &max_time_secs.max(1).to_string(),
        ])
        .args(args)
        .stdin(Stdio::null())
        .output()
        .await?;
    if !output.status.success() {
        anyhow::bail!("{}", String::from_utf8_lossy(&output.stderr).trim());
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_owned())
}

async fn run_lima_shell(args: &[&str]) -> anyhow::Result<()> {
    let mut last_error = None;
    for _ in 0..6 {
        match run_lima_shell_once(args).await {
            Ok(()) => return Ok(()),
            Err(error) => last_error = Some(error),
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("limactl shell failed")))
}

async fn run_lima_shell_once(args: &[&str]) -> anyhow::Result<()> {
    let output = Command::new(limactl_bin())
        .arg("--tty=false")
        .arg("shell")
        .arg("--reconnect")
        .arg("--workdir")
        .arg("/tmp")
        .arg(INSTANCE)
        .args(args)
        .stdin(Stdio::null())
        .output()
        .await?;
    if output.status.success() {
        Ok(())
    } else {
        anyhow::bail!(
            "limactl shell failed with {} stdout={} stderr={}",
            output.status,
            String::from_utf8_lossy(&output.stdout).trim(),
            String::from_utf8_lossy(&output.stderr).trim()
        )
    }
}

async fn instance_exists() -> bool {
    limactl_list().await.contains(INSTANCE)
}

async fn instance_running() -> bool {
    limactl_list()
        .await
        .lines()
        .any(|line| line.contains(INSTANCE) && line.contains("Running"))
}

async fn limactl_list() -> String {
    let output = Command::new(limactl_bin())
        .arg("list")
        .stdin(Stdio::null())
        .output()
        .await;
    output
        .ok()
        .map(|output| String::from_utf8_lossy(&output.stdout).to_string())
        .unwrap_or_default()
}

async fn delete_instance() -> anyhow::Result<()> {
    crate::command::run(
        Command::new(limactl_bin())
            .arg("--tty=false")
            .arg("delete")
            .arg("-f")
            .arg(INSTANCE),
        "limactl delete saucedust-vpn",
    )
    .await
}

fn instance_config_matches(config: &VpnConfig) -> anyhow::Result<bool> {
    let active_path = config.dir.join(ACTIVE_LIMA_YAML);
    if !active_path.exists() {
        return Ok(false);
    }
    let active = fs::read_to_string(active_path)?;
    let desired = fs::read_to_string(config.dir.join("lima.yaml"))?;
    Ok(active == desired)
}

fn write_active_proxy_port(config: &VpnConfig) -> anyhow::Result<()> {
    fs::write(
        config.dir.join(ACTIVE_PROXY_PORT),
        config.proxy_port.to_string(),
    )?;
    Ok(())
}

fn read_active_proxy_port(config: &VpnConfig) -> Option<u16> {
    fs::read_to_string(config.dir.join(ACTIVE_PROXY_PORT))
        .ok()
        .and_then(|value| value.trim().parse().ok())
}

fn write_active_ovpn_path(config: &VpnConfig) -> anyhow::Result<()> {
    fs::write(
        config.dir.join(ACTIVE_OVPN_PATH),
        config.ovpn_path.to_string_lossy().as_ref(),
    )?;
    Ok(())
}

fn read_active_ovpn_path(config: &VpnConfig) -> Option<PathBuf> {
    fs::read_to_string(config.dir.join(ACTIVE_OVPN_PATH))
        .ok()
        .map(|value| value.trim().to_owned())
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
}

fn limactl_bin() -> PathBuf {
    crate::paths::tool_bin("limactl")
}

fn required_env(key: &str) -> anyhow::Result<String> {
    optional_env(key).ok_or_else(|| anyhow::anyhow!("{key} 환경값이 필요합니다"))
}

fn optional_env(key: &str) -> Option<String> {
    env::var(key)
        .ok()
        .map(|value| value.trim().to_owned())
        .filter(|value| !value.is_empty())
}

fn required_path(key: &str) -> anyhow::Result<PathBuf> {
    let path = crate::paths::expand_path(&required_env(key)?);
    if path.exists() {
        Ok(path)
    } else {
        anyhow::bail!("{key} 경로가 존재하지 않습니다: {}", path.display())
    }
}

fn optional_path(key: &str) -> Option<PathBuf> {
    optional_env(key).map(|value| crate::paths::expand_path(&value))
}

fn ovpn_paths_from_env() -> anyhow::Result<Vec<PathBuf>> {
    if let Some(dir) = optional_path("EXPRESSVPN_OVPN_DIR") {
        if !dir.exists() {
            anyhow::bail!(
                "EXPRESSVPN_OVPN_DIR 경로가 존재하지 않습니다: {}",
                dir.display()
            );
        }
        if !dir.is_dir() {
            anyhow::bail!(
                "EXPRESSVPN_OVPN_DIR는 디렉터리여야 합니다: {}",
                dir.display()
            );
        }
        let mut paths = fs::read_dir(&dir)?
            .filter_map(Result::ok)
            .map(|entry| entry.path())
            .filter(|path| {
                path.is_file()
                    && path
                        .extension()
                        .and_then(|extension| extension.to_str())
                        .is_some_and(|extension| extension.eq_ignore_ascii_case("ovpn"))
            })
            .collect::<Vec<_>>();
        paths.sort();
        if paths.is_empty() {
            anyhow::bail!(
                "EXPRESSVPN_OVPN_DIR 안에 .ovpn 파일이 없습니다: {}",
                dir.display()
            );
        }
        return Ok(paths);
    }

    Ok(vec![required_path("EXPRESSVPN_OVPN_PATH")?])
}

fn select_active_ovpn(dir: &std::path::Path, ovpn_paths: &[PathBuf]) -> Option<PathBuf> {
    let active = fs::read_to_string(dir.join(ACTIVE_OVPN_PATH)).ok()?;
    let active = PathBuf::from(active.trim());
    ovpn_paths
        .iter()
        .find(|path| same_path(path, &active))
        .cloned()
}

fn same_path(left: &std::path::Path, right: &std::path::Path) -> bool {
    if left == right {
        return true;
    }
    match (left.canonicalize(), right.canonicalize()) {
        (Ok(left), Ok(right)) => left == right,
        _ => false,
    }
}

fn env_bool(key: &str, default: bool) -> bool {
    env::var(key)
        .ok()
        .map(|value| {
            matches!(
                value.trim().to_ascii_lowercase().as_str(),
                "1" | "true" | "yes" | "on"
            )
        })
        .unwrap_or(default)
}

fn env_u64(key: &str, default: u64) -> u64 {
    env::var(key)
        .ok()
        .and_then(|value| value.trim().parse().ok())
        .unwrap_or(default)
}

fn outbound_proxy_configured() -> bool {
    env::var("SAUCEDUST_OUTBOUND_PROXY")
        .ok()
        .map(|value| !value.trim().is_empty())
        .unwrap_or(false)
}

fn direct_fallback_enabled() -> bool {
    env_bool("SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE", true)
}

fn enable_direct_fallback(message: &str) {
    crate::ui::warn(message);
    env::remove_var("SAUCEDUST_OUTBOUND_PROXY");
    env::set_var("SAUCEDUST_DIRECT_FALLBACK_ACTIVE", "1");
}

fn env_state(key: &str) -> &'static str {
    match env::var(key).ok().map(|value| value.trim().to_owned()) {
        None => "없음",
        Some(value) if value.is_empty() => "비어 있음",
        Some(_) => "설정됨",
    }
}

fn print_proxy_env(config: &VpnConfig) {
    crate::ui::info("saucedust 외부 요청만 VPN 프록시를 사용하려면 아래 값을 .env에 둡니다.");
    crate::ui::command(&format!(
        "SAUCEDUST_OUTBOUND_PROXY=http://127.0.0.1:{}",
        config.proxy_port
    ));
    crate::ui::command("SAUCEDUST_VPN_AUTOSTART=1");
}

fn resolve_proxy_port(preferred: u16) -> anyhow::Result<u16> {
    if port_is_available(preferred) {
        return Ok(preferred);
    }
    let fallback = available_port()?;
    crate::ui::warn(&format!(
        "localhost:{preferred} 포트가 이미 사용 중입니다. 이번 실행에서는 localhost:{fallback} 포트를 사용합니다."
    ));
    Ok(fallback)
}

fn port_is_available(port: u16) -> bool {
    TcpListener::bind(("127.0.0.1", port)).is_ok()
}

fn available_port() -> anyhow::Result<u16> {
    let listener = TcpListener::bind(("127.0.0.1", 0))?;
    Ok(listener.local_addr()?.port())
}

fn apply_proxy_env(config: &VpnConfig) {
    env::set_var(
        "SAUCEDUST_OUTBOUND_PROXY",
        format!("http://127.0.0.1:{}", config.proxy_port),
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_config(proxy_port: u16) -> VpnConfig {
        let ovpn_path = PathBuf::from("/tmp/client.ovpn");
        VpnConfig {
            ovpn_path: ovpn_path.clone(),
            ovpn_paths: vec![ovpn_path],
            username: "user".to_owned(),
            password: "pass".to_owned(),
            dir: PathBuf::from("/tmp/saucedust-vpn"),
            proxy_port,
        }
    }

    #[test]
    fn lima_yaml_uses_alpine_and_selected_proxy_port() {
        let yaml = lima_yaml(&test_config(32123));
        assert!(yaml.contains("template:_images/alpine"));
        assert!(yaml.contains("memory: 512MiB"));
        assert!(yaml.contains("disk: 2GiB"));
        assert!(yaml.contains("containerd:\n  system: false\n  user: false"));
        assert!(yaml.contains("hostPort: 32123"));
        assert!(yaml.contains("apk add --no-cache openvpn tinyproxy curl ca-certificates procps"));
    }

    #[test]
    fn resolve_proxy_port_falls_back_when_preferred_port_is_busy() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let busy_port = listener.local_addr().unwrap().port();
        let selected = resolve_proxy_port(busy_port).unwrap();
        assert_ne!(selected, busy_port);
    }

    #[test]
    fn detects_openvpn_auth_failed_log() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dir.path().join("logs")).unwrap();
        fs::write(
            dir.path().join("logs/openvpn.log"),
            "AUTH: Received control message: AUTH_FAILED",
        )
        .unwrap();
        let mut config = test_config(32123);
        config.dir = dir.path().to_path_buf();
        assert!(openvpn_auth_failed(&config));
    }

    #[test]
    fn rotates_candidates_after_active_path() {
        let first = PathBuf::from("/tmp/a.ovpn");
        let second = PathBuf::from("/tmp/b.ovpn");
        let third = PathBuf::from("/tmp/c.ovpn");
        let mut config = test_config(32123);
        config.ovpn_path = second.clone();
        config.ovpn_paths = vec![first.clone(), second, third.clone()];
        let dir = tempfile::tempdir().unwrap();
        config.dir = dir.path().to_path_buf();
        fs::write(
            config.dir.join(ACTIVE_OVPN_PATH),
            first.to_string_lossy().as_ref(),
        )
        .unwrap();
        let candidates = config.candidates_from_active(true);
        assert_eq!(candidates[0], PathBuf::from("/tmp/b.ovpn"));
        assert_eq!(candidates[1], third);
    }
}
