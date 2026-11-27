use std::collections::BTreeMap;
use std::fmt;

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use ts_rs::TS;

use crate::ids::{BootId, Epoch, MailboxMessageId, PtyId, Seq, ThreadId, TimestampMs, TurnId};
use crate::lease::{HandoffReason, Lease, LeaseOwner, LeaseTransition};
use crate::mailbox::{
    MailboxAddress, MailboxMessage, MailboxMessageKind, MailboxPriority, MailboxStatus,
};
use crate::osc::{OscResumeHint, OscStatus};
use crate::thread::{Thread, ThreadDriver, ThreadKind, ThreadState, Turn, TurnOrigin};

/// JSON-RPC version string used on all UDS NDJSON-RPC 2.0 frames.
pub const JSONRPC_VERSION: &str = "2.0";

/// JSON-RPC 2.0 request/response correlation identifier.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(untagged)]
#[ts(export)]
pub enum RpcId {
    Number(#[ts(type = "number")] i64),
    String(String),
}

impl From<i64> for RpcId {
    fn from(value: i64) -> Self {
        Self::Number(value)
    }
}

impl From<String> for RpcId {
    fn from(value: String) -> Self {
        Self::String(value)
    }
}

impl From<&str> for RpcId {
    fn from(value: &str) -> Self {
        Self::String(value.to_owned())
    }
}

/// Standard JSON-RPC 2.0 and Spaces-specific numeric error codes.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, TS)]
#[ts(
    export,
    type = "-32700 | -32600 | -32601 | -32602 | -32603 | -32001 | -32002 | -32003 | -32004 | -32005"
)]
#[repr(i32)]
pub enum RpcErrorCode {
    ParseError = -32700,
    InvalidRequest = -32600,
    MethodNotFound = -32601,
    InvalidParams = -32602,
    InternalError = -32603,
    Unauthorized = -32001,
    StaleEpoch = -32002,
    NotFound = -32003,
    LeaseConflict = -32004,
    ReplayGap = -32005,
}

impl RpcErrorCode {
    /// Returns the numeric JSON-RPC error code.
    pub const fn code(self) -> i32 {
        self as i32
    }

    /// Resolves a numeric JSON-RPC error code to a known `RpcErrorCode`.
    pub const fn from_code(code: i32) -> Option<Self> {
        match code {
            -32700 => Some(Self::ParseError),
            -32600 => Some(Self::InvalidRequest),
            -32601 => Some(Self::MethodNotFound),
            -32602 => Some(Self::InvalidParams),
            -32603 => Some(Self::InternalError),
            -32001 => Some(Self::Unauthorized),
            -32002 => Some(Self::StaleEpoch),
            -32003 => Some(Self::NotFound),
            -32004 => Some(Self::LeaseConflict),
            -32005 => Some(Self::ReplayGap),
            _ => None,
        }
    }
}

impl From<RpcErrorCode> for i32 {
    fn from(value: RpcErrorCode) -> Self {
        value.code()
    }
}

impl TryFrom<i32> for RpcErrorCode {
    type Error = i32;

    fn try_from(value: i32) -> Result<Self, Self::Error> {
        Self::from_code(value).ok_or(value)
    }
}

impl fmt::Display for RpcErrorCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.code())
    }
}

// Serialize/Deserialize as i32 so JSON-RPC 2.0 error codes remain numeric on the wire.
impl Serialize for RpcErrorCode {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        serializer.serialize_i32(self.code())
    }
}

impl<'de> Deserialize<'de> for RpcErrorCode {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let code = i32::deserialize(deserializer)?;
        Self::from_code(code)
            .ok_or_else(|| serde::de::Error::custom(format!("unknown rpc error code: {code}")))
    }
}

/// JSON-RPC 2.0 error object.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcError {
    pub code: i32,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "unknown")]
    pub data: Option<serde_json::Value>,
}

impl RpcError {
    /// Builds an `RpcError` from a known `RpcErrorCode` and message.
    pub fn new(code: RpcErrorCode, message: impl Into<String>) -> Self {
        Self {
            code: code.code(),
            message: message.into(),
            data: None,
        }
    }

    /// Attaches structured error metadata to this `RpcError`.
    pub fn with_data(mut self, data: serde_json::Value) -> Self {
        self.data = Some(data);
        self
    }
}

/// JSON-RPC 2.0 request frame.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcRequest {
    pub jsonrpc: String,
    pub id: RpcId,
    pub method: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "unknown")]
    pub params: Option<serde_json::Value>,
}

impl RpcRequest {
    /// Builds a JSON-RPC 2.0 request with `"2.0"` version populated.
    pub fn new(
        id: impl Into<RpcId>,
        method: impl Into<String>,
        params: Option<serde_json::Value>,
    ) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: id.into(),
            method: method.into(),
            params,
        }
    }
}

/// JSON-RPC 2.0 response frame.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcResponse {
    pub jsonrpc: String,
    pub id: RpcId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "unknown")]
    pub result: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub error: Option<RpcError>,
}

impl RpcResponse {
    /// Builds a successful JSON-RPC 2.0 response.
    pub fn ok(id: impl Into<RpcId>, result: serde_json::Value) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: id.into(),
            result: Some(result),
            error: None,
        }
    }

    /// Builds an error JSON-RPC 2.0 response.
    pub fn err(id: impl Into<RpcId>, error: RpcError) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            id: id.into(),
            result: None,
            error: Some(error),
        }
    }
}

/// JSON-RPC 2.0 notification frame (no `id`).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcNotification {
    pub jsonrpc: String,
    pub method: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "unknown")]
    pub params: Option<serde_json::Value>,
}

impl RpcNotification {
    /// Builds a JSON-RPC 2.0 notification with `"2.0"` version populated.
    pub fn new(method: impl Into<String>, params: Option<serde_json::Value>) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.to_owned(),
            method: method.into(),
            params,
        }
    }
}

/// Untagged union of all NDJSON-RPC 2.0 wire frames.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(untagged)]
#[ts(export)]
pub enum RpcFrame {
    Request(RpcRequest),
    Response(RpcResponse),
    Notification(RpcNotification),
}

/// Role of a client connecting over UDS to `spacesd`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RpcClientKind {
    Cli,
    PtyHost,
    TmuxFrontend,
    McpBridge,
    BrowserCompanion,
    Tui,
}

/// Params for `rpc.auth` client handshake.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcAuthParams {
    pub protocol_version: u32,
    pub client_kind: RpcClientKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub client_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub token: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub label: Option<String>,
}

/// Result of `rpc.auth` handshake.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct RpcAuthResult {
    pub ok: bool,
    pub protocol_version: u32,
    pub boot_id: BootId,
    pub client_id: String,
    pub current_seq: Seq,
}

/// Result of `daemon.ping`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct DaemonPingResult {
    pub ok: bool,
    pub boot_id: BootId,
    pub ts_ms: TimestampMs,
}

/// Result of `daemon.status`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct DaemonStatusResult {
    pub boot_id: BootId,
    pub protocol_version: u32,
    pub pid: u32,
    pub started_at_ms: TimestampMs,
    #[ts(type = "number")]
    pub uptime_ms: u64,
    pub current_seq: Seq,
    pub active_threads: u32,
    pub active_ptys: u32,
    pub connected_clients: u32,
}

/// Params for `thread.spawn`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadSpawnParams {
    pub agent: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub profile: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub driver: Option<ThreadDriver>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub kind: Option<ThreadKind>,
    pub cwd: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub space: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub window_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub pane_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub worktree_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub vendor_session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub initial_prompt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub env: Option<BTreeMap<String, String>>,
}

/// Result of `thread.spawn`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadSpawnResult {
    pub thread: Thread,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub lease: Option<Lease>,
}

/// Params for `thread.list`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadListParams {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub space: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub state: Option<ThreadState>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub agent: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub driver: Option<ThreadDriver>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub include_closed: Option<bool>,
}

/// Result of `thread.list`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadListResult {
    pub threads: Vec<Thread>,
}

/// Params for `thread.get`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadGetParams {
    pub thread_id: ThreadId,
}

/// Params for `thread.close`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct ThreadCloseParams {
    pub thread_id: ThreadId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub epoch: Option<Epoch>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub force: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub reason: Option<String>,
}

/// Params for `turn.send`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct TurnSendParams {
    pub thread_id: ThreadId,
    pub epoch: Epoch,
    pub origin: TurnOrigin,
    pub prompt: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub idempotency_key: Option<String>,
}

/// Result of `turn.send`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct TurnSendResult {
    pub turn: Turn,
}

/// Params for `turn.cancel`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct TurnCancelParams {
    pub thread_id: ThreadId,
    pub turn_id: TurnId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub epoch: Option<Epoch>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub reason: Option<String>,
}

/// Params for `lease.claim`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseClaimParams {
    pub thread_id: ThreadId,
    pub owner: LeaseOwner,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub expected_epoch: Option<Epoch>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "number")]
    pub ttl_ms: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub reason: Option<HandoffReason>,
}

/// Params for `lease.yield`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseYieldParams {
    pub thread_id: ThreadId,
    pub epoch: Epoch,
    pub owner: LeaseOwner,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub reason: Option<HandoffReason>,
}

/// Params for `lease.handoff`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseHandoffParams {
    pub thread_id: ThreadId,
    pub from_epoch: Epoch,
    pub to_owner: LeaseOwner,
    pub reason: HandoffReason,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub from_owner: Option<LeaseOwner>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "number")]
    pub ttl_ms: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub cancel_active_turn: Option<bool>,
}

/// Params for `lease.status`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseStatusParams {
    pub thread_id: ThreadId,
}

/// Result returned by lease control RPC methods.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseResult {
    pub lease: Lease,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub transition: Option<LeaseTransition>,
}

/// Params for `mailbox.send`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxSendParams {
    pub thread_id: ThreadId,
    pub from: MailboxAddress,
    pub kind: MailboxMessageKind,
    pub priority: MailboxPriority,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub dedup_key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub correlation_id: Option<String>,
    #[ts(type = "unknown")]
    pub payload: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub expires_at_ms: Option<TimestampMs>,
}

/// Result of `mailbox.send`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxSendResult {
    pub message: MailboxMessage,
    pub deduplicated: bool,
}

/// Params for `mailbox.list`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxListParams {
    pub thread_id: ThreadId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub status: Option<MailboxStatus>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub kind: Option<MailboxMessageKind>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub limit: Option<u32>,
}

/// Result of `mailbox.list`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxListResult {
    pub messages: Vec<MailboxMessage>,
}

/// Params for `mailbox.ack`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxAckParams {
    pub thread_id: ThreadId,
    pub message_id: MailboxMessageId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub status: Option<MailboxStatus>,
}

/// Payload encoding used for PTY byte chunks over NDJSON-RPC 2.0.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum PtyEncoding {
    Base64,
}

/// Terminal dimensions for PTY spawn and SIGWINCH resize (`TIOCSWINSZ`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtySize {
    pub cols: u16,
    pub rows: u16,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub xpixel: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub ypixel: Option<u16>,
}

/// Params for `pty.spawn`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtySpawnParams {
    pub thread_id: ThreadId,
    pub argv: Vec<String>,
    pub cwd: String,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    pub size: PtySize,
}

/// Result of `pty.spawn`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtySpawnResult {
    pub pty_id: PtyId,
    pub pid: u32,
    pub pgid: u32,
}

/// Params for `pty.attach`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyAttachParams {
    pub pty_id: PtyId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "number")]
    pub after_offset: Option<u64>,
    pub request_replay: bool,
}

/// Result of `pty.attach`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyAttachResult {
    pub pty_id: PtyId,
    #[ts(type = "number")]
    pub current_offset: u64,
    pub replay_truncated: bool,
}

/// Params for `pty.detach`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyDetachParams {
    pub pty_id: PtyId,
}

/// Params for `pty.input`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyInputParams {
    pub pty_id: PtyId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub epoch: Option<Epoch>,
    pub encoding: PtyEncoding,
    pub data_b64: String,
}

/// Notification params for `pty.output`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyOutputNotification {
    pub pty_id: PtyId,
    #[ts(type = "number")]
    pub offset: u64,
    pub encoding: PtyEncoding,
    pub data_b64: String,
    pub ts_ms: TimestampMs,
}

/// Params for `pty.resize`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyResizeParams {
    pub pty_id: PtyId,
    pub size: PtySize,
}

/// Params for `pty.kill`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyKillParams {
    pub pty_id: PtyId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub signal: Option<String>,
}

/// Notification params for `pty.exit`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyExitNotification {
    pub pty_id: PtyId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub exit_code: Option<i32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub signal: Option<String>,
    pub ts_ms: TimestampMs,
}

/// Notification params for `pty.osc`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct PtyOscNotification {
    pub pty_id: PtyId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub status: Option<OscStatus>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub resume_hint: Option<OscResumeHint>,
    pub ts_ms: TimestampMs,
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;
    use crate::ids::LeaseId;
    use crate::lease::{LeaseOwnerKind, LeaseState};
    use crate::osc::OscState;
    use crate::thread::{ThreadState, TurnError, TurnState, TurnUsage};

    fn assert_ndjson_roundtrip<T>(value: &T) -> String
    where
        T: Serialize + for<'de> Deserialize<'de> + PartialEq + std::fmt::Debug,
    {
        let line = serde_json::to_string(value).expect("serialize to single-line JSON");
        assert!(!line.contains('\n'), "NDJSON line must not contain \\n");
        assert!(!line.contains('\r'), "NDJSON line must not contain \\r");

        let framed = format!("{line}\n{line}\n");
        let parsed: Vec<T> = framed
            .lines()
            .map(|l| serde_json::from_str(l).expect("deserialize NDJSON line"))
            .collect();
        assert_eq!(parsed.len(), 2);
        assert_eq!(&parsed[0], value);
        assert_eq!(&parsed[1], value);
        line
    }

    #[test]
    fn rpc_frame_untagged_roundtrip() {
        let req = RpcFrame::Request(RpcRequest::new(
            1_i64,
            "daemon.ping",
            Some(json!({"echo": "line1\nline2"})),
        ));
        let req_line = assert_ndjson_roundtrip(&req);
        assert!(req_line.contains(r#""jsonrpc":"2.0""#));
        assert!(req_line.contains(r#""id":1"#));

        let resp_ok = RpcFrame::Response(RpcResponse::ok(
            "req-2",
            json!({"ok": true, "boot_id": "boot_1"}),
        ));
        let resp_ok_line = assert_ndjson_roundtrip(&resp_ok);
        assert!(!resp_ok_line.contains(r#""error""#));

        let resp_err = RpcFrame::Response(RpcResponse::err(
            3_i64,
            RpcError::new(RpcErrorCode::StaleEpoch, "epoch mismatch")
                .with_data(json!({"expected": 4, "got": 3})),
        ));
        let resp_err_line = assert_ndjson_roundtrip(&resp_err);
        assert!(resp_err_line.contains(r#""code":-32002"#));

        let notif = RpcFrame::Notification(RpcNotification::new(
            "pty.output",
            Some(json!({"pty_id": "pty_1", "offset": 42})),
        ));
        let notif_line = assert_ndjson_roundtrip(&notif);
        assert!(!notif_line.contains(r#""id""#));
    }

    #[test]
    fn rpc_error_code_roundtrip() {
        let codes = [
            (RpcErrorCode::ParseError, -32700),
            (RpcErrorCode::InvalidRequest, -32600),
            (RpcErrorCode::MethodNotFound, -32601),
            (RpcErrorCode::InvalidParams, -32602),
            (RpcErrorCode::InternalError, -32603),
            (RpcErrorCode::Unauthorized, -32001),
            (RpcErrorCode::StaleEpoch, -32002),
            (RpcErrorCode::NotFound, -32003),
            (RpcErrorCode::LeaseConflict, -32004),
            (RpcErrorCode::ReplayGap, -32005),
        ];
        for (variant, raw) in codes {
            assert_eq!(variant.code(), raw);
            assert_eq!(RpcErrorCode::from_code(raw), Some(variant));
            let json = assert_ndjson_roundtrip(&variant);
            assert_eq!(json, raw.to_string());
        }
        assert!(serde_json::from_str::<RpcErrorCode>("-32099").is_err());
    }

    #[test]
    fn auth_and_daemon_health_roundtrip() {
        let auth_params = RpcAuthParams {
            protocol_version: crate::PROTOCOL_VERSION,
            client_kind: RpcClientKind::BrowserCompanion,
            client_id: Some("comp_1".into()),
            token: Some("secret".into()),
            label: Some("Chrome".into()),
        };
        let auth_json = assert_ndjson_roundtrip(&auth_params);
        assert!(auth_json.contains(r#""client_kind":"browser_companion""#));

        let auth_res = RpcAuthResult {
            ok: true,
            protocol_version: crate::PROTOCOL_VERSION,
            boot_id: BootId::from("boot_abc"),
            client_id: "comp_1".into(),
            current_seq: Seq(99),
        };
        assert_ndjson_roundtrip(&auth_res);

        let ping = DaemonPingResult {
            ok: true,
            boot_id: BootId::from("boot_abc"),
            ts_ms: TimestampMs(1_700_000_000_000),
        };
        assert_ndjson_roundtrip(&ping);

        let status = DaemonStatusResult {
            boot_id: BootId::from("boot_abc"),
            protocol_version: crate::PROTOCOL_VERSION,
            pid: 4242,
            started_at_ms: TimestampMs(1_700_000_000_000),
            uptime_ms: 60_000,
            current_seq: Seq(99),
            active_threads: 3,
            active_ptys: 2,
            connected_clients: 4,
        };
        assert_ndjson_roundtrip(&status);
    }

    #[test]
    fn thread_turn_lease_mailbox_rpc_roundtrip() {
        let mut env = BTreeMap::new();
        env.insert("HARNESS_PROFILE".into(), "work".into());
        let spawn = ThreadSpawnParams {
            agent: "claude".into(),
            profile: Some("work".into()),
            driver: Some(ThreadDriver::ClaudeSdk),
            kind: Some(ThreadKind::Hybrid),
            cwd: "/repo".into(),
            space: Some("main".into()),
            window_id: None,
            pane_id: None,
            worktree_path: None,
            vendor_session_id: None,
            title: Some("Task".into()),
            model: None,
            initial_prompt: Some("Run tests".into()),
            env: Some(env),
        };
        assert_ndjson_roundtrip(&spawn);

        let thread = Thread {
            id: ThreadId::from("thr_1"),
            space: Some("main".into()),
            window_id: None,
            pane_id: None,
            pty_id: None,
            agent: "claude".into(),
            profile: Some("work".into()),
            driver: ThreadDriver::ClaudeSdk,
            kind: ThreadKind::Hybrid,
            state: ThreadState::Idle,
            cwd: "/repo".into(),
            canonical_cwd: "/repo".into(),
            worktree_path: None,
            vendor_session_id: None,
            title: Some("Task".into()),
            model: None,
            epoch: Epoch(1),
            active_turn_id: None,
            created_at_ms: TimestampMs(100),
            updated_at_ms: TimestampMs(200),
        };
        let lease = Lease {
            id: LeaseId::from("lease_1"),
            thread_id: thread.id.clone(),
            epoch: Epoch(1),
            state: LeaseState::Owned,
            owner: Some(LeaseOwner {
                kind: LeaseOwnerKind::Human,
                id: "user".into(),
                client_id: None,
                label: None,
            }),
            acquired_at_ms: Some(TimestampMs(100)),
            expires_at_ms: None,
            updated_at_ms: TimestampMs(100),
        };

        assert_ndjson_roundtrip(&ThreadSpawnResult {
            thread: thread.clone(),
            lease: Some(lease.clone()),
        });
        assert_ndjson_roundtrip(&ThreadListParams::default());
        assert_ndjson_roundtrip(&ThreadListResult {
            threads: vec![thread],
        });
        assert_ndjson_roundtrip(&ThreadGetParams {
            thread_id: ThreadId::from("thr_1"),
        });
        assert_ndjson_roundtrip(&ThreadCloseParams {
            thread_id: ThreadId::from("thr_1"),
            epoch: Some(Epoch(1)),
            force: Some(false),
            reason: Some("done".into()),
        });

        let turn_send = TurnSendParams {
            thread_id: ThreadId::from("thr_1"),
            epoch: Epoch(1),
            origin: TurnOrigin::Human,
            prompt: "Hello\nworld".into(),
            model: None,
            idempotency_key: Some("idem-1".into()),
        };
        assert_ndjson_roundtrip(&turn_send);

        let turn_res = TurnSendResult {
            turn: Turn {
                id: TurnId::from("turn_1"),
                thread_id: ThreadId::from("thr_1"),
                epoch: Epoch(1),
                origin: TurnOrigin::Human,
                state: TurnState::Failed,
                prompt: Some("Hello\nworld".into()),
                reply_preview: None,
                model: None,
                usage: Some(TurnUsage::default()),
                error: Some(TurnError {
                    code: "aborted".into(),
                    message: "cancelled".into(),
                    retryable: false,
                }),
                started_at_ms: TimestampMs(150),
                ended_at_ms: Some(TimestampMs(160)),
            },
        };
        assert_ndjson_roundtrip(&turn_res);
        assert_ndjson_roundtrip(&TurnCancelParams {
            thread_id: ThreadId::from("thr_1"),
            turn_id: TurnId::from("turn_1"),
            epoch: Some(Epoch(1)),
            reason: None,
        });

        let owner = LeaseOwner {
            kind: LeaseOwnerKind::Agent,
            id: "coord".into(),
            client_id: None,
            label: None,
        };
        assert_ndjson_roundtrip(&LeaseClaimParams {
            thread_id: ThreadId::from("thr_1"),
            owner: owner.clone(),
            expected_epoch: Some(Epoch(1)),
            ttl_ms: Some(30_000),
            reason: Some(HandoffReason::AgentClaim),
        });
        assert_ndjson_roundtrip(&LeaseYieldParams {
            thread_id: ThreadId::from("thr_1"),
            epoch: Epoch(2),
            owner: owner.clone(),
            reason: Some(HandoffReason::AgentYield),
        });
        assert_ndjson_roundtrip(&LeaseHandoffParams {
            thread_id: ThreadId::from("thr_1"),
            from_epoch: Epoch(2),
            to_owner: owner.clone(),
            reason: HandoffReason::UserAttach,
            from_owner: None,
            ttl_ms: None,
            cancel_active_turn: Some(true),
        });
        assert_ndjson_roundtrip(&LeaseStatusParams {
            thread_id: ThreadId::from("thr_1"),
        });
        assert_ndjson_roundtrip(&LeaseResult {
            lease,
            transition: Some(LeaseTransition {
                thread_id: ThreadId::from("thr_1"),
                from_state: LeaseState::Unowned,
                to_state: LeaseState::Owned,
                from_epoch: Epoch(0),
                to_epoch: Epoch(1),
                reason: HandoffReason::UserAttach,
                actor: Some(owner),
                ts_ms: TimestampMs(100),
            }),
        });

        let send_mail = MailboxSendParams {
            thread_id: ThreadId::from("thr_1"),
            from: MailboxAddress {
                thread_id: None,
                agent: Some("codex".into()),
                role: Some("worker".into()),
            },
            kind: MailboxMessageKind::Reply,
            priority: MailboxPriority::Normal,
            dedup_key: Some("k1".into()),
            correlation_id: None,
            payload: json!({"status": "ok"}),
            expires_at_ms: None,
        };
        assert_ndjson_roundtrip(&send_mail);

        let mailbox_msg = MailboxMessage {
            id: MailboxMessageId::from("m1"),
            thread_id: ThreadId::from("thr_1"),
            from: MailboxAddress::default(),
            kind: MailboxMessageKind::Wakeup,
            priority: MailboxPriority::Urgent,
            status: MailboxStatus::Delivered,
            dedup_key: None,
            correlation_id: None,
            payload: json!(null),
            created_at_ms: TimestampMs(10),
            delivered_at_ms: Some(TimestampMs(11)),
            expires_at_ms: None,
        };
        assert_ndjson_roundtrip(&MailboxSendResult {
            message: mailbox_msg.clone(),
            deduplicated: false,
        });
        assert_ndjson_roundtrip(&MailboxListParams {
            thread_id: ThreadId::from("thr_1"),
            status: Some(MailboxStatus::Pending),
            kind: None,
            limit: Some(50),
        });
        assert_ndjson_roundtrip(&MailboxListResult {
            messages: vec![mailbox_msg],
        });
        assert_ndjson_roundtrip(&MailboxAckParams {
            thread_id: ThreadId::from("thr_1"),
            message_id: MailboxMessageId::from("m1"),
            status: Some(MailboxStatus::Acknowledged),
        });
    }

    #[test]
    fn pty_rpc_frames_roundtrip_and_ndjson_check() {
        let mut env = BTreeMap::new();
        env.insert("TERM".into(), "xterm-256color".into());
        let spawn = PtySpawnParams {
            thread_id: ThreadId::from("thr_1"),
            argv: vec!["claude".into(), "--continue".into()],
            cwd: "/repo".into(),
            env,
            size: PtySize {
                cols: 120,
                rows: 40,
                xpixel: None,
                ypixel: None,
            },
        };
        assert_ndjson_roundtrip(&spawn);
        assert_ndjson_roundtrip(&PtySpawnResult {
            pty_id: PtyId::from("pty_1"),
            pid: 1001,
            pgid: 1001,
        });
        assert_ndjson_roundtrip(&PtyAttachParams {
            pty_id: PtyId::from("pty_1"),
            after_offset: Some(4096),
            request_replay: true,
        });
        assert_ndjson_roundtrip(&PtyAttachResult {
            pty_id: PtyId::from("pty_1"),
            current_offset: 8192,
            replay_truncated: false,
        });
        assert_ndjson_roundtrip(&PtyDetachParams {
            pty_id: PtyId::from("pty_1"),
        });
        assert_ndjson_roundtrip(&PtyInputParams {
            pty_id: PtyId::from("pty_1"),
            epoch: Some(Epoch(2)),
            encoding: PtyEncoding::Base64,
            data_b64: "bHMgLWxhCg==".into(),
        });
        assert_ndjson_roundtrip(&PtyOutputNotification {
            pty_id: PtyId::from("pty_1"),
            offset: 8192,
            encoding: PtyEncoding::Base64,
            data_b64: "G1szMW1oZWxsbwo=".into(),
            ts_ms: TimestampMs(1_700_000_000_123),
        });
        assert_ndjson_roundtrip(&PtyResizeParams {
            pty_id: PtyId::from("pty_1"),
            size: PtySize {
                cols: 160,
                rows: 50,
                xpixel: Some(1280),
                ypixel: Some(800),
            },
        });
        assert_ndjson_roundtrip(&PtyKillParams {
            pty_id: PtyId::from("pty_1"),
            signal: Some("SIGTERM".into()),
        });
        assert_ndjson_roundtrip(&PtyExitNotification {
            pty_id: PtyId::from("pty_1"),
            exit_code: Some(0),
            signal: None,
            ts_ms: TimestampMs(1_700_000_010_000),
        });
        assert_ndjson_roundtrip(&PtyOscNotification {
            pty_id: PtyId::from("pty_1"),
            status: Some(OscStatus {
                state: OscState::Working,
                app: Some("claude-code".into()),
                profile: None,
                kind: None,
                msg: None,
                raw: None,
            }),
            resume_hint: Some(OscResumeHint {
                agent: "claude".into(),
                session_id: "sess_99".into(),
                profile: None,
                cwd: Some("/repo".into()),
                resume_target: None,
                thread_id: Some(ThreadId::from("thr_1")),
                raw: None,
            }),
            ts_ms: TimestampMs(1_700_000_001_000),
        });
    }

    #[test]
    fn rpc_core_ts_declarations_have_no_bigint_or_json_value() {
        let decls = [
            RpcId::decl(),
            RpcErrorCode::decl(),
            RpcError::decl(),
            RpcRequest::decl(),
            RpcResponse::decl(),
            RpcNotification::decl(),
            RpcFrame::decl(),
            RpcClientKind::decl(),
            RpcAuthParams::decl(),
            RpcAuthResult::decl(),
            DaemonPingResult::decl(),
            DaemonStatusResult::decl(),
            ThreadSpawnParams::decl(),
            ThreadSpawnResult::decl(),
            ThreadListParams::decl(),
            ThreadListResult::decl(),
            ThreadGetParams::decl(),
            ThreadCloseParams::decl(),
            TurnSendParams::decl(),
            TurnSendResult::decl(),
            TurnCancelParams::decl(),
            LeaseClaimParams::decl(),
            LeaseYieldParams::decl(),
            LeaseHandoffParams::decl(),
            LeaseStatusParams::decl(),
            LeaseResult::decl(),
            MailboxSendParams::decl(),
            MailboxSendResult::decl(),
            MailboxListParams::decl(),
            MailboxListResult::decl(),
            MailboxAckParams::decl(),
            PtyEncoding::decl(),
            PtySize::decl(),
            PtySpawnParams::decl(),
            PtySpawnResult::decl(),
            PtyAttachParams::decl(),
            PtyAttachResult::decl(),
            PtyDetachParams::decl(),
            PtyInputParams::decl(),
            PtyOutputNotification::decl(),
            PtyResizeParams::decl(),
            PtyKillParams::decl(),
            PtyExitNotification::decl(),
            PtyOscNotification::decl(),
        ];
        for decl in decls {
            assert!(
                !decl.contains("bigint"),
                "unexpected bigint in TS decl: {decl}"
            );
            assert!(
                !decl.contains("JsonValue"),
                "unexpected JsonValue in TS decl: {decl}"
            );
        }
        assert!(RpcErrorCode::decl().contains("-32005"));
        assert!(PtyAttachParams::decl().contains("after_offset?: number"));
        assert!(PtyAttachResult::decl().contains("current_offset: number"));
    }
}
