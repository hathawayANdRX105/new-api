//! `usage` command — user log retrieval and quota status for the calling
//! user's own activity.
//!
//! This command only hits `/api/log/self` paths so non-admin users can
//! audit their own usage without server-side admin privileges.

use anyhow::{bail, Result};
use clap::Subcommand;
use serde_json::Value;

use crate::client::ApiClient;
use crate::output::print_json;

const SELF: &str = "/api/log/self";

#[derive(Subcommand)]
pub enum UsageCommand {
    /// GET /api/log/self?p=&page_size= — paginated usage entries
    List {
        #[arg(long)]
        p: Option<u32>,
        #[arg(long)]
        page_size: Option<u32>,
        #[arg(long)]
        start_timestamp: Option<i64>,
        #[arg(long)]
        end_timestamp: Option<i64>,
        #[arg(long)]
        model_name: Option<String>,
        #[arg(long)]
        r#type: Option<i32>,
        #[arg(long)]
        request_id: Option<String>,
        #[arg(long)]
        upstream_request_id: Option<String>,
    },
    /// GET /api/log/self/stat — quota summary for the caller
    Stat {
        #[arg(long)]
        start_timestamp: Option<i64>,
        #[arg(long)]
        end_timestamp: Option<i64>,
        #[arg(long)]
        model_name: Option<String>,
    },
    /// GET /api/log/self/token — token-level usage breakdown
    Token {
        #[arg(long)]
        start_timestamp: Option<i64>,
        #[arg(long)]
        end_timestamp: Option<i64>,
    },
    /// GET /api/log/self/models — model-level usage breakdown
    Models {
        #[arg(long)]
        start_timestamp: Option<i64>,
        #[arg(long)]
        end_timestamp: Option<i64>,
    },
}

pub fn run(client: &ApiClient, cmd: &UsageCommand) -> Result<()> {
    let result = dispatch(client, cmd)?;
    print_json(&result)?;
    Ok(())
}

pub fn dispatch(client: &ApiClient, cmd: &UsageCommand) -> Result<Value> {
    match cmd {
        UsageCommand::List {
            p,
            page_size,
            start_timestamp,
            end_timestamp,
            model_name,
            r#type,
            request_id,
            upstream_request_id,
        } => client.get(
            SELF,
            &list_query(
                *p,
                *page_size,
                *start_timestamp,
                *end_timestamp,
                model_name.as_deref(),
                *r#type,
                request_id.as_deref(),
                upstream_request_id.as_deref(),
            ),
        ),
        UsageCommand::Stat {
            start_timestamp,
            end_timestamp,
            model_name,
        } => client.get(
            &format!("{}/stat", SELF),
            &stat_query(*start_timestamp, *end_timestamp, model_name.as_deref()),
        ),
        UsageCommand::Token {
            start_timestamp,
            end_timestamp,
        } => client.get(
            &format!("{}/token", SELF),
            &ts_query(*start_timestamp, *end_timestamp),
        ),
        UsageCommand::Models {
            start_timestamp,
            end_timestamp,
        } => client.get(
            &format!("{}/models", SELF),
            &ts_query(*start_timestamp, *end_timestamp),
        ),
    }
}

#[allow(clippy::too_many_arguments)]
fn list_query(
    p: Option<u32>,
    page_size: Option<u32>,
    start_timestamp: Option<i64>,
    end_timestamp: Option<i64>,
    model_name: Option<&str>,
    r#type: Option<i32>,
    request_id: Option<&str>,
    upstream_request_id: Option<&str>,
) -> Vec<(&'static str, String)> {
    let mut q: Vec<(&'static str, String)> = Vec::new();
    if let Some(p) = p {
        q.push(("p", p.to_string()));
    }
    if let Some(ps) = page_size {
        q.push(("page_size", ps.to_string()));
    }
    if let Some(s) = start_timestamp {
        q.push(("start_timestamp", s.to_string()));
    }
    if let Some(e) = end_timestamp {
        q.push(("end_timestamp", e.to_string()));
    }
    if let Some(m) = model_name {
        q.push(("model_name", m.to_string()));
    }
    if let Some(t) = r#type {
        q.push(("type", t.to_string()));
    }
    if let Some(r) = request_id {
        q.push(("request_id", r.to_string()));
    }
    if let Some(u) = upstream_request_id {
        q.push(("upstream_request_id", u.to_string()));
    }
    q
}

fn stat_query(
    start_timestamp: Option<i64>,
    end_timestamp: Option<i64>,
    model_name: Option<&str>,
) -> Vec<(&'static str, String)> {
    let mut q: Vec<(&'static str, String)> = Vec::new();
    if let Some(s) = start_timestamp {
        q.push(("start_timestamp", s.to_string()));
    }
    if let Some(e) = end_timestamp {
        q.push(("end_timestamp", e.to_string()));
    }
    if let Some(m) = model_name {
        q.push(("model_name", m.to_string()));
    }
    q
}

fn ts_query(
    start_timestamp: Option<i64>,
    end_timestamp: Option<i64>,
) -> Vec<(&'static str, String)> {
    let mut q: Vec<(&'static str, String)> = Vec::new();
    if let Some(s) = start_timestamp {
        q.push(("start_timestamp", s.to_string()));
    }
    if let Some(e) = end_timestamp {
        q.push(("end_timestamp", e.to_string()));
    }
    q
}

#[allow(dead_code)]
fn _unused_bail_anchor() {
    // Reserve bail import in case future variants require validation.
    let _ = || -> Result<()> { bail!("never") };
}
