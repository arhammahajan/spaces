import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  type BackendConversationsListResult,
  type BackendConversationsSearchResult,
  type BackendExecResult,
  type RpcResponse,
} from "@spaces/types";
import type { MultiplexerDriver } from "../drivers/types";
import { encodeClaudeProjectSlug } from "../plugins/claude";
import { createDefaultPluginRegistry } from "../plugins/providers/index";
import { SpacesDaemon } from "../server/daemon";
import { ConversationService } from "./service";

interface UdsClient {
  request<T = unknown>(method: string, params?: unknown): Promise<RpcResponse & { result?: T }>;
  close(): void;
}

async function connectUds(socketPath: string): Promise<UdsClient> {
  let nextId = 1;
  let buffer = "";
  const pending = new Map<number, (resp: RpcResponse) => void>();

  const socket = await Bun.connect({
    unix: socketPath,
    socket: {
      data(_socket, chunk) {
        buffer += Buffer.from(chunk).toString("utf8");
        let idx = buffer.indexOf("\n");
        while (idx !== -1) {
          const line = buffer.slice(0, idx).trim();
          buffer = buffer.slice(idx + 1);
          if (line.length > 0) {
            const msg = JSON.parse(line) as RpcResponse;
            if (msg.id !== undefined) {
              const resolver = pending.get(Number(msg.id));
              if (resolver) {
                pending.delete(Number(msg.id));
                resolver(msg);
              }
            }
          }
          idx = buffer.indexOf("\n");
        }
      },
      open() {},
      close() {},
      error() {},
    },
  });

  return {
    request<T = unknown>(method: string, params?: unknown) {
      const id = nextId++;
      return new Promise<RpcResponse & { result?: T }>((resolve) => {
        pending.set(id, resolve as (resp: RpcResponse) => void);
        socket.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
      });
    },
    close() {
      socket.end();
    },
  };
}

describe("Step 8 E2E: all 33 plugins + conversations.list/search + open_conversation/open_outcome", () => {
  const tempDirs: string[] = [];
  const daemons: SpacesDaemon[] = [];

  afterEach(async () => {
    while (daemons.length > 0) {
      await daemons.pop()!.stop();
    }
    while (tempDirs.length > 0) {
      rmSync(tempDirs.pop()!, { recursive: true, force: true });
    }
  });

  test("registers all 33 providers and serves conversations.list, search, open_conversation & open_outcome over UDS", async () => {
    const root = mkdtempSync(path.join(os.tmpdir(), "spaces-step8-e2e-"));
    tempDirs.push(root);
    const homeDir = path.join(root, "home");
    const spacesDir = path.join(root, "spaces");
    const repoDir = path.join(root, "repo");
    mkdirSync(repoDir, { recursive: true });

    // Seed a Claude JSONL conversation
    const claudeProjDir = path.join(
      homeDir,
      ".claude",
      "projects",
      encodeClaudeProjectSlug(repoDir),
    );
    mkdirSync(claudeProjDir, { recursive: true });
    writeFileSync(
      path.join(claudeProjDir, "sess_claude_8.jsonl"),
      [
        JSON.stringify({ type: "custom-title", customTitle: "Refactor ACP Subsystem" }),
        JSON.stringify({
          type: "user",
          cwd: repoDir,
          sessionId: "sess_claude_8",
          timestamp: "2026-11-28T12:00:00.000Z",
          message: { role: "user", content: "Design the ACP bidirectional JSON-RPC bridge" },
        }),
      ].join("\n") + "\n",
    );

    // Seed a Codex rollout JSONL conversation
    const codexDir = path.join(homeDir, ".codex", "sessions", "2026", "11", "28");
    mkdirSync(codexDir, { recursive: true });
    writeFileSync(
      path.join(codexDir, "rollout-2026-11-28T12-05-00-sess_codex_8.jsonl"),
      [
        JSON.stringify({
          timestamp: "2026-11-28T12:05:00.000Z",
          type: "session_meta",
          payload: { id: "sess_codex_8", cwd: repoDir },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T12:05:01.000Z",
          type: "event_msg",
          payload: { type: "user_message", message: "Implement Ratatui layout tree" },
        }),
      ].join("\n") + "\n",
    );

    const openedWindows: Array<{ session: string; cwd: string; label: string; cmd?: string[] }> = [];
    const fakeDriver = {
      kind: "tmux",
      serverRunning: async () => true,
      currentSession: async () => "dev",
      listSessions: async () => [{ name: "dev" }],
      newWindow: async (session: string, cwd: string, label: string, cmd?: string[]) => {
        openedWindows.push({ session, cwd, label, cmd });
      },
      switchToSpace: async () => {},
    } as unknown as MultiplexerDriver;

    const registry = createDefaultPluginRegistry({ homeDir, spacesDir });
    expect(registry.list()).toHaveLength(33);
    expect(registry.runtimePlugins()).toHaveLength(29);
    expect(registry.allMcpTargets().length).toBeGreaterThanOrEqual(20);
    expect(registry.asResumePolicy().uniqueExplicitSessions?.("codex")).toBe(true);
    expect(registry.asResumePolicy().resumeArgs?.("antigravity", "conv-1")).toEqual([
      "--conversation",
      "conv-1",
    ]);

    const daemon = new SpacesDaemon({
      spacesDir,
      plugins: registry,
      driver: fakeDriver,
      conversations: new ConversationService({
        registry,
        homeDir,
        spacesDir,
      }),
    });
    daemons.push(daemon);
    const endpoint = daemon.start();

    const client = await connectUds(endpoint.socket);
    try {
      await client.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "step8-e2e",
        protocol_version: PROTOCOL_VERSION,
      });

      // 1. backend.conversations.list
      const listed = await client.request<BackendConversationsListResult>(
        RPC_METHODS.BACKEND_CONVERSATIONS_LIST,
        { refresh: true, include_archived: false },
      );
      expect(listed.error).toBeUndefined();
      const ids = listed.result!.snapshot.conversations.map((e) => e.id);
      expect(ids).toContain("sess_claude_8");
      expect(ids).toContain("sess_codex_8");

      // 2. backend.conversations.search (message body + SnippetSpan)
      const searched = await client.request<BackendConversationsSearchResult>(
        RPC_METHODS.BACKEND_CONVERSATIONS_SEARCH,
        {
          request: {
            query: "Ratatui",
            scope: "all",
            limit: 10,
            include_archived: false,
          },
        },
      );
      expect(searched.error).toBeUndefined();
      expect(searched.result!.hits.length).toBe(1);
      expect(searched.result!.hits[0]!.conversation.id).toBe("sess_codex_8");
      expect(searched.result!.hits[0]!.snippet_highlights.length).toBeGreaterThan(0);

      // 3. BackendVerb::OpenConversation
      const claudeEntry = listed.result!.snapshot.conversations.find((e) => e.id === "sess_claude_8")!;
      const openedConv = await client.request<BackendExecResult>(RPC_METHODS.BACKEND_EXEC, {
        verb: { verb: "open_conversation", conversation: claudeEntry, force_new_tab: false },
      });
      expect(openedConv.error).toBeUndefined();
      expect(openedWindows).toHaveLength(1);
      expect(openedWindows[0]!.cmd).toContain("--resume");
      expect(openedWindows[0]!.cmd).toContain("sess_claude_8");

      // 4. BackendVerb::OpenOutcome
      daemon.getHookIngestor().recordOutcome({
        session: "dev",
        pane: "1",
        agent: "codex",
        app: "codex",
        profile: "",
        outcome: "done",
        text: "Finished build",
        title: "Codex Build",
        model: "gpt-5",
        session_id: "sess_codex_8",
        cwd: repoDir,
      });
      const outcomesBefore = daemon.getStore().listOutcomes();
      expect(outcomesBefore).toHaveLength(1);

      const openedOutcome = await client.request<BackendExecResult>(RPC_METHODS.BACKEND_EXEC, {
        verb: { verb: "open_outcome", outcome: outcomesBefore[0]! },
      });
      expect(openedOutcome.error).toBeUndefined();
      expect(openedWindows).toHaveLength(2);
      expect(openedWindows[1]!.cmd).toContain("resume");
      expect(openedWindows[1]!.cmd).toContain("sess_codex_8");
      expect(daemon.getStore().listOutcomes()).toHaveLength(0);
    } finally {
      client.close();
    }
  });
});
