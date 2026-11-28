use std::os::fd::{BorrowedFd, RawFd};

use anyhow::{Context, Result};
use nix::sys::termios::{SetArg, Termios, cfmakeraw, tcgetattr, tcsetattr};
use spaces_types::PtySize;

const CTRL_RBRACKET: u8 = 0x1d;
const DEFAULT_COLS: u16 = 80;
const DEFAULT_ROWS: u16 = 24;

/// RAII guard that puts a TTY file descriptor into raw mode and restores original settings on drop.
pub struct RawModeGuard {
    fd: RawFd,
    original: Termios,
}

impl RawModeGuard {
    /// Enables raw mode on `fd` and returns a guard that restores the previous termios state.
    pub fn enter(fd: RawFd) -> Result<Self> {
        // SAFETY: caller supplies an open terminal file descriptor (typically STDIN_FILENO).
        let borrowed = unsafe { BorrowedFd::borrow_raw(fd) };
        let original = tcgetattr(borrowed).context("failed to read terminal attributes")?;
        let mut raw = original.clone();
        cfmakeraw(&mut raw);
        tcsetattr(borrowed, SetArg::TCSANOW, &raw).context("failed to enable raw terminal mode")?;
        Ok(Self { fd, original })
    }
}

impl Drop for RawModeGuard {
    fn drop(&mut self) {
        // SAFETY: `self.fd` remains open for the lifetime of the guard (stdin in `attach`).
        let borrowed = unsafe { BorrowedFd::borrow_raw(self.fd) };
        let _ = tcsetattr(borrowed, SetArg::TCSANOW, &self.original);
    }
}

/// Queries the current terminal window size via `TIOCGWINSZ`, falling back to `80x24`.
pub fn query_terminal_size(fd: RawFd) -> PtySize {
    let mut ws = libc::winsize {
        ws_row: 0,
        ws_col: 0,
        ws_xpixel: 0,
        ws_ypixel: 0,
    };

    // SAFETY: `ws` is a valid mutable `libc::winsize` pointer for `TIOCGWINSZ`.
    let rc = unsafe { libc::ioctl(fd, libc::TIOCGWINSZ, &mut ws) };
    if rc == 0 && ws.ws_col > 0 && ws.ws_row > 0 {
        PtySize {
            cols: ws.ws_col,
            rows: ws.ws_row,
            xpixel: (ws.ws_xpixel > 0).then_some(ws.ws_xpixel),
            ypixel: (ws.ws_ypixel > 0).then_some(ws.ws_ypixel),
        }
    } else {
        PtySize {
            cols: DEFAULT_COLS,
            rows: DEFAULT_ROWS,
            xpixel: None,
            ypixel: None,
        }
    }
}

/// Output action produced by [`DetachEscapeParser`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DetachAction {
    /// Forward raw bytes to the attached PTY.
    Passthrough(Vec<u8>),
    /// User pressed `Ctrl-] d` to detach cleanly.
    Detach,
}

/// Stateful byte scanner recognizing `Ctrl-] d` detach and `Ctrl-] Ctrl-]` literal passthrough.
#[derive(Debug, Default)]
pub struct DetachEscapeParser {
    pending_escape: bool,
}

impl DetachEscapeParser {
    pub fn new() -> Self {
        Self::default()
    }

    /// Scans an input chunk, handling escape sequences split across `read()` calls.
    pub fn feed(&mut self, bytes: &[u8]) -> Vec<DetachAction> {
        let mut actions = Vec::new();
        let mut out = Vec::new();

        for &byte in bytes {
            if self.pending_escape {
                self.pending_escape = false;
                match byte {
                    b'd' | b'D' => {
                        if !out.is_empty() {
                            actions.push(DetachAction::Passthrough(std::mem::take(&mut out)));
                        }
                        actions.push(DetachAction::Detach);
                        return actions;
                    }
                    CTRL_RBRACKET => {
                        out.push(CTRL_RBRACKET);
                    }
                    other => {
                        out.push(CTRL_RBRACKET);
                        out.push(other);
                    }
                }
            } else if byte == CTRL_RBRACKET {
                self.pending_escape = true;
            } else {
                out.push(byte);
            }
        }

        if !out.is_empty() {
            actions.push(DetachAction::Passthrough(out));
        }

        actions
    }

    /// Flushes a trailing pending `Ctrl-]` if stdin reaches EOF before a second byte arrives.
    pub fn flush(&mut self) -> Option<DetachAction> {
        if self.pending_escape {
            self.pending_escape = false;
            Some(DetachAction::Passthrough(vec![CTRL_RBRACKET]))
        } else {
            None
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn query_terminal_size_falls_back_when_not_a_tty() {
        let size = query_terminal_size(-1);
        assert_eq!(size.cols, 80);
        assert_eq!(size.rows, 24);
        assert_eq!(size.xpixel, None);
        assert_eq!(size.ypixel, None);
    }

    #[test]
    fn parser_passes_through_normal_bytes() {
        let mut parser = DetachEscapeParser::new();
        assert_eq!(
            parser.feed(b"hello world\n"),
            vec![DetachAction::Passthrough(b"hello world\n".to_vec())]
        );
        assert!(parser.feed(b"").is_empty());
        assert_eq!(parser.flush(), None);
    }

    #[test]
    fn parser_recognizes_ctrl_rbracket_d_in_single_and_split_chunks() {
        let mut parser = DetachEscapeParser::new();
        assert_eq!(
            parser.feed(b"abc\x1ddxyz"),
            vec![
                DetachAction::Passthrough(b"abc".to_vec()),
                DetachAction::Detach,
            ]
        );

        let mut split = DetachEscapeParser::new();
        assert!(split.feed(&[CTRL_RBRACKET]).is_empty());
        assert_eq!(split.feed(b"d"), vec![DetachAction::Detach]);

        let mut uppercase = DetachEscapeParser::new();
        assert!(uppercase.feed(&[CTRL_RBRACKET]).is_empty());
        assert_eq!(uppercase.feed(b"D"), vec![DetachAction::Detach]);
    }

    #[test]
    fn parser_escapes_double_ctrl_rbracket_and_other_bytes() {
        let mut parser = DetachEscapeParser::new();
        assert!(parser.feed(&[CTRL_RBRACKET]).is_empty());
        assert_eq!(
            parser.feed(&[CTRL_RBRACKET]),
            vec![DetachAction::Passthrough(vec![CTRL_RBRACKET])]
        );

        // Escaped Ctrl-] followed immediately by 'd' should pass through [0x1d, b'd'], not detach.
        assert_eq!(
            parser.feed(&[CTRL_RBRACKET, CTRL_RBRACKET, b'd']),
            vec![DetachAction::Passthrough(vec![CTRL_RBRACKET, b'd'])]
        );

        // Ctrl-] followed by arbitrary byte passes both bytes through across chunks.
        assert!(parser.feed(&[CTRL_RBRACKET]).is_empty());
        assert_eq!(
            parser.feed(b"x"),
            vec![DetachAction::Passthrough(vec![CTRL_RBRACKET, b'x'])]
        );

        // Trailing Ctrl-] on EOF flushes cleanly.
        assert!(parser.feed(&[CTRL_RBRACKET]).is_empty());
        assert_eq!(
            parser.flush(),
            Some(DetachAction::Passthrough(vec![CTRL_RBRACKET]))
        );
        assert_eq!(parser.flush(), None);
    }
}
