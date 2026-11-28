use base64::Engine;
use base64::engine::general_purpose::{STANDARD, STANDARD_NO_PAD, URL_SAFE, URL_SAFE_NO_PAD};
use spaces_types::{OscResumeHint, OscState, OscStatus, ThreadId};

/// Maximum buffered byte length for a single OSC payload before dropping it.
pub const MAX_OSC_BYTES: usize = 16 * 1024;

const OSC_STATUS_PREFIX: &str = "7501;";
const OSC_RESUME_HINT_PREFIX: &str = "7577;";

/// Parsed OSC event emitted by [`OscScanner`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OscEvent {
    Status(OscStatus),
    ResumeHint(OscResumeHint),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
enum ScanState {
    #[default]
    Ground,
    Esc,
    Osc,
    OscEsc,
}

/// 4-state streaming byte scanner that extracts OSC 7501 and OSC 7577 events
/// across arbitrary chunk boundaries (including tmux DCS passthrough).
#[derive(Debug, Default)]
pub struct OscScanner {
    state: ScanState,
    buf: Vec<u8>,
    overflow: bool,
}

impl OscScanner {
    /// Creates a new scanner in the initial `Ground` state.
    pub fn new() -> Self {
        Self::default()
    }

    /// Feeds an arbitrary chunk of PTY bytes and invokes `on_event` for each
    /// valid OSC 7501 or OSC 7577 sequence completed within `chunk`.
    pub fn feed<F>(&mut self, chunk: &[u8], mut on_event: F)
    where
        F: FnMut(OscEvent),
    {
        for &b in chunk {
            match self.state {
                ScanState::Ground => {
                    if b == 0x1b {
                        self.state = ScanState::Esc;
                    }
                }
                ScanState::Esc => match b {
                    b']' => {
                        self.state = ScanState::Osc;
                        self.buf.clear();
                        self.overflow = false;
                    }
                    0x1b => {
                        self.state = ScanState::Esc;
                    }
                    _ => {
                        self.state = ScanState::Ground;
                    }
                },
                ScanState::Osc => match b {
                    0x07 => {
                        self.finish(&mut on_event);
                        self.state = ScanState::Ground;
                    }
                    0x1b => {
                        self.state = ScanState::OscEsc;
                    }
                    0x18 | 0x1a => {
                        self.reset();
                    }
                    _ => {
                        if !self.overflow {
                            if self.buf.len() < MAX_OSC_BYTES {
                                self.buf.push(b);
                            } else {
                                self.overflow = true;
                                self.buf.clear();
                            }
                        }
                    }
                },
                ScanState::OscEsc => match b {
                    b'\\' => {
                        self.finish(&mut on_event);
                        self.state = ScanState::Ground;
                    }
                    b']' => {
                        self.buf.clear();
                        self.overflow = false;
                        self.state = ScanState::Osc;
                    }
                    0x1b => {
                        // Stay in OscEsc so tmux DCS passthrough doubled escapes
                        // (\x1bPtmux;\x1b\x1b]7501;...\x1b\x1b\\\x1b\\) terminate cleanly.
                        self.state = ScanState::OscEsc;
                    }
                    _ => {
                        self.reset();
                    }
                },
            }
        }
    }

    fn finish(&mut self, on_event: &mut impl FnMut(OscEvent)) {
        if self.overflow {
            self.reset();
            return;
        }
        let raw = String::from_utf8_lossy(&self.buf).into_owned();
        self.buf.clear();
        if let Some(body) = raw.strip_prefix(OSC_STATUS_PREFIX) {
            if let Some(status) = parse_status_payload(body) {
                on_event(OscEvent::Status(status));
            }
        } else if let Some(body) = raw.strip_prefix(OSC_RESUME_HINT_PREFIX)
            && let Some(hint) = parse_resume_hint_payload(body)
        {
            on_event(OscEvent::ResumeHint(hint));
        }
    }

    fn reset(&mut self) {
        self.state = ScanState::Ground;
        self.buf.clear();
        self.overflow = false;
    }
}

/// Parses an OSC 7501 payload (everything after `"7501;"`).
pub fn parse_status_payload(body: &str) -> Option<OscStatus> {
    let body = body.trim();
    if body.is_empty() {
        return None;
    }

    let mut state: Option<OscState> = None;
    let mut app: Option<String> = None;
    let mut profile: Option<String> = None;
    let mut kind: Option<String> = None;
    let mut msg: Option<String> = None;

    for part in body.split(':') {
        let Some((k, v)) = part.split_once('=') else {
            continue;
        };
        let key = k.trim().to_ascii_lowercase();
        let val = v.trim();
        match key.as_str() {
            "state" => state = normalize_state(val),
            "app" => app = sanitize_text(val),
            "profile" => profile = sanitize_text(val),
            "kind" => kind = sanitize_text(val),
            "msg" => msg = decode_b64_or_raw(val),
            _ => {}
        }
    }

    let state = state?;
    Some(OscStatus {
        state,
        app,
        profile,
        kind,
        msg,
        raw: Some(body.to_owned()),
    })
}

/// Parses an OSC 7577 resume-hint payload (everything after `"7577;"`).
pub fn parse_resume_hint_payload(body: &str) -> Option<OscResumeHint> {
    let body = body.trim();
    if body.is_empty() {
        return None;
    }

    let mut agent: Option<String> = None;
    let mut session_id: Option<String> = None;
    let mut profile: Option<String> = None;
    let mut cwd: Option<String> = None;
    let mut resume_target: Option<String> = None;
    let mut thread_id: Option<ThreadId> = None;
    let mut signature: Option<String> = None;

    for part in body.split(':') {
        let Some((k, v)) = part.split_once('=') else {
            continue;
        };
        let key = k.trim().to_ascii_lowercase();
        let val = v.trim();
        match key.as_str() {
            "agent" => agent = sanitize_text(val),
            "session_id" => session_id = sanitize_text(val),
            "profile" => profile = sanitize_text(val),
            "cwd" => cwd = decode_b64_or_raw(val),
            "resume_target" => resume_target = decode_b64_or_raw(val),
            "thread_id" => thread_id = sanitize_text(val).map(ThreadId::from),
            "sig" | "signature" => signature = sanitize_text(val),
            _ => {}
        }
    }

    let agent = agent?;
    let session_id = session_id?;
    Some(OscResumeHint {
        agent,
        session_id,
        profile,
        cwd,
        resume_target,
        thread_id,
        signature,
        raw: Some(body.to_owned()),
    })
}

/// Normalizes an OSC 7501 `state=` value matching `internal/osc/osc.go`.
pub fn normalize_state(raw: &str) -> Option<OscState> {
    match raw.trim().to_ascii_lowercase().as_str() {
        "working" | "busy" | "running" => Some(OscState::Working),
        "idle" => Some(OscState::Idle),
        "done" | "complete" | "completed" | "success" => Some(OscState::Done),
        "blocked" | "needs_you" | "needs-you" | "needsyou" | "waiting" => Some(OscState::Blocked),
        "error" | "failed" | "failure" => Some(OscState::Failed),
        "clear" => Some(OscState::Clear),
        _ => None,
    }
}

/// Sanitizes untrusted text fields by converting `\t`, `\n`, `\r` to `' '`,
/// stripping all other ASCII control characters (`< 0x20` or `0x7f`), and trimming.
pub fn sanitize_text(s: &str) -> Option<String> {
    let mut out = String::with_capacity(s.len());
    for ch in s.chars() {
        let code = ch as u32;
        if code < 0x20 || code == 0x7f {
            if ch == '\t' || ch == '\n' || ch == '\r' {
                out.push(' ');
            }
            continue;
        }
        out.push(ch);
    }
    let trimmed = out.trim();
    if trimmed.is_empty() {
        None
    } else {
        Some(trimmed.to_owned())
    }
}

fn decode_b64_or_raw(raw: &str) -> Option<String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return None;
    }
    for engine in [&STANDARD, &STANDARD_NO_PAD, &URL_SAFE, &URL_SAFE_NO_PAD] {
        if let Ok(bytes) = engine.decode(trimmed) {
            let decoded = String::from_utf8_lossy(&bytes);
            return sanitize_text(&decoded);
        }
    }
    sanitize_text(trimmed)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scan_all(bytes: &[u8]) -> Vec<OscEvent> {
        let mut scanner = OscScanner::new();
        let mut out = Vec::new();
        scanner.feed(bytes, |ev| out.push(ev));
        out
    }

    #[test]
    fn parse_status_payload_cases() {
        let msg = STANDARD.encode("Approve bash: go test");
        let rec = parse_status_payload(&format!(
            "state=blocked:app=claude-code:kind=permission:msg={msg}"
        ))
        .expect("expected valid status record");
        assert_eq!(rec.state, OscState::Blocked);
        assert_eq!(rec.app.as_deref(), Some("claude-code"));
        assert_eq!(rec.kind.as_deref(), Some("permission"));
        assert_eq!(rec.msg.as_deref(), Some("Approve bash: go test"));

        let rec2 = parse_status_payload("state=error:app=codex:profile=work")
            .expect("error should normalize to failed");
        assert_eq!(rec2.state, OscState::Failed);
        assert_eq!(rec2.app.as_deref(), Some("codex"));
        assert_eq!(rec2.profile.as_deref(), Some("work"));

        let rec3 = parse_status_payload("state=clear").expect("clear should parse");
        assert_eq!(rec3.state, OscState::Clear);
        assert_eq!(rec3.app, None);

        assert!(parse_status_payload("app=claude").is_none());
        assert!(parse_status_payload("state=bogus:app=claude").is_none());
        assert!(parse_status_payload("   ").is_none());
    }

    #[test]
    fn normalize_state_all_aliases() {
        for raw in ["working", "busy", "running", " WORKING "] {
            assert_eq!(normalize_state(raw), Some(OscState::Working));
        }
        assert_eq!(normalize_state("idle"), Some(OscState::Idle));
        for raw in ["done", "complete", "completed", "success"] {
            assert_eq!(normalize_state(raw), Some(OscState::Done));
        }
        for raw in ["blocked", "needs_you", "needs-you", "needsyou", "waiting"] {
            assert_eq!(normalize_state(raw), Some(OscState::Blocked));
        }
        for raw in ["error", "failed", "failure"] {
            assert_eq!(normalize_state(raw), Some(OscState::Failed));
        }
        assert_eq!(normalize_state("clear"), Some(OscState::Clear));
        assert_eq!(normalize_state("unknown"), None);
    }

    #[test]
    fn scanner_st_and_bel_and_one_byte_chunk_splits() {
        let msg = STANDARD.encode("editing main.go");
        let stream = format!(
            "normal output\x1b]2;Window Title\x07more text\
             \x1b]7501;state=working:app=claude:msg={msg}\x1b\\\
             in between\x1b]7501;state=done:app=claude\x07tail"
        );

        let mut scanner = OscScanner::new();
        let mut got = Vec::new();
        for &b in stream.as_bytes() {
            scanner.feed(&[b], |ev| got.push(ev));
        }

        assert_eq!(got.len(), 2);
        assert_eq!(
            got[0],
            OscEvent::Status(OscStatus {
                state: OscState::Working,
                app: Some("claude".into()),
                profile: None,
                kind: None,
                msg: Some("editing main.go".into()),
                raw: Some(format!("state=working:app=claude:msg={msg}")),
            })
        );
        assert_eq!(
            got[1],
            OscEvent::Status(OscStatus {
                state: OscState::Done,
                app: Some("claude".into()),
                profile: None,
                kind: None,
                msg: None,
                raw: Some("state=done:app=claude".into()),
            })
        );
    }

    #[test]
    fn scanner_ignores_oversized_osc_and_recovers() {
        let huge = format!(
            "\x1b]7501;state=working:app={}\x1b\\",
            "a".repeat(MAX_OSC_BYTES + 100)
        );
        let valid = "\x1b]7501;state=idle:app=pi\x07";
        let got = scan_all(format!("{huge}{valid}").as_bytes());
        assert_eq!(got.len(), 1);
        match &got[0] {
            OscEvent::Status(s) => {
                assert_eq!(s.state, OscState::Idle);
                assert_eq!(s.app.as_deref(), Some("pi"));
            }
            other => panic!("unexpected event: {other:?}"),
        }

        // Exact MAX_OSC_BYTES payload fits; MAX_OSC_BYTES + 1 overflows.
        let prefix = "7501;state=working:app=";
        let exact_app = "x".repeat(MAX_OSC_BYTES - prefix.len());
        let exact = format!("\x1b]{prefix}{exact_app}\x07");
        assert_eq!(exact.len(), MAX_OSC_BYTES + 3);
        let got_exact = scan_all(exact.as_bytes());
        assert_eq!(got_exact.len(), 1);

        let one_over = format!("\x1b]{prefix}{}x\x07", exact_app);
        assert!(scan_all(one_over.as_bytes()).is_empty());

        // Immediate ESC ] restart inside OscEsc drops incomplete first sequence and parses second.
        let restart = b"\x1b]7501;state=working\x1b]7501;state=done:app=codex\x07";
        let got_restart = scan_all(restart);
        assert_eq!(got_restart.len(), 1);
        assert!(matches!(
            &got_restart[0],
            OscEvent::Status(OscStatus {
                state: OscState::Done,
                ..
            })
        ));
    }

    #[test]
    fn sanitize_control_chars_in_msg_and_four_b64_alphabets() {
        let dirty = STANDARD.encode(b"hello\x1b[31m\nworld\r\t!\x7f");
        let rec = parse_status_payload(&format!("state=working:app=opencode:msg={dirty}"))
            .expect("expected valid status");
        let msg = rec.msg.expect("expected sanitized msg");
        assert!(!msg.contains('\x1b'));
        assert!(!msg.contains('\n'));
        assert!(!msg.contains('\r'));
        assert!(!msg.contains('\t'));
        assert!(!msg.contains('\x7f'));
        assert_eq!(msg, "hello[31m world  !");

        // Exercise all 4 base64 variants and raw fallback.
        let sample = "subject?+>/=";
        for encoded in [
            STANDARD.encode(sample),
            STANDARD_NO_PAD.encode(sample),
            URL_SAFE.encode(sample),
            URL_SAFE_NO_PAD.encode(sample),
        ] {
            let parsed =
                parse_status_payload(&format!("state=idle:msg={encoded}")).expect("b64 variant");
            assert_eq!(parsed.msg.as_deref(), Some(sample));
        }

        let raw_fallback =
            parse_status_payload("state=idle:msg=plain raw text!").expect("raw fallback");
        assert_eq!(raw_fallback.msg.as_deref(), Some("plain raw text!"));
    }

    #[test]
    fn scanner_tmux_passthrough_osc() {
        let stream = "\x1bPtmux;\x1b\x1b]7501;state=working:app=claude\x1b\x1b\\\x1b\\\
                      \x1bPtmux;\x1b\x1b]7501;state=done:app=claude\x1b\x1b\\\x1b\\";
        let got = scan_all(stream.as_bytes());
        assert_eq!(got.len(), 2);
        assert!(matches!(
            &got[0],
            OscEvent::Status(OscStatus {
                state: OscState::Working,
                ..
            })
        ));
        assert!(matches!(
            &got[1],
            OscEvent::Status(OscStatus {
                state: OscState::Done,
                ..
            })
        ));
    }

    #[test]
    fn scanner_can_and_sub_abort_and_recover() {
        let stream = b"\x1b]7501;state=working:app=claude\x18state=done\x07\
                       \x1b]7501;state=blocked:app=codex\x1a\x07\
                       \x1b]7501;state=idle:app=pi\x07";
        let got = scan_all(stream);
        assert_eq!(got.len(), 1);
        match &got[0] {
            OscEvent::Status(s) => {
                assert_eq!(s.state, OscState::Idle);
                assert_eq!(s.app.as_deref(), Some("pi"));
            }
            other => panic!("unexpected event: {other:?}"),
        }
    }

    #[test]
    fn scanner_osc_7577_resume_hint_parsing_and_streaming() {
        let cwd_b64 = URL_SAFE_NO_PAD.encode("/home/user/repo:work");
        let target_b64 = STANDARD_NO_PAD.encode("claude --resume sess_42");
        let sig = "a".repeat(64);
        let stream = format!(
            "\x1bPtmux;\x1b\x1b]7577;agent=claude:session_id=sess_42:profile=work:\
             cwd={cwd_b64}:resume_target={target_b64}:thread_id=thr_9:sig={sig}\x1b\x1b\\\x1b\\\
             \x1b]7577;agent=codex:session_id=sess_99:cwd=/tmp/proj:resume_target=codex resume sess_99:\
             signature={sig}\x07\
             \x1b]7577;agent=claude\x07\
             \x1b]7577;session_id=only_sess\x07"
        );

        let mut scanner = OscScanner::new();
        let mut got = Vec::new();
        for &b in stream.as_bytes() {
            scanner.feed(&[b], |ev| got.push(ev));
        }

        assert_eq!(got.len(), 2);
        assert_eq!(
            got[0],
            OscEvent::ResumeHint(OscResumeHint {
                agent: "claude".into(),
                session_id: "sess_42".into(),
                profile: Some("work".into()),
                cwd: Some("/home/user/repo:work".into()),
                resume_target: Some("claude --resume sess_42".into()),
                thread_id: Some(ThreadId::from("thr_9")),
                signature: Some(sig.clone()),
                raw: Some(format!(
                    "agent=claude:session_id=sess_42:profile=work:\
                     cwd={cwd_b64}:resume_target={target_b64}:thread_id=thr_9:sig={sig}"
                )),
            })
        );
        assert_eq!(
            got[1],
            OscEvent::ResumeHint(OscResumeHint {
                agent: "codex".into(),
                session_id: "sess_99".into(),
                profile: None,
                cwd: Some("/tmp/proj".into()),
                resume_target: Some("codex resume sess_99".into()),
                thread_id: None,
                signature: Some(sig),
                raw: Some(format!(
                    "agent=codex:session_id=sess_99:cwd=/tmp/proj:resume_target=codex resume sess_99:\
                     signature={}",
                    "a".repeat(64)
                )),
            })
        );
    }
}
