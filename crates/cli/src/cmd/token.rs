//! `token` command — API token management (user and admin surfaces).
//!
//! Destructive operations require `--yes`. Token strings only flow through
//! the server's create response and are never echoed in error output.

use anyhow::{bail, Result};
use clap::Subcommand;
use serde_json::Value;

use crate::client::ApiClient;
use crate::json_input::read_json_arg;
use crate::output::print_json;

const TOKEN: &str = "/api/token";

#[derive(Subcommand)]
pub enum TokenCommand {
    /// List tokens for the current user
    List {
        #[arg(long)]
        p: Option<u32>,
        #[arg(long)]
        page_size: Option<u32>,
    },
    /// Search tokens by keyword (admin)
    Search {
        keyword: String,
        #[arg(long)]
        p: Option<u32>,
        #[arg(long)]
        page_size: Option<u32>,
    },
    /// GET /api/token/:id
    Get { id: i32 },
    /// POST /api/token — body must include name, group, expires_at, etc.
    Create { json: String },
    /// PATCH /api/token — update fields (status, expired_at, remain_quota,
    /// unlimited_quota, model_limits, group, name, etc.)
    Update { id: i32, json: String },
    /// DELETE /api/token/:id (requires --yes)
    Delete {
        id: i32,
        #[arg(long)]
        yes: bool,
    },
}

pub fn run(client: &ApiClient, cmd: &TokenCommand) -> Result<()> {
    let result = dispatch(client, cmd)?;
    print_json(&result)?;
    Ok(())
}

pub fn dispatch(client: &ApiClient, cmd: &TokenCommand) -> Result<Value> {
    match cmd {
        TokenCommand::List { p, page_size } => {
            let mut q: Vec<(&str, String)> = Vec::new();
            if let Some(p) = p {
                q.push(("p", p.to_string()));
            }
            if let Some(ps) = page_size {
                q.push(("page_size", ps.to_string()));
            }
            client.get(TOKEN, &q)
        }
        TokenCommand::Search {
            keyword,
            p,
            page_size,
        } => {
            let mut q = vec![("keyword", keyword.clone())];
            if let Some(p) = p {
                q.push(("p", p.to_string()));
            }
            if let Some(ps) = page_size {
                q.push(("page_size", ps.to_string()));
            }
            client.get(&format!("{}/search", TOKEN), &q)
        }
        TokenCommand::Get { id } => client.get(&format!("{}/{}", TOKEN, id), &[]),
        TokenCommand::Create { json } => {
            let body = read_json_arg(json)?;
            client.post_json(TOKEN, &body)
        }
        TokenCommand::Update { id, json } => {
            let body = read_json_arg(json)?;
            client.patch_json(&format!("{}/{}", TOKEN, id), &body)
        }
        TokenCommand::Delete { id, yes } => {
            if !*yes {
                bail!("token delete requires --yes");
            }
            client.delete(&format!("{}/{}", TOKEN, id))
        }
    }
}
