use std::collections::VecDeque;

use spaces_types::PTY_REPLAY_RING_BYTES;

/// Result of querying [`ReplayRing::read_after`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ReplaySlice {
    pub bytes: Vec<u8>,
    pub current_offset: u64,
    pub truncated: bool,
}

/// Bounded circular byte replay buffer tracking monotonic total bytes written.
#[derive(Debug)]
pub struct ReplayRing {
    capacity: usize,
    total_written: u64,
    buf: VecDeque<u8>,
}

impl Default for ReplayRing {
    fn default() -> Self {
        Self::new()
    }
}

impl ReplayRing {
    /// Creates a replay ring with the canonical `PTY_REPLAY_RING_BYTES` (1 MiB) capacity.
    pub fn new() -> Self {
        Self::with_capacity(PTY_REPLAY_RING_BYTES)
    }

    /// Creates a replay ring with a custom byte capacity.
    pub fn with_capacity(capacity: usize) -> Self {
        Self {
            capacity,
            total_written: 0,
            buf: VecDeque::with_capacity(capacity),
        }
    }

    /// Returns the configured byte capacity of the ring buffer.
    pub fn capacity(&self) -> usize {
        self.capacity
    }

    /// Returns the number of bytes currently retained in the ring buffer.
    pub fn len(&self) -> usize {
        self.buf.len()
    }

    /// Returns `true` when no bytes are currently buffered.
    pub fn is_empty(&self) -> bool {
        self.buf.is_empty()
    }

    /// Returns the total monotonic number of bytes ever pushed to this ring.
    pub fn total_written(&self) -> u64 {
        self.total_written
    }

    /// Appends `data` to the ring, evicting oldest bytes beyond `capacity`,
    /// and returns the new monotonic `total_written` offset.
    pub fn push(&mut self, data: &[u8]) -> u64 {
        self.total_written = self.total_written.saturating_add(data.len() as u64);
        if self.capacity == 0 || data.is_empty() {
            return self.total_written;
        }

        if data.len() >= self.capacity {
            self.buf.clear();
            let tail = &data[data.len() - self.capacity..];
            self.buf.extend(tail);
            return self.total_written;
        }

        let overflow = (self.buf.len() + data.len()).saturating_sub(self.capacity);
        if overflow > 0 {
            self.buf.drain(..overflow);
        }
        self.buf.extend(data);
        self.total_written
    }

    /// Reads buffered bytes after `after_offset`, reporting the current stream
    /// offset and whether older requested bytes were already overwritten.
    pub fn read_after(&self, after_offset: Option<u64>) -> ReplaySlice {
        let buf_len = self.buf.len() as u64;
        let ring_start = self.total_written.saturating_sub(buf_len);

        match after_offset {
            None => ReplaySlice {
                bytes: self.copy_from(0),
                current_offset: self.total_written,
                truncated: self.total_written > buf_len,
            },
            Some(off) if off < ring_start => ReplaySlice {
                bytes: self.copy_from(0),
                current_offset: self.total_written,
                truncated: true,
            },
            Some(off) if off >= self.total_written => ReplaySlice {
                bytes: Vec::new(),
                current_offset: self.total_written,
                truncated: off > self.total_written,
            },
            Some(off) => {
                let start_idx = (off - ring_start) as usize;
                ReplaySlice {
                    bytes: self.copy_from(start_idx),
                    current_offset: self.total_written,
                    truncated: false,
                }
            }
        }
    }

    fn copy_from(&self, start_idx: usize) -> Vec<u8> {
        if start_idx >= self.buf.len() {
            return Vec::new();
        }
        let (head, tail) = self.buf.as_slices();
        let mut out = Vec::with_capacity(self.buf.len() - start_idx);
        if start_idx < head.len() {
            out.extend_from_slice(&head[start_idx..]);
            out.extend_from_slice(tail);
        } else {
            out.extend_from_slice(&tail[start_idx - head.len()..]);
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_capacity_is_1_mib() {
        let ring = ReplayRing::new();
        assert_eq!(ring.capacity(), 1024 * 1024);
        assert!(ring.is_empty());
        assert_eq!(ring.total_written(), 0);
    }

    #[test]
    fn empty_and_partial_reads() {
        let mut ring = ReplayRing::with_capacity(16);
        assert_eq!(
            ring.read_after(None),
            ReplaySlice {
                bytes: vec![],
                current_offset: 0,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(0)),
            ReplaySlice {
                bytes: vec![],
                current_offset: 0,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(5)),
            ReplaySlice {
                bytes: vec![],
                current_offset: 0,
                truncated: true,
            }
        );

        assert_eq!(ring.push(b"hello"), 5);
        assert_eq!(ring.push(b" world"), 11);

        assert_eq!(
            ring.read_after(None),
            ReplaySlice {
                bytes: b"hello world".to_vec(),
                current_offset: 11,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(5)),
            ReplaySlice {
                bytes: b" world".to_vec(),
                current_offset: 11,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(11)),
            ReplaySlice {
                bytes: vec![],
                current_offset: 11,
                truncated: false,
            }
        );
    }

    #[test]
    fn exact_capacity_and_wrap_around_and_truncation() {
        let mut ring = ReplayRing::with_capacity(8);
        assert_eq!(ring.push(b"12345678"), 8);
        assert_eq!(ring.len(), 8);
        assert_eq!(
            ring.read_after(None),
            ReplaySlice {
                bytes: b"12345678".to_vec(),
                current_offset: 8,
                truncated: false,
            }
        );

        // Push 4 more bytes -> total_written = 12, ring holds "5678abcd" (offsets 4..12).
        assert_eq!(ring.push(b"abcd"), 12);
        assert_eq!(ring.len(), 8);
        assert_eq!(
            ring.read_after(None),
            ReplaySlice {
                bytes: b"5678abcd".to_vec(),
                current_offset: 12,
                truncated: true,
            }
        );
        assert_eq!(
            ring.read_after(Some(2)),
            ReplaySlice {
                bytes: b"5678abcd".to_vec(),
                current_offset: 12,
                truncated: true,
            }
        );
        assert_eq!(
            ring.read_after(Some(4)),
            ReplaySlice {
                bytes: b"5678abcd".to_vec(),
                current_offset: 12,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(7)),
            ReplaySlice {
                bytes: b"8abcd".to_vec(),
                current_offset: 12,
                truncated: false,
            }
        );
        assert_eq!(
            ring.read_after(Some(13)),
            ReplaySlice {
                bytes: vec![],
                current_offset: 12,
                truncated: true,
            }
        );

        // Large single write exceeding capacity keeps only the final `capacity` bytes.
        assert_eq!(ring.push(b"0123456789XY"), 24);
        assert_eq!(
            ring.read_after(Some(16)),
            ReplaySlice {
                bytes: b"456789XY".to_vec(),
                current_offset: 24,
                truncated: false,
            }
        );

        // Zero-capacity ring tracks offsets and reports truncation accurately.
        let mut zero = ReplayRing::with_capacity(0);
        assert_eq!(zero.push(b"abc"), 3);
        assert_eq!(
            zero.read_after(None),
            ReplaySlice {
                bytes: vec![],
                current_offset: 3,
                truncated: true,
            }
        );
    }
}
