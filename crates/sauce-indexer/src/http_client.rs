use std::env;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use hickory_resolver::config::{LookupIpStrategy, ResolverConfig};
use hickory_resolver::name_server::TokioConnectionProvider;
use hickory_resolver::TokioResolver;
use reqwest::dns::{Addrs, Name, Resolve, Resolving};
use sauce_core::errors::{SauceError, SauceResult};

pub fn builder(
    user_agent: Option<String>,
    timeout: Duration,
) -> SauceResult<reqwest::ClientBuilder> {
    let mut builder = base_builder(user_agent, timeout)?;
    builder = sauce_core::outbound_proxy::apply_env_proxy(builder)?;
    apply_dns_resolver(builder)
}

pub fn builder_without_proxy(
    user_agent: Option<String>,
    timeout: Duration,
) -> SauceResult<reqwest::ClientBuilder> {
    apply_dns_resolver(base_builder(user_agent, timeout)?)
}

fn base_builder(
    user_agent: Option<String>,
    timeout: Duration,
) -> SauceResult<reqwest::ClientBuilder> {
    let mut builder = reqwest::Client::builder().timeout(timeout).tls_sni(true);
    if let Some(user_agent) = user_agent {
        builder = builder.user_agent(user_agent);
    }
    Ok(builder)
}

fn apply_dns_resolver(builder: reqwest::ClientBuilder) -> SauceResult<reqwest::ClientBuilder> {
    match env::var("SAUCEDUST_DNS_RESOLVER")
        .unwrap_or_else(|_| "cloudflare".to_owned())
        .trim()
        .to_ascii_lowercase()
        .as_str()
    {
        "" | "system" | "default" => Ok(builder),
        "hickory" | "hickory-system" | "hickory-dns" | "trust-dns" => Ok(builder.hickory_dns(true)),
        "cloudflare" | "1.1.1.1" => Ok(builder.dns_resolver(Arc::new(HickoryPublicResolver::new(
            ResolverConfig::cloudflare(),
        )))),
        "google" | "8.8.8.8" => Ok(builder.dns_resolver(Arc::new(HickoryPublicResolver::new(
            ResolverConfig::google(),
        )))),
        value => Err(SauceError::Config(format!(
            "SAUCEDUST_DNS_RESOLVER: unsupported value {value}"
        ))),
    }
}

struct HickoryPublicResolver {
    resolver: TokioResolver,
}

impl HickoryPublicResolver {
    fn new(config: ResolverConfig) -> Self {
        let mut builder =
            TokioResolver::builder_with_config(config, TokioConnectionProvider::default());
        builder.options_mut().ip_strategy = LookupIpStrategy::Ipv4AndIpv6;
        Self {
            resolver: builder.build(),
        }
    }
}

impl Resolve for HickoryPublicResolver {
    fn resolve(&self, name: Name) -> Resolving {
        let resolver = self.resolver.clone();
        let name = name.as_str().to_owned();
        Box::pin(async move {
            let lookup = resolver.lookup_ip(name).await?;
            let addrs = lookup
                .iter()
                .map(|ip_addr| SocketAddr::new(ip_addr, 0))
                .collect::<Vec<_>>();
            Ok(Box::new(addrs.into_iter()) as Addrs)
        })
    }
}
