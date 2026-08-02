use std::sync::LazyLock;
use std::time::Duration;
use std::{env, fs, path::PathBuf, sync::Arc, time::SystemTime};

use reqwest::StatusCode;
use sauce_core::errors::{SauceError, SauceResult};
use tokio::process::Command;
use tokio::sync::{Mutex, RwLock};
use tracing::debug;

const MAX_IMAGE_BYTES: u64 = 32 * 1024 * 1024;
static VPN_ROTATE_LOCK: LazyLock<Mutex<()>> = LazyLock::new(|| Mutex::new(()));

#[derive(Clone)]
pub struct ImageDownloader {
    client: Arc<RwLock<reqwest::Client>>,
    client_built_at: Arc<RwLock<SystemTime>>,
    user_agent: String,
    timeout: Duration,
}

impl ImageDownloader {
    pub fn new(user_agent: impl Into<String>, timeout: Duration) -> SauceResult<Self> {
        let user_agent = user_agent.into();
        let client = Self::build_client(&user_agent, timeout)?;

        Ok(Self {
            client: Arc::new(RwLock::new(client)),
            client_built_at: Arc::new(RwLock::new(SystemTime::now())),
            user_agent,
            timeout,
        })
    }

    pub async fn download(&self, url: &str) -> SauceResult<Vec<u8>> {
        debug!("image download url={url}");
        let rotate_attempts = env_u64("SAUCEDUST_VPN_ROTATE_ATTEMPTS", 3).max(1);
        let mut attempt = 0u64;
        loop {
            let client = self.current_client().await;
            match client.get(url).send().await {
                Ok(response) if response.status().is_success() => {
                    if let Some(length) = response.content_length() {
                        if length > MAX_IMAGE_BYTES {
                            return Err(SauceError::InvalidInput(format!(
                                "image is too large: {length}"
                            )));
                        }
                    }

                    match response.bytes().await {
                        Ok(bytes) => {
                            if bytes.len() as u64 > MAX_IMAGE_BYTES {
                                return Err(SauceError::InvalidInput(format!(
                                    "image is too large: {}",
                                    bytes.len()
                                )));
                            }
                            return Ok(bytes.to_vec());
                        }
                        Err(error) if should_rotate_vpn(url, &error) => {
                            if attempt >= rotate_attempts {
                                return Err(SauceError::Http(error));
                            }
                            if self.retry_after_vpn_rotation(url, attempt).await? {
                                attempt = attempt.saturating_add(1);
                                continue;
                            }
                            return Err(SauceError::Http(error));
                        }
                        Err(error) => return Err(SauceError::Http(error)),
                    }
                }
                Ok(response) if should_rotate_vpn_for_status(url, response.status()) => {
                    let error = response.error_for_status().unwrap_err();
                    if attempt >= rotate_attempts {
                        return Err(SauceError::Http(error));
                    }
                    if self.retry_after_vpn_rotation(url, attempt).await? {
                        attempt = attempt.saturating_add(1);
                        continue;
                    } else {
                        return Err(SauceError::Http(error));
                    }
                }
                Ok(response) => return Err(response.error_for_status().unwrap_err().into()),
                Err(error) if should_rotate_vpn(url, &error) => {
                    if attempt >= rotate_attempts {
                        return Err(SauceError::Http(error));
                    }
                    if self.retry_after_vpn_rotation(url, attempt).await? {
                        attempt = attempt.saturating_add(1);
                        continue;
                    } else {
                        return Err(SauceError::Http(error));
                    }
                }
                Err(error) => return Err(SauceError::Http(error)),
            }
        }
    }

    fn build_client(user_agent: &str, timeout: Duration) -> SauceResult<reqwest::Client> {
        Ok(crate::http_client::builder(Some(user_agent.to_owned()), timeout)?.build()?)
    }

    async fn current_client(&self) -> reqwest::Client {
        self.client.read().await.clone()
    }

    async fn rebuild_client(&self) -> SauceResult<()> {
        sync_active_proxy_env();
        let client = Self::build_client(&self.user_agent, self.timeout)?;
        *self.client.write().await = client;
        *self.client_built_at.write().await = SystemTime::now();
        Ok(())
    }

    async fn retry_after_vpn_rotation(&self, url: &str, attempt: u64) -> SauceResult<bool> {
        if sauce_core::outbound_proxy::direct_fallback_active() {
            return Ok(false);
        }

        if self.rebuild_if_rotation_happened_after_client().await? {
            println!("VPN 서버 전환 감지: 새 연결로 다운로드 재시도");
            return Ok(true);
        }

        let force = attempt > 0 || env_bool("SAUCEDUST_VPN_FORCE_ROTATE_ON_DOWNLOAD_ERROR", true);
        if rotate_vpn_if_needed(url, force)
            .await
            .should_retry_download()
        {
            self.rebuild_client().await?;
            return Ok(true);
        }

        if sauce_core::outbound_proxy::direct_fallback_enabled()
            && sauce_core::outbound_proxy::proxy_configured()
        {
            println!("직접망 fallback 전환: VPN 프록시 실패로 현재 네트워크로 다운로드 재시도");
            sauce_core::outbound_proxy::activate_direct_fallback();
            self.rebuild_client().await?;
            return Ok(true);
        }

        Ok(false)
    }

    async fn rebuild_if_rotation_happened_after_client(&self) -> SauceResult<bool> {
        if sauce_core::outbound_proxy::direct_fallback_active() {
            return Ok(false);
        }

        let built_at = *self.client_built_at.read().await;
        if successful_rotation_after(built_at) {
            self.rebuild_client().await?;
            return Ok(true);
        }
        Ok(false)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum VpnRotateOutcome {
    Rotated,
    RecentlyRotated,
    Unavailable,
}

impl VpnRotateOutcome {
    fn should_retry_download(self) -> bool {
        matches!(self, Self::Rotated | Self::RecentlyRotated)
    }
}

fn should_rotate_vpn_for_status(url: &str, status: StatusCode) -> bool {
    env_bool("SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR", true)
        && url.contains("donmai.us")
        && matches!(
            status,
            StatusCode::TOO_MANY_REQUESTS | StatusCode::FORBIDDEN
        )
}

fn should_rotate_vpn(url: &str, error: &reqwest::Error) -> bool {
    if !env_bool("SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR", true) {
        return false;
    }
    if !url.contains("donmai.us") {
        return false;
    }
    error.is_connect() || error.is_timeout() || error.to_string().contains("error sending request")
}

async fn rotate_vpn_if_needed(url: &str, force: bool) -> VpnRotateOutcome {
    let _guard = VPN_ROTATE_LOCK.lock().await;
    let Some(marker_path) = rotate_marker_path() else {
        return VpnRotateOutcome::Unavailable;
    };
    let cooldown = Duration::from_secs(env_u64("SAUCEDUST_VPN_ROTATE_COOLDOWN_SECS", 10).max(1));
    if !force {
        if let Some(success) = rotate_cooldown_state(&marker_path, cooldown) {
            return if success {
                VpnRotateOutcome::RecentlyRotated
            } else {
                VpnRotateOutcome::Unavailable
            };
        }
    }
    if let Some(parent) = marker_path.parent() {
        let _ = fs::create_dir_all(parent);
    }
    println!("VPN 서버 전환 시작: 이미지 CDN 연결 오류 감지");
    let Ok(exe) = env::current_exe() else {
        println!("VPN 서버 전환 건너뜀: 현재 실행 파일 확인 실패");
        write_rotate_marker(&marker_path, false);
        return VpnRotateOutcome::Unavailable;
    };
    let executable_name = exe
        .file_stem()
        .and_then(|name| name.to_str())
        .unwrap_or_default();
    if executable_name != "saucedust" {
        println!("VPN 서버 전환 건너뜀: saucedust 단일 바이너리 실행이 아닙니다");
        write_rotate_marker(&marker_path, false);
        return VpnRotateOutcome::Unavailable;
    }
    let status = Command::new(exe)
        .args(["vpn", "rotate"])
        .env("SAUCEDUST_VPN_HEALTHCHECK_URL", url)
        .status()
        .await
        .ok()
        .filter(|status| status.success());
    if status.is_some() {
        write_rotate_marker(&marker_path, true);
        println!("VPN 서버 전환 완료: 다운로드 재시도");
        VpnRotateOutcome::Rotated
    } else {
        write_rotate_marker(&marker_path, false);
        println!("VPN 서버 전환 실패: 기존 오류를 반환합니다");
        VpnRotateOutcome::Unavailable
    }
}

fn rotate_cooldown_state(path: &PathBuf, cooldown: Duration) -> Option<bool> {
    let Ok(metadata) = fs::metadata(path) else {
        return None;
    };
    let Ok(modified) = metadata.modified() else {
        return None;
    };
    if modified
        .elapsed()
        .map(|elapsed| elapsed >= cooldown)
        .unwrap_or(true)
    {
        return None;
    }
    let success = fs::read_to_string(path)
        .map(|value| value.lines().next() == Some("success"))
        .unwrap_or(false);
    Some(success)
}

fn successful_rotation_after(since: SystemTime) -> bool {
    let Some(path) = rotate_marker_path() else {
        return false;
    };
    let Ok(metadata) = fs::metadata(&path) else {
        return false;
    };
    let Ok(modified) = metadata.modified() else {
        return false;
    };
    if modified <= since {
        return false;
    }
    fs::read_to_string(&path)
        .map(|value| value.lines().next() == Some("success"))
        .unwrap_or(false)
}

fn write_rotate_marker(path: &PathBuf, success: bool) {
    let status = if success { "success" } else { "failed" };
    let _ = fs::write(path, format!("{status}\n{:?}", SystemTime::now()));
}

fn sync_active_proxy_env() {
    if sauce_core::outbound_proxy::direct_fallback_active() {
        return;
    }

    let Some(path) = active_proxy_port_path() else {
        return;
    };
    let Ok(port) = fs::read_to_string(path) else {
        return;
    };
    let port = port.trim();
    if port.is_empty() {
        return;
    }
    let proxy = format!("http://127.0.0.1:{port}");
    if env::var("SAUCEDUST_OUTBOUND_PROXY")
        .ok()
        .as_deref()
        .map(str::trim)
        != Some(proxy.as_str())
    {
        env::set_var("SAUCEDUST_OUTBOUND_PROXY", proxy);
    }
}

fn active_proxy_port_path() -> Option<PathBuf> {
    if let Ok(value) = env::var("SAUCEDUST_VPN_DATA_DIR") {
        let value = value.trim();
        if !value.is_empty() {
            return Some(PathBuf::from(value).join("active-proxy-port"));
        }
    }
    if let Ok(value) = env::var("SAUCE_ENGINE_DATA_DIR") {
        let value = value.trim();
        if !value.is_empty() {
            return Some(PathBuf::from(value).join("vpn").join("active-proxy-port"));
        }
    }
    env::var("HOME").ok().map(|home| {
        PathBuf::from(home)
            .join(".saucedust")
            .join("vpn")
            .join("active-proxy-port")
    })
}

fn rotate_marker_path() -> Option<PathBuf> {
    if let Ok(value) = env::var("SAUCEDUST_VPN_ROTATE_STATE_PATH") {
        let value = value.trim();
        if !value.is_empty() {
            return Some(PathBuf::from(value));
        }
    }
    if let Ok(value) = env::var("SAUCEDUST_VPN_DATA_DIR") {
        let value = value.trim();
        if !value.is_empty() {
            return Some(PathBuf::from(value).join("last-rotate-at"));
        }
    }
    if let Ok(value) = env::var("SAUCE_ENGINE_DATA_DIR") {
        let value = value.trim();
        if !value.is_empty() {
            return Some(PathBuf::from(value).join("vpn").join("last-rotate-at"));
        }
    }
    env::var("HOME").ok().map(|home| {
        PathBuf::from(home)
            .join(".saucedust")
            .join("vpn")
            .join("last-rotate-at")
    })
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rotates_on_donmai_rate_limit_status() {
        env::set_var("SAUCEDUST_VPN_ROTATE_ON_DOWNLOAD_ERROR", "1");
        assert!(should_rotate_vpn_for_status(
            "https://cdn.donmai.us/sample/a.jpg",
            StatusCode::TOO_MANY_REQUESTS
        ));
        assert!(should_rotate_vpn_for_status(
            "https://cdn.donmai.us/sample/a.jpg",
            StatusCode::FORBIDDEN
        ));
        assert!(!should_rotate_vpn_for_status(
            "https://example.com/a.jpg",
            StatusCode::TOO_MANY_REQUESTS
        ));
    }

    #[test]
    fn retries_download_after_recent_rotation() {
        assert!(VpnRotateOutcome::Rotated.should_retry_download());
        assert!(VpnRotateOutcome::RecentlyRotated.should_retry_download());
        assert!(!VpnRotateOutcome::Unavailable.should_retry_download());
    }
}
