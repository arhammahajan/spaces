pub mod attach;
pub mod handoff;
pub mod ls;
pub mod spawn;

use spaces_types::{LeaseOwner, LeaseOwnerKind, LeaseState};

/// Returns the default human owner identifier from `$USER` or `"human"`.
pub fn default_human_owner_id() -> String {
    std::env::var("USER")
        .ok()
        .map(|s| s.trim().to_owned())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "human".to_owned())
}

/// Formats a [`LeaseOwnerKind`] as lowercase wire/display text.
pub fn format_owner_kind(kind: LeaseOwnerKind) -> &'static str {
    match kind {
        LeaseOwnerKind::Human => "human",
        LeaseOwnerKind::Agent => "agent",
        LeaseOwnerKind::System => "system",
    }
}

/// Formats a [`LeaseOwner`] as `<kind>:<id>`.
pub fn format_lease_owner(owner: &LeaseOwner) -> String {
    format!("{}:{}", format_owner_kind(owner.kind), owner.id)
}

/// Formats a [`LeaseState`] as lowercase snake_case text.
pub fn format_lease_state(state: LeaseState) -> &'static str {
    match state {
        LeaseState::Unowned => "unowned",
        LeaseState::Owned => "owned",
        LeaseState::Draining => "draining",
        LeaseState::Cancelling => "cancelling",
        LeaseState::StoppingAndFlushing => "stopping_and_flushing",
    }
}
