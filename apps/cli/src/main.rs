//! newapi-cli binary entry — parse, dispatch, print only.

use anyhow::{Context, Result};
use clap::{Parser, Subcommand};

use newapi_cli_lib::brand::BRAND;
use newapi_cli_lib::client::ApiClient;
use newapi_cli_lib::cmd;
use newapi_cli_lib::config::load_config;

#[derive(Parser)]
#[command(name = BRAND.bin_name, bin_name = BRAND.bin_name, version, about = "agent-callable control plane for new-api")]
struct Cli {
    /// Override the base URL (otherwise ${BRAND.env_prefix}_BASE_URL or config file).
    #[arg(long, global = true)]
    url: Option<String>,

    /// Override the bearer token.
    #[arg(long, global = true)]
    token: Option<String>,

    #[command(subcommand)]
    command: TopLevel,
}

#[derive(Subcommand)]
enum TopLevel {
    /// Manage upstream channels (P0)
    #[command(subcommand)]
    Channel(cmd::channel::ChannelCommand),
    /// Manage the model catalog (P0)
    #[command(subcommand)]
    Catalog(cmd::catalog::CatalogCommand),
    /// Manage model and group pricing (P0)
    #[command(subcommand)]
    Pricing(cmd::pricing::PricingCommand),
    /// Manage proxy nodes and system instances (P1)
    #[command(subcommand)]
    Device(cmd::device::DeviceCommand),
    /// Manage admin performance, system tasks, and admin log (P1)
    #[command(subcommand)]
    System(cmd::system::SystemCommand),
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    let cfg =
        load_config(cli.url.as_deref(), cli.token.as_deref()).context("loading configuration")?;
    let client = ApiClient::new(cfg.base_url, cfg.token).context("building API client")?;

    match cli.command {
        TopLevel::Channel(cmd) => cmd::channel::run(&client, &cmd),
        TopLevel::Catalog(cmd) => cmd::catalog::run(&client, &cmd),
        TopLevel::Pricing(cmd) => cmd::pricing::run(&client, &cmd),
        TopLevel::Device(cmd) => cmd::device::run(&client, &cmd),
        TopLevel::System(cmd) => cmd::system::run(&client, &cmd),
    }
}
