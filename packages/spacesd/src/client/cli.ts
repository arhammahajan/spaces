#!/usr/bin/env bun
import { RPC_METHODS, type BackendVerb } from "@spaces/types";
import { SpacesRpcClient } from "./rpc-client";

async function main(): Promise<void> {
  const [cmd, ...args] = process.argv.slice(2);
  if (!cmd) {
    console.error("usage: cli.ts <open-space|focus-pane|new-window|kill-window|create-space|launch-agent|exec|spaces|ping> [...]");
    process.exit(2);
  }

  const client = new SpacesRpcClient();

  switch (cmd) {
    case "ping": {
      const res = await client.call(RPC_METHODS.DAEMON_PING);
      console.log(JSON.stringify(res));
      return;
    }
    case "spaces": {
      const res = await client.call(RPC_METHODS.BACKEND_SPACES_LIST);
      console.log(JSON.stringify(res));
      return;
    }
    case "open-space": {
      const [name] = args;
      if (!name) throw new Error("open-space requires <name>");
      await client.execVerb({ verb: "open_space", name });
      return;
    }
    case "focus-pane": {
      const [session, window_id, pane_id] = args;
      if (!session || !window_id || !pane_id) {
        throw new Error("focus-pane requires <session> <window_id> <pane_id>");
      }
      await client.execVerb({ verb: "focus_pane", session, window_id, pane_id });
      return;
    }
    case "new-window": {
      const [session, cwd, name] = args;
      if (!session) throw new Error("new-window requires <session> [cwd] [name]");
      await client.execVerb({
        verb: "new_window",
        session,
        cwd: cwd ?? "",
        name: name ?? "",
      });
      return;
    }
    case "kill-window": {
      const [window_id] = args;
      if (!window_id) throw new Error("kill-window requires <window_id>");
      await client.execVerb({ verb: "kill_window", window_id });
      return;
    }
    case "create-space": {
      const [name, cwd] = args;
      if (!name) throw new Error("create-space requires <name> [cwd]");
      await client.execVerb({ verb: "create_space", name, cwd: cwd ?? "" });
      return;
    }
    case "launch-agent": {
      const [session, cwd, agent, profile] = args;
      if (!agent) throw new Error("launch-agent requires <session> <cwd> <agent> [profile]");
      await client.execVerb({
        verb: "launch_agent",
        session: session ?? "",
        cwd: cwd ?? "",
        agent,
        profile: profile ?? "",
      });
      return;
    }
    case "exec": {
      const [rawJson] = args;
      if (!rawJson) throw new Error("exec requires a BackendVerb JSON string");
      const verb = JSON.parse(rawJson) as BackendVerb;
      const res = await client.execVerb(verb);
      console.log(JSON.stringify(res));
      return;
    }
    case "resurrect": {
      const [sub, ...rest] = args;
      if (sub === "autosave") {
        const force = rest.includes("--force") || rest.includes("-f");
        const res = await client.call("resurrect.autosave", { force });
        console.log(JSON.stringify(res));
        return;
      }
      if (sub === "auto-restore") {
        const res = await client.call("resurrect.auto_restore");
        console.log(JSON.stringify(res));
        return;
      }
      throw new Error(`unknown resurrect subcommand: ${sub ?? ""}`);
    }
    default:
      throw new Error(`unknown command: ${cmd}`);
  }
}

if (import.meta.main) {
  main().catch((err) => {
    console.error(err instanceof Error ? err.message : String(err));
    process.exit(1);
  });
}
