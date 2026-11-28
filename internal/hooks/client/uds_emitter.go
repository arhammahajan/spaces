package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	daemonEndpointFileName = "daemon.endpoint.json"
	daemonExpectedProtocol = "ndjson-rpc/uds"
	daemonProtocolVersion  = 1
	daemonClientKindHook   = "hook_client"
	daemonClientIDHook     = "go-hook-client"
	daemonDialTimeout      = 200 * time.Millisecond
	daemonIODeadline       = 200 * time.Millisecond
)

// DaemonEndpoint mirrors <SPACES_DIR|~/.spaces>/daemon.endpoint.json written by spacesd.
type DaemonEndpoint struct {
	Version   int    `json:"version"`
	Protocol  string `json:"protocol"`
	PID       int    `json:"pid"`
	UID       int    `json:"uid"`
	Socket    string `json:"socket"`
	Token     string `json:"token"`
	BootID    string `json:"boot_id"`
	StartedMs int64  `json:"started_ms"`
}

// HookEmitParams is the wire payload for the `hook.emit` NDJSON-RPC 2.0 method.
type HookEmitParams struct {
	Agent     string         `json:"agent"`
	Event     string         `json:"event,omitempty"`
	Session   string         `json:"session,omitempty"`
	Pane      string         `json:"pane,omitempty"`
	PaneRef   string         `json:"pane_ref,omitempty"`
	CWD       string         `json:"cwd,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Profile   string         `json:"profile,omitempty"`
	Title     string         `json:"title,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	TsMs      int64          `json:"ts_ms,omitempty"`
}

type udsRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type udsRPCAuthParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClientKind      string `json:"client_kind"`
	ClientID        string `json:"client_id"`
	Token           string `json:"token"`
}

type udsRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type udsRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *udsRPCError    `json:"error,omitempty"`
}

type udsRPCAuthResult struct {
	OK bool `json:"ok"`
}

// ResolveSpacesDir returns the active Spaces runtime directory (SPACES_DIR or ~/.spaces).
func ResolveSpacesDir() string {
	if env := strings.TrimSpace(os.Getenv("SPACES_DIR")); env != "" {
		return env
	}
	if h := homeDir(); h != "" {
		return filepath.Join(h, ".spaces")
	}
	return ".spaces"
}

// LoadDaemonEndpoint reads and validates <SPACES_DIR|~/.spaces>/daemon.endpoint.json.
func LoadDaemonEndpoint() (DaemonEndpoint, error) {
	path := filepath.Join(ResolveSpacesDir(), daemonEndpointFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return DaemonEndpoint{}, err
	}
	var ep DaemonEndpoint
	if err := json.Unmarshal(raw, &ep); err != nil {
		return DaemonEndpoint{}, err
	}
	if ep.Protocol != daemonExpectedProtocol {
		return DaemonEndpoint{}, fmt.Errorf("unsupported daemon protocol %q", ep.Protocol)
	}
	if strings.TrimSpace(ep.Socket) == "" {
		return DaemonEndpoint{}, errors.New("daemon endpoint socket is empty")
	}
	if strings.TrimSpace(ep.Token) == "" {
		return DaemonEndpoint{}, errors.New("daemon endpoint token is empty")
	}
	return ep, nil
}

// EmitHookToDaemon sends a single hook event to spacesd over UDS NDJSON-RPC 2.0.
// It returns true on a successful rpc.auth + hook.emit exchange, and false
// silently whenever spacesd is unavailable or rejects the call.
func EmitHookToDaemon(params HookEmitParams) bool {
	ep, err := LoadDaemonEndpoint()
	if err != nil {
		return false
	}
	return emitHookToEndpoint(ep, params) == nil
}

func emitHookToEndpoint(ep DaemonEndpoint, params HookEmitParams) error {
	conn, err := net.DialTimeout("unix", ep.Socket, daemonDialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(daemonIODeadline)); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)

	authReq := udsRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "rpc.auth",
		Params: udsRPCAuthParams{
			ProtocolVersion: daemonProtocolVersion,
			ClientKind:      daemonClientKindHook,
			ClientID:        daemonClientIDHook,
			Token:           ep.Token,
		},
	}
	authResp, err := roundTripUDS(conn, reader, authReq)
	if err != nil {
		return err
	}
	if authResp.Error != nil {
		return fmt.Errorf("rpc.auth failed (%d): %s", authResp.Error.Code, authResp.Error.Message)
	}
	var authRes udsRPCAuthResult
	if len(authResp.Result) > 0 {
		if err := json.Unmarshal(authResp.Result, &authRes); err != nil {
			return err
		}
		if !authRes.OK {
			return errors.New("rpc.auth rejected")
		}
	}

	emitReq := udsRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "hook.emit",
		Params:  params,
	}
	emitResp, err := roundTripUDS(conn, reader, emitReq)
	if err != nil {
		return err
	}
	if emitResp.Error != nil {
		return fmt.Errorf("hook.emit failed (%d): %s", emitResp.Error.Code, emitResp.Error.Message)
	}
	return nil
}

func roundTripUDS(conn net.Conn, reader *bufio.Reader, req udsRPCRequest) (udsRPCResponse, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return udsRPCResponse{}, err
	}
	encoded = append(encoded, '\n')
	if _, err := conn.Write(encoded); err != nil {
		return udsRPCResponse{}, err
	}

	line, err := reader.ReadBytes('\n')
	if err != nil {
		return udsRPCResponse{}, err
	}
	var resp udsRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return udsRPCResponse{}, err
	}
	return resp, nil
}

func buildHookEmitParams(
	agentName, event, session, pane, paneRef string,
	payload, normalized map[string]any,
	envMap map[string]string,
) HookEmitParams {
	params := HookEmitParams{
		Agent:   agentName,
		Event:   event,
		Session: session,
		Pane:    pane,
		PaneRef: paneRef,
		Payload: payload,
	}
	if NowFunc != nil {
		params.TsMs = NowFunc().UnixMilli()
	} else {
		params.TsMs = time.Now().UnixMilli()
	}

	if normalized != nil {
		if v, _ := normalized["cwd"].(string); strings.TrimSpace(v) != "" {
			params.CWD = v
		}
		if v, _ := normalized["session_id"].(string); strings.TrimSpace(v) != "" {
			params.SessionID = v
		}
		if v, _ := normalized["profile"].(string); strings.TrimSpace(v) != "" {
			params.Profile = v
		}
		if v, _ := normalized["title"].(string); strings.TrimSpace(v) != "" {
			params.Title = v
		}
	}
	if params.SessionID == "" && payload != nil {
		params.SessionID = ExtractSessionID(payload)
	}
	if params.CWD == "" && payload != nil {
		if v, _ := payload["cwd"].(string); strings.TrimSpace(v) != "" {
			params.CWD = v
		}
	}
	if params.CWD == "" {
		params.CWD = CurrentDirectory()
	}
	if params.Profile == "" && envMap != nil {
		if v := strings.TrimSpace(envMap["HARNESS_PROFILE"]); v != "" {
			params.Profile = v
		}
	}
	return params
}
