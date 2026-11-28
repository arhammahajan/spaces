export { ThreadActor, ThreadActorRegistry } from "./core/actor";
export {
  EventBus,
  matchesEventFilter,
  type EventBusOptions,
  type EventStoreAdapter,
  type PublishEventOptions,
  type SubscribeResult,
  type TailResult,
} from "./core/event-bus";
export {
  SAFE_RESTORE_COMMANDS,
  canonicalResumeHintPayload,
  generatePaneSecret,
  signOscResumeHint,
  validateResumeCommand,
  verifyOscResumeHint,
  type ValidateResumeCommandOptions,
  type ValidateResumeCommandResult,
} from "./core/hmac";
export {
  LeaseError,
  LeaseMachine,
  type LeaseCancelInput,
  type LeaseClaimInput,
  type LeaseHandoffInput,
  type LeaseHooks,
  type LeaseMachineOptions,
  type LeaseStoreAdapter,
  type LeaseTransitionResult,
  type LeaseYieldInput,
} from "./core/lease-fsm";
export {
  AUTH_TTL_MS,
  BLOCK_TTL_MS,
  DONE_TTL_MS,
  FAILED_TTL_MS,
  MAX_OUTCOMES,
  OUTCOME_DEDUPE_MS,
  OUTCOME_TTL_MS,
  SpacesStore,
  WORKING_STALE_MS,
  canonicalConversationAgent,
  type ConversationOverlayInput,
  type ConversationOverlayRecord,
  type EventBounds,
  type HookChildInput,
  type HookPaneInput,
  type HookPaneRecord,
  type OutcomeInput,
  type PaneHmacTokenRecord,
  type RecordOutcomeResult,
  type RemoveOutcomeOptions,
  type ResurrectOpenState,
  type ResurrectPaneSnapshot,
  type ResurrectSpaceSnapshot,
  type ResurrectWindowSnapshot,
  type ThreadPatch,
  type TurnPatch,
  type VerifiedResumeHintInput,
  type VerifiedResumeHintRecord,
} from "./store/db";
export { SCHEMA_VERSION, openSpacesDatabase } from "./store/schema";
export {
  MAX_RPC_FRAME_BYTES,
  SPACESD_VERSION,
  SpacesDaemon,
  type DaemonEndpointMetadata,
  type SpacesDaemonOptions,
} from "./server/daemon";
export * from "./detection/index";
export * from "./drivers/types";
export * from "./drivers/tmux";
export * from "./resurrect/capture";
export * from "./resurrect/restore";
export * from "./backend/service";
export * from "./client/rpc-client";
export * from "./plugins/types";
export * from "./plugins/profiles";
export * from "./plugins/registry";
export * from "./plugins/claude";
export * from "./executors/claude";
export * from "./git/driver";
export * from "./git/diff-parser";
export * from "./git/collector";
export * from "./worktree/naming";
export * from "./worktree/manager";
export * from "./forge/types";
export * from "./forge/http";
export * from "./forge/registry";
export * from "./forge/github";
export * from "./forge/gitlab";
export * from "./forge/forgejo";
export * from "./issues/types";
export * from "./issues/registry";
export * from "./shared/index";
export {
  HookIngestor,
  canonicalAgent as canonicalHookAgent,
  canonicalEvent,
  displayApp,
  eventState,
  normalizeHookEvent,
  type HookIngestorOptions,
  type NormalizedHookEvent,
} from "./hooks/ingest";
export * from "./hooks/statusbar";

import { SpacesDaemon } from "./server/daemon";

if (import.meta.main) {
  const daemon = new SpacesDaemon();
  const endpoint = daemon.start();
  console.log(
    JSON.stringify({
      event: "spacesd_started",
      pid: endpoint.pid,
      boot_id: endpoint.boot_id,
      socket: endpoint.socket,
    }),
  );

  const shutdown = () => {
    void daemon.stop().then(() => process.exit(0));
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}
