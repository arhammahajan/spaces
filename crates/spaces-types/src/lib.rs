pub mod backend;
pub mod conversations;
pub mod ids;
pub mod model;
pub mod shared;
pub mod telemetry;

pub const PROTOCOL_VERSION: u32 = 1;
pub const OSC_STATUS_CODE: u16 = 7501;
pub const OSC_RESUME_HINT_CODE: u16 = 7577;
pub const PTY_REPLAY_RING_BYTES: usize = 1024 * 1024;
pub const SPACES_CONFIG_DIR_NAME: &str = ".spaces";

pub use ids::*;
