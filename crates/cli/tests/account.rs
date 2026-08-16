//! Integration tests for `account` dispatch against a mock server.
//!
//! Asserts URL paths/methods and the `--yes` guard on destructive
//! 2FA / OAuth unlink operations.

use httpmock::prelude::*;
use newapi_cli_lib::client::ApiClient;
use newapi_cli_lib::cmd::account::AccountCommand;
use serde_json::json;

fn client_for(server: &MockServer) -> ApiClient {
    ApiClient::new(server.base_url(), "t".into()).unwrap()
}

fn d(client: &ApiClient, cmd: &AccountCommand) -> serde_json::Value {
    newapi_cli_lib::cmd::account::dispatch(client, cmd).unwrap()
}

#[test]
fn status_hits_user_self() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/user/self");
        then.status(200)
            .json_body(json!({"success": true, "data": {"id": 1}}));
    });
    d(&client_for(&server), &AccountCommand::Status);
    m.assert();
}

#[test]
fn update_uses_put() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/user/self");
        then.status(200).json_body(json!({"success": true}));
    });
    let put_m = server.mock(|when, then| {
        when.method(PUT).path("/api/user/self");
        then.status(200).json_body(json!({"success": true}));
    });
    // Status uses GET; update uses PUT on the same path. Drop status mock
    // and run update separately:
    drop(m);
    let body = json!({"display_name": "x"}).to_string();
    d(&client_for(&server), &AccountCommand::Update { json: body });
    put_m.assert();
}

#[test]
fn change_password_hits_change_password_path() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(POST)
            .path("/api/user/self/change_password")
            .json_body(json!({"old_password": "a", "new_password": "b"}));
        then.status(200).json_body(json!({"success": true}));
    });
    let body = json!({"old_password": "a", "new_password": "b"}).to_string();
    d(
        &client_for(&server),
        &AccountCommand::ChangePassword { json: body },
    );
    m.assert();
}

#[test]
fn disable_2fa_without_yes_sends_no_http() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(DELETE).path("/api/user/self/2fa");
        then.status(200).json_body(json!({"success": true}));
    });
    let res = newapi_cli_lib::cmd::account::dispatch(
        &client_for(&server),
        &AccountCommand::Disable2fa { yes: false },
    );
    assert!(res.is_err());
    assert_eq!(m.hits(), 0);
}

#[test]
fn disable_2fa_with_yes_hits_endpoint() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(DELETE).path("/api/user/self/2fa");
        then.status(200).json_body(json!({"success": true}));
    });
    d(
        &client_for(&server),
        &AccountCommand::Disable2fa { yes: true },
    );
    m.assert();
}

#[test]
fn oauth_list_hits_self_oauth() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/user/self/oauth");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(&client_for(&server), &AccountCommand::Oauth);
    m.assert();
}

#[test]
fn oauth_unlink_uses_provider_path_and_yes() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(DELETE).path("/api/user/self/oauth/wechat");
        then.status(200).json_body(json!({"success": true}));
    });
    d(
        &client_for(&server),
        &AccountCommand::UnlinkOauth {
            provider: "wechat".into(),
            yes: true,
        },
    );
    m.assert();
}

#[test]
fn topup_history_hits_topup_self() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/topup/self");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(
        &client_for(&server),
        &AccountCommand::TopupHistory {
            p: None,
            page_size: None,
        },
    );
    m.assert();
}

#[test]
fn topup_history_forwards_pagination() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET)
            .path("/api/topup/self")
            .query_param("p", "3")
            .query_param("page_size", "15");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(
        &client_for(&server),
        &AccountCommand::TopupHistory {
            p: Some(3),
            page_size: Some(15),
        },
    );
    m.assert();
}

#[test]
fn topup_posts_to_topup_self() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(POST)
            .path("/api/topup/self")
            .json_body(json!({"amount": 100, "payment_method": "card"}));
        then.status(200).json_body(json!({"success": true}));
    });
    let body = json!({"amount": 100, "payment_method": "card"}).to_string();
    d(&client_for(&server), &AccountCommand::Topup { json: body });
    m.assert();
}

#[test]
fn account_never_hits_admin_user_root() {
    // No mock on /api/user/1 — if any admin path were reached, dispatch
    // would error and the test would fail.
    let server = MockServer::start();
    let self_m = server.mock(|when, then| {
        when.method(GET).path("/api/user/self");
        then.status(200)
            .json_body(json!({"success": true, "data": {"id": 1}}));
    });
    d(&client_for(&server), &AccountCommand::Status);
    self_m.assert();
}
