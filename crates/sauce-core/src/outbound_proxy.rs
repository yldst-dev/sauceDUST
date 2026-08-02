use std::env;

use crate::errors::{SauceError, SauceResult};

pub fn apply_env_proxy(builder: reqwest::ClientBuilder) -> SauceResult<reqwest::ClientBuilder> {
    if direct_fallback_active() {
        return Ok(builder);
    }

    let Some(proxy_url) = env::var("SAUCEDUST_OUTBOUND_PROXY")
        .ok()
        .map(|value| value.trim().to_owned())
        .filter(|value| !value.is_empty())
    else {
        return Ok(builder);
    };

    let proxy = reqwest::Proxy::all(&proxy_url)
        .map_err(|err| SauceError::Config(format!("SAUCEDUST_OUTBOUND_PROXY: {err}")))?;
    Ok(builder.proxy(proxy))
}

pub fn proxy_configured() -> bool {
    env::var("SAUCEDUST_OUTBOUND_PROXY")
        .ok()
        .map(|value| !value.trim().is_empty())
        .unwrap_or(false)
}

pub fn direct_fallback_enabled() -> bool {
    env_bool("SAUCEDUST_DIRECT_FALLBACK_ON_PROXY_FAILURE", true)
}

pub fn direct_fallback_active() -> bool {
    env_bool("SAUCEDUST_DIRECT_FALLBACK_ACTIVE", false)
}

pub fn activate_direct_fallback() {
    env::remove_var("SAUCEDUST_OUTBOUND_PROXY");
    env::set_var("SAUCEDUST_DIRECT_FALLBACK_ACTIVE", "1");
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
