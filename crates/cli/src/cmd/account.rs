//! `account` command — self-service profile, password, 2FA, OAuth link,
//! and topup for the calling user.
//!
//! All paths are scoped to `/api/user/self/*` and `/api/topup/*`. The
//! admin `/api/user/:id` surface is unreachable from this command.

use anyhow::{bail, Result};
use clap::Subcommand;
use serde_json::Value;

use crate::client::ApiClient;
use crate::json_input::read_json_arg;
use crate::output::print_json;

const SELF: &str = "/api/user/self";
const TOPUP: &str = "/api/topup";

#[derive(Subcommand)]
pub enum AccountCommand {
    /// GET /api/user/self — caller profile
    Status,
    /// PUT /api/user/self — update display name, email, etc.
    /// Sensitive fields (password, OAuth credentials) only flow through
    /// their dedicated subcommands.
    Update {
        json: String,
    },
    /// POST /api/user/self/change_password — body {old_password,new_password}
    ChangePassword {
        json: String,
    },
    /// POST /api/user/self/2fa/setup — start 2FA enrolment
    Setup2fa,
    /// POST /api/user/self/2fa (requires --yes to commit the response)
    Enable2fa {
        json: String,
        #[arg(long)]
        yes: bool,
    },
    /// DELETE /api/user/self/2fa (requires --yes)
    Disable2fa {
        #[arg(long)]
        yes: bool,
    },
    /// GET /api/user/self/oauth — list OAuth bindings for the caller
    Oauth,
    /// POST /api/user/self/oauth — body {provider, code, redirect_uri, state}
    LinkOauth {
        json: String,
    },
    /// DELETE /api/user/self/oauth/:provider (requires --yes)
    UnlinkOauth {
        provider: String,
        #[arg(long)]
        yes: bool,
    },
    /// GET /api/topup/self — caller topup history
    TopupHistory {
        #[arg(long)]
        p: Option<u32>,
        #[arg(long)]
        page_size: Option<u32>,
    },
    /// POST /api/topup/self — body {amount, payment_method}
    Topup {
        json: String,
    },
}

pub fn run(client: &ApiClient, cmd: &AccountCommand) -> Result<()> {
    let result = dispatch(client, cmd)?;
    print_json(&result)?;
    Ok(())
}

pub fn dispatch(client: &ApiClient, cmd: &AccountCommand) -> Result<Value> {
    match cmd {
        AccountCommand::Status => client.get(SELF, &[]),
        AccountCommand::Update { json } => {
            let body = read_json_arg(json)?;
            client.put_json(SELF, &body)
        }
        AccountCommand::ChangePassword { json } => {
            let body = read_json_arg(json)?;
            client.post_json(&format!("{}/change_password", SELF), &body)
        }
        AccountCommand::Setup2fa => client.post_json(&format!("{}/2fa/setup", SELF), &Value::Null)
            .or_else(|_| client.post_json(&format!("{}/2fa/setup", SELF), &Value::Object(Default::default()))),
        AccountCommand::Enable2fa { json, yes } => {
            if !*yes {
                bail!("2fa enable requires --yes");
            }
            let body = read_json_arg(json)?;
            client.post_json(&format!("{}/2fa", SELF), &body)
        }
        AccountCommand::Disable2fa { yes } => {
            if !*yes {
                bail!("2fa disable requires --yes");
            }
            client.delete(&format!("{}/2fa", SELF))
        }
        AccountCommand::Oauth => client.get(&format!("{}/oauth", SELF), &[]),
        AccountCommand::LinkOauth { json } => {
            let body = read_json_arg(json)?;
            client.post_json(&format!("{}/oauth", SELF), &body)
        }
        AccountCommand::UnlinkOauth { provider, yes } => {
            if !*yes {
                bail!("oauth unlink requires --yes");
            }
            client.delete(&format!("{}/oauth/{}", SELF, provider))
        }
        AccountCommand::TopupHistory { p, page_size } => {
            let mut q: Vec<(&str, String)> = Vec::new();
            if let Some(p) = p {
                q.push(("p", p.to_string()));
            }
            if let Some(ps) = page_size {
                q.push(("page_size", ps.to_string()));
            }
            client.get(&format!("{}/self", TOPUP), &q)
        }
        AccountCommand::Topup { json } => {
            let body = read_json_arg(json)?;
            client.post_json(&format!("{}/self", TOPUP), &body)
        }
    }
}
