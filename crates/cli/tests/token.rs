//! Integration tests for `token` dispatch against a mock server.
//!
//! Asserts URL paths/methods and the `--yes` guard on delete.

use httpmock::prelude::*;
use newapi_cli_lib::client::ApiClient;
use newapi_cli_lib::cmd::token::TokenCommand;
use serde_json::json;

fn client_for(server: &MockServer) -> ApiClient {
    ApiClient::new(server.base_url(), "t".into()).unwrap()
}

fn d(client: &ApiClient, cmd: &TokenCommand) -> serde_json::Value {
    newapi_cli_lib::cmd::token::dispatch(client, cmd).unwrap()
}

#[test]
fn token_list_uses_token_root() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/token");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(
        &client_for(&server),
        &TokenCommand::List {
            p: None,
            page_size: None,
        },
    );
    m.assert();
}

#[test]
fn token_list_forwards_pagination() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET)
            .path("/api/token")
            .query_param("p", "2")
            .query_param("page_size", "25");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(
        &client_for(&server),
        &TokenCommand::List {
            p: Some(2),
            page_size: Some(25),
        },
    );
    m.assert();
}

#[test]
fn token_search_forwards_keyword() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET)
            .path("/api/token/search")
            .query_param("keyword", "alice");
        then.status(200)
            .json_body(json!({"success": true, "data": []}));
    });
    d(
        &client_for(&server),
        &TokenCommand::Search {
            keyword: "alice".into(),
            p: None,
            page_size: None,
        },
    );
    m.assert();
}

#[test]
fn token_get_hits_id() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(GET).path("/api/token/7");
        then.status(200)
            .json_body(json!({"success": true, "data": {"id": 7}}));
    });
    d(&client_for(&server), &TokenCommand::Get { id: 7 });
    m.assert();
}

#[test]
fn token_create_posts_json() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(POST)
            .path("/api/token")
            .json_body(json!({"name": "tk", "group": "default"}));
        then.status(200)
            .json_body(json!({"success": true, "data": {"key": "sk-xxx"}}));
    });
    let body = json!({"name": "tk", "group": "default"}).to_string();
    d(&client_for(&server), &TokenCommand::Create { json: body });
    m.assert();
}

#[test]
fn token_update_uses_patch() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(httpmock::Method::PATCH)
            .path("/api/token/3")
            .json_body(json!({"status": 1}));
        then.status(200).json_body(json!({"success": true}));
    });
    let body = json!({"status": 1}).to_string();
    d(
        &client_for(&server),
        &TokenCommand::Update { id: 3, json: body },
    );
    m.assert();
}

#[test]
fn token_delete_without_yes_sends_no_http() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(DELETE).path("/api/token/1");
        then.status(200).json_body(json!({"success": true}));
    });
    let res = newapi_cli_lib::cmd::token::dispatch(
        &client_for(&server),
        &TokenCommand::Delete { id: 1, yes: false },
    );
    assert!(res.is_err());
    assert_eq!(m.hits(), 0);
}

#[test]
fn token_delete_with_yes_hits_endpoint() {
    let server = MockServer::start();
    let m = server.mock(|when, then| {
        when.method(DELETE).path("/api/token/1");
        then.status(200).json_body(json!({"success": true}));
    });
    d(
        &client_for(&server),
        &TokenCommand::Delete { id: 1, yes: true },
    );
    m.assert();
}
