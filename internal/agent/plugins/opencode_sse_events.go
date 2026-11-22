package plugins

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	openCodeMaxTextLen   = 240
	openCodeMaxDetailLen = 2048
)

type openCodeChildState struct {
	ID       string
	ParentID string
	Title    string
	Type     string
	State    string
	SinceMs  int64
}

type openCodeMessageMeta struct {
	SessionID string
	Role      string
	Model     string
}

// OpenCodeEventMapper is a stateful translator that converts OpenCode v2 SSE
// JSON payloads (both direct and /global/event wrapped) into normalized
// provider-agnostic agent.DaemonEvent records.
type OpenCodeEventMapper struct {
	mu            sync.Mutex
	rootSessionID string
	rootTitle     string
	cwd           string
	profile       string
	nowFn         func() int64
	children      map[string]*openCodeChildState
	messages      map[string]openCodeMessageMeta
}

// NewOpenCodeEventMapper creates a new stateful OpenCode SSE event mapper
// initialized with pane metadata from target.
func NewOpenCodeEventMapper(target agent.DaemonWatchTarget) *OpenCodeEventMapper {
	return &OpenCodeEventMapper{
		cwd:      strings.TrimSpace(target.CWD),
		profile:  strings.TrimSpace(target.Profile),
		children: make(map[string]*openCodeChildState),
		messages: make(map[string]openCodeMessageMeta),
	}
}

func (m *OpenCodeEventMapper) nowMs() int64 {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now().UnixMilli()
}

func (m *OpenCodeEventMapper) ensureMaps() {
	if m.children == nil {
		m.children = make(map[string]*openCodeChildState)
	}
	if m.messages == nil {
		m.messages = make(map[string]openCodeMessageMeta)
	}
}

// Map parses a single SSE event (eventName + JSON data) into zero or more
// normalized agent.DaemonEvent values.
func (m *OpenCodeEventMapper) Map(eventName, data string) []agent.DaemonEvent {
	raw := strings.TrimSpace(data)
	if raw == "" {
		return nil
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(raw), &top); err != nil || top == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMaps()

	evObj := top
	eventDir := ocFirstStr(top, "directory", "cwd")
	if payload, ok := top["payload"].(map[string]any); ok && payload != nil {
		if ocFirstStr(top, "type") == "" || ocFirstStr(payload, "type") != "" {
			evObj = payload
		}
	}

	eventType := strings.TrimSpace(ocFirstStr(evObj, "type"))
	if eventType == "" {
		eventType = strings.TrimSpace(eventName)
	}
	if eventType == "" {
		return nil
	}

	props := ocMapAt(evObj, "properties")
	if props == nil {
		props = evObj
	}
	if eventDir == "" {
		eventDir = ocFirstStr(props, "directory", "cwd")
	}
	if eventDir != "" && m.cwd == "" {
		m.cwd = eventDir
	}
	cwd := eventDir
	if cwd == "" {
		cwd = m.cwd
	}

	tsMs := ocExtractTimestampMs(evObj, props)
	if tsMs <= 0 {
		tsMs = m.nowMs()
	}

	base := agent.DaemonEvent{
		Agent:    "opencode",
		App:      "opencode",
		Source:   "daemon_sse",
		RawEvent: eventType,
		CWD:      cwd,
		Profile:  m.profile,
		TsMs:     tsMs,
	}

	switch eventType {
	case "server.connected", "server.heartbeat":
		return nil

	case "session.status":
		return m.mapSessionStatus(base, props)

	case "session.busy", "session.execution.started":
		sid := ocSessionID(props)
		return m.mapSessionRunning(base, sid, "working")

	case "session.idle", "session.execution.succeeded", "session.execution.interrupted":
		sid := ocSessionID(props)
		text := "turn complete"
		if eventType == "session.execution.interrupted" {
			text = "turn interrupted"
		}
		return m.mapSessionDone(base, sid, text)

	case "session.error", "session.execution.failed":
		sid := ocSessionID(props)
		errText := ocExtractError(ocFirstVal(props, "error", "message", "reason"))
		return m.mapSessionError(base, sid, errText)

	case "session.created", "session.updated":
		return m.mapSessionLifecycle(base, eventType, props)

	case "session.deleted":
		return m.mapSessionDeleted(base, props)

	case "tool.execute.before":
		return m.mapToolBefore(base, props)

	case "tool.execute.after":
		return m.mapToolAfter(base, props)

	case "permission.asked", "permission.updated":
		return m.mapPermissionAsked(base, props)

	case "permission.replied":
		return m.mapPermissionReplied(base, props)

	case "question.asked":
		return m.mapQuestionAsked(base, props)

	case "question.replied", "question.rejected":
		return m.mapQuestionReplied(base, eventType, props)

	case "message.updated":
		return m.mapMessageUpdated(base, props)

	case "message.part.updated":
		return m.mapMessagePartUpdated(base, props)
	}

	return nil
}

func (m *OpenCodeEventMapper) rootOrParent(child *openCodeChildState) string {
	if m.rootSessionID != "" {
		return m.rootSessionID
	}
	if child != nil && child.ParentID != "" {
		return child.ParentID
	}
	return ""
}

func (m *OpenCodeEventMapper) setRootSession(sid string) string {
	sid = strings.TrimSpace(sid)
	if sid != "" {
		if _, isChild := m.children[sid]; !isChild {
			m.rootSessionID = sid
		}
	}
	return m.rootSessionID
}

func (m *OpenCodeEventMapper) mapSessionStatus(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	var statusType, retryMsg string
	var attempt int64
	if stObj := ocMapAt(props, "status"); stObj != nil {
		statusType = strings.ToLower(ocFirstStr(stObj, "type", "status", "state"))
		retryMsg = ocFirstStr(stObj, "message", "reason", "error")
		attempt = ocInt64(ocFirstVal(stObj, "attempt"))
	} else {
		statusType = strings.ToLower(ocFirstStr(props, "status", "state"))
		retryMsg = ocFirstStr(props, "message", "reason")
	}

	switch statusType {
	case "busy", "running":
		return m.mapSessionRunning(base, sid, "working")
	case "idle", "done", "completed":
		return m.mapSessionDone(base, sid, "turn complete")
	case "retry", "retrying":
		text := "retrying"
		if retryMsg != "" {
			text = "retrying: " + ocOneLine(retryMsg, openCodeMaxTextLen)
		} else if attempt > 0 {
			text = fmt.Sprintf("retrying (attempt %d)", attempt)
		}
		if child, ok := m.children[sid]; ok {
			child.State = "working"
			ev := base
			ev.Event = "subagent_start"
			ev.SessionID = m.rootOrParent(child)
			ev.Text = text
			ev.Child = &agent.DaemonChildEvent{
				ID:      child.ID,
				Type:    child.Type,
				State:   "working",
				Text:    text,
				SinceMs: child.SinceMs,
				TsMs:    base.TsMs,
			}
			return []agent.DaemonEvent{ev}
		}
		ev := base
		ev.Event = "working"
		ev.State = "working"
		ev.SessionID = m.setRootSession(sid)
		ev.Text = text
		return []agent.DaemonEvent{ev}
	case "error", "failed":
		return m.mapSessionError(base, sid, retryMsg)
	}
	return nil
}

func (m *OpenCodeEventMapper) mapSessionRunning(base agent.DaemonEvent, sid, text string) []agent.DaemonEvent {
	if child, ok := m.children[sid]; ok {
		child.State = "working"
		childText := child.Title
		if childText == "" {
			childText = text
		}
		ev := base
		ev.Event = "subagent_start"
		ev.SessionID = m.rootOrParent(child)
		ev.Text = "subagent: " + ocOneLine(childText, openCodeMaxTextLen)
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "working",
			Text:    childText,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}
	ev := base
	ev.Event = "prompt"
	ev.State = "working"
	ev.SessionID = m.setRootSession(sid)
	ev.Text = text
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapSessionDone(base agent.DaemonEvent, sid, text string) []agent.DaemonEvent {
	if child, ok := m.children[sid]; ok {
		child.State = "done"
		childText := child.Title
		if childText == "" {
			childText = text
		}
		ev := base
		ev.Event = "subagent_stop"
		ev.SessionID = m.rootOrParent(child)
		ev.Text = "subagent done: " + ocOneLine(childText, openCodeMaxTextLen)
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "done",
			Outcome: "done",
			Text:    childText,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
			EndedMs: base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}
	ev := base
	ev.Event = "stop"
	ev.State = "done"
	ev.SessionID = m.setRootSession(sid)
	ev.Text = text
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapSessionError(base agent.DaemonEvent, sid, errText string) []agent.DaemonEvent {
	msg := ocOneLine(errText, openCodeMaxTextLen)
	if child, ok := m.children[sid]; ok {
		child.State = "failed"
		if msg == "" {
			msg = "subagent failed"
		}
		ev := base
		ev.Event = "subagent_stop"
		ev.SessionID = m.rootOrParent(child)
		ev.Text = "subagent failed: " + msg
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "failed",
			Outcome: "failed",
			Text:    msg,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
			EndedMs: base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}
	ev := base
	ev.Event = "stop_failure"
	ev.State = "failed"
	ev.SessionID = m.setRootSession(sid)
	if msg != "" {
		ev.Text = "error: " + msg
	} else {
		ev.Text = "error: turn failed"
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapSessionLifecycle(base agent.DaemonEvent, eventType string, props map[string]any) []agent.DaemonEvent {
	info := ocMapAt(props, "info")
	if info == nil {
		info = props
	}
	sid := ocFirstStr(info, "id", "sessionID", "session_id")
	if sid == "" {
		sid = ocSessionID(props)
	}
	parentID := ocFirstStr(info, "parentID", "parent_id")
	if parentID == "" {
		parentID = ocFirstStr(props, "parentID", "parent_id")
	}
	title := ocFirstStr(info, "title")
	if title == "" {
		title = ocFirstStr(props, "title")
	}
	dir := ocFirstStr(info, "directory", "cwd")
	if dir != "" {
		base.CWD = dir
		if parentID == "" {
			m.cwd = dir
		}
	}

	if parentID != "" && sid != "" {
		child, exists := m.children[sid]
		if !exists {
			child = &openCodeChildState{
				ID:       sid,
				ParentID: parentID,
				Title:    title,
				Type:     "subagent",
				State:    "working",
				SinceMs:  base.TsMs,
			}
			m.children[sid] = child
		} else {
			if title != "" {
				child.Title = title
			}
			if child.ParentID == "" {
				child.ParentID = parentID
			}
		}
		if m.rootSessionID == "" {
			if _, parentIsChild := m.children[parentID]; !parentIsChild {
				m.rootSessionID = parentID
			}
		}
		desc := child.Title
		if desc == "" {
			desc = sid
		}
		ev := base
		ev.Event = "subagent_start"
		ev.State = "working"
		ev.SessionID = m.rootOrParent(child)
		ev.Text = "subagent: " + ocOneLine(desc, openCodeMaxTextLen)
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "working",
			Text:    desc,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}

	rootID := m.setRootSession(sid)
	if title != "" {
		m.rootTitle = title
	}

	ev := base
	ev.SessionID = rootID
	ev.Title = title
	if eventType == "session.created" {
		ev.Event = "session_start"
		ev.State = "idle"
		ev.Text = "session started"
	} else {
		ev.Event = "session_updated"
		ev.Text = ocOneLine(title, openCodeMaxTextLen)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapSessionDeleted(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	info := ocMapAt(props, "info")
	sid := ""
	if info != nil {
		sid = ocFirstStr(info, "id", "sessionID", "session_id")
	}
	if sid == "" {
		sid = ocFirstStr(props, "sessionID", "session_id", "id")
	}
	if child, ok := m.children[sid]; ok {
		child.State = "done"
		desc := child.Title
		if desc == "" {
			desc = child.ID
		}
		ev := base
		ev.Event = "subagent_stop"
		ev.SessionID = m.rootOrParent(child)
		ev.Text = "subagent done: " + ocOneLine(desc, openCodeMaxTextLen)
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "done",
			Outcome: "done",
			Text:    desc,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
			EndedMs: base.TsMs,
		}
		delete(m.children, sid)
		return []agent.DaemonEvent{ev}
	}
	ev := base
	ev.Event = "session_end"
	ev.State = "idle"
	ev.SessionID = m.setRootSession(sid)
	ev.Text = "session ended"
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapToolBefore(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	tool := ocFirstStr(props, "tool", "name", "tool_name", "toolName")
	inputVal := ocFirstVal(props, "args", "input", "arguments", "tool_input", "toolInput")
	toolInput := ocFormatPayload(inputVal, openCodeMaxDetailLen)
	preview := ocToolPreview(tool, inputVal)

	if child, ok := m.children[sid]; ok {
		child.State = "working"
		ev := base
		ev.Event = "subagent_start"
		ev.SessionID = m.rootOrParent(child)
		ev.Tool = tool
		ev.ToolInput = toolInput
		ev.Text = preview
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "working",
			Text:    preview,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}

	ev := base
	ev.Event = "pre_tool"
	ev.SessionID = m.setRootSession(sid)
	ev.Tool = tool
	ev.ToolInput = toolInput
	ev.Text = preview
	if ocToolIsAsk(tool) {
		ev.State = "blocked"
		ev.Kind = "question"
	} else {
		ev.State = "working"
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapToolAfter(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	tool := ocFirstStr(props, "tool", "name", "tool_name", "toolName")
	inputVal := ocFirstVal(props, "args", "input", "arguments", "tool_input", "toolInput")
	outputVal := ocFirstVal(props, "output", "result", "tool_output", "toolOutput")
	errText := ocExtractError(ocFirstVal(props, "error"))
	title := ocFirstStr(props, "title")

	toolInput := ocFormatPayload(inputVal, openCodeMaxDetailLen)
	toolOutput := ocFormatPayload(outputVal, openCodeMaxDetailLen)
	preview := title
	if preview == "" {
		preview = ocToolPreview(tool, inputVal)
	}

	if child, ok := m.children[sid]; ok {
		ev := base
		ev.Event = "subagent_start"
		ev.SessionID = m.rootOrParent(child)
		ev.Tool = tool
		ev.ToolInput = toolInput
		ev.ToolOutput = toolOutput
		ev.Text = preview
		ev.Child = &agent.DaemonChildEvent{
			ID:      child.ID,
			Type:    child.Type,
			State:   "working",
			Text:    preview,
			SinceMs: child.SinceMs,
			TsMs:    base.TsMs,
		}
		return []agent.DaemonEvent{ev}
	}

	ev := base
	ev.SessionID = m.setRootSession(sid)
	ev.Tool = tool
	ev.ToolInput = toolInput
	ev.ToolOutput = toolOutput
	ev.State = "working"
	if errText != "" {
		ev.Event = "tool_failure"
		ev.Text = "tool failed: " + ocOneLine(errText, openCodeMaxTextLen)
	} else {
		ev.Event = "post_tool"
		ev.Text = ocOneLine(preview, openCodeMaxTextLen)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapPermissionAsked(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	tool := ocFirstStr(props, "permission", "type", "tool", "tool_name", "toolName")
	title := ocFirstStr(props, "title", "message")
	pattern := ocFirstStr(props, "pattern")
	if pattern == "" {
		if pats, ok := props["patterns"].([]any); ok && len(pats) > 0 {
			var parts []string
			for _, p := range pats {
				if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			pattern = strings.Join(parts, ", ")
		}
	}
	if pattern == "" {
		if meta := ocMapAt(props, "metadata"); meta != nil {
			pattern = ocFirstStr(meta, "command", "path", "filePath", "filepath", "pattern", "url")
		}
	}

	var desc string
	switch {
	case tool != "" && pattern != "":
		desc = "approve " + tool + ": " + pattern
	case title != "" && pattern != "":
		desc = "approve " + title + ": " + pattern
	case title != "":
		desc = "approve " + title
	case tool != "":
		desc = "approve " + tool
	case pattern != "":
		desc = "approve " + pattern
	default:
		desc = "needs approval"
	}

	ev := base
	ev.Event = "permission"
	ev.State = "blocked"
	ev.Kind = "permission"
	ev.Tool = tool
	ev.ToolInput = ocOneLine(pattern, openCodeMaxDetailLen)
	ev.Text = ocOneLine(desc, openCodeMaxTextLen)
	if _, isChild := m.children[sid]; isChild {
		ev.SessionID = m.rootOrParent(m.children[sid])
	} else {
		ev.SessionID = m.setRootSession(sid)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapPermissionReplied(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	reply := ocFirstStr(props, "reply", "response", "decision")
	ev := base
	ev.Event = "working"
	ev.State = "working"
	if reply != "" {
		ev.Text = "permission: " + ocOneLine(reply, openCodeMaxTextLen)
	} else {
		ev.Text = "permission replied"
	}
	if child, isChild := m.children[sid]; isChild {
		ev.SessionID = m.rootOrParent(child)
	} else {
		ev.SessionID = m.setRootSession(sid)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapQuestionAsked(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	qText := ocFirstStr(props, "question", "prompt", "text", "title", "message")
	if qText == "" {
		if qs, ok := props["questions"].([]any); ok && len(qs) > 0 {
			if first, ok := qs[0].(map[string]any); ok {
				qText = ocFirstStr(first, "question", "header", "text", "title")
			} else if s, ok := qs[0].(string); ok {
				qText = s
			}
		}
	}
	ev := base
	ev.Event = "elicitation"
	ev.State = "blocked"
	ev.Kind = "question"
	if qText != "" {
		ev.Text = "answer: " + ocOneLine(qText, openCodeMaxTextLen)
	} else {
		ev.Text = "answer: needs an answer"
	}
	if child, isChild := m.children[sid]; isChild {
		ev.SessionID = m.rootOrParent(child)
	} else {
		ev.SessionID = m.setRootSession(sid)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapQuestionReplied(base agent.DaemonEvent, eventType string, props map[string]any) []agent.DaemonEvent {
	sid := ocSessionID(props)
	ev := base
	ev.Event = "elicitation_result"
	ev.State = "working"
	if eventType == "question.rejected" {
		ev.Text = "question rejected"
	} else {
		ev.Text = "question answered"
	}
	if child, isChild := m.children[sid]; isChild {
		ev.SessionID = m.rootOrParent(child)
	} else {
		ev.SessionID = m.setRootSession(sid)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapMessageUpdated(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	info := ocMapAt(props, "info")
	if info == nil {
		info = props
	}
	msgID := ocFirstStr(info, "id", "messageID", "message_id")
	sid := ocFirstStr(info, "sessionID", "session_id")
	if sid == "" {
		sid = ocSessionID(props)
	}
	role := strings.ToLower(ocFirstStr(info, "role"))
	model := ocExtractModel(info)

	if msgID != "" {
		m.messages[msgID] = openCodeMessageMeta{
			SessionID: sid,
			Role:      role,
			Model:     model,
		}
	}

	if role == "user" {
		prompt := ocFirstStr(info, "prompt", "text", "content")
		if prompt == "" {
			if summary := ocMapAt(info, "summary"); summary != nil {
				prompt = ocFirstStr(summary, "title", "body")
			}
		}
		if _, isChild := m.children[sid]; isChild {
			return nil
		}
		ev := base
		ev.Event = "prompt"
		ev.State = "working"
		ev.SessionID = m.setRootSession(sid)
		ev.Prompt = prompt
		ev.Model = model
		if prompt != "" {
			ev.Text = "prompt: " + ocOneLine(prompt, openCodeMaxTextLen)
		} else {
			ev.Text = "working"
		}
		return []agent.DaemonEvent{ev}
	}

	// Assistant (or unspecified) message update.
	usage := ocExtractUsage(info)
	reply := ocFirstStr(info, "text", "content", "reply")
	errText := ocExtractError(ocFirstVal(info, "error"))

	if child, isChild := m.children[sid]; isChild {
		if errText != "" {
			return m.mapSessionError(base, sid, errText)
		}
		if usage != nil || model != "" {
			ev := base
			ev.Event = "response"
			ev.SessionID = m.rootOrParent(child)
			ev.Model = model
			ev.Usage = usage
			return []agent.DaemonEvent{ev}
		}
		return nil
	}

	rootID := m.setRootSession(sid)
	if errText != "" {
		ev := base
		ev.Event = "stop_failure"
		ev.State = "failed"
		ev.SessionID = rootID
		ev.Model = model
		ev.Reply = reply
		ev.Usage = usage
		ev.Text = "error: " + ocOneLine(errText, openCodeMaxTextLen)
		return []agent.DaemonEvent{ev}
	}

	if usage == nil && model == "" && reply == "" {
		return nil
	}

	ev := base
	ev.Event = "response"
	ev.SessionID = rootID
	ev.Model = model
	ev.Reply = reply
	ev.Usage = usage
	// If the message has a completion timestamp or finish reason, treat as
	// metadata/usage enrichment without overriding a done state back to working.
	completed := false
	if timeObj := ocMapAt(info, "time"); timeObj != nil {
		if ocInt64(ocFirstVal(timeObj, "completed")) > 0 {
			completed = true
		}
	}
	if ocFirstStr(info, "finish") != "" {
		completed = true
	}
	if !completed {
		ev.State = "working"
	}
	if reply != "" {
		ev.Text = ocOneLine(reply, openCodeMaxTextLen)
	}
	return []agent.DaemonEvent{ev}
}

func (m *OpenCodeEventMapper) mapMessagePartUpdated(base agent.DaemonEvent, props map[string]any) []agent.DaemonEvent {
	part := ocMapAt(props, "part")
	if part == nil {
		part = props
	}
	partType := strings.ToLower(ocFirstStr(part, "type"))
	msgID := ocFirstStr(part, "messageID", "message_id")
	sid := ocFirstStr(part, "sessionID", "session_id")
	if sid == "" {
		sid = ocSessionID(props)
	}
	meta := m.messages[msgID]
	if sid == "" {
		sid = meta.SessionID
	}

	switch partType {
	case "tool":
		tool := ocFirstStr(part, "tool", "name")
		stateObj := ocMapAt(part, "state")
		status := strings.ToLower(ocFirstStr(stateObj, "status"))
		if status == "" {
			status = strings.ToLower(ocFirstStr(part, "status"))
		}
		inputVal := ocFirstVal(stateObj, "input", "args", "arguments")
		if inputVal == nil {
			inputVal = ocFirstVal(part, "input", "args")
		}
		outputVal := ocFirstVal(stateObj, "output", "result")
		errText := ocExtractError(ocFirstVal(stateObj, "error"))
		title := ocFirstStr(stateObj, "title")

		toolInput := ocFormatPayload(inputVal, openCodeMaxDetailLen)
		toolOutput := ocFormatPayload(outputVal, openCodeMaxDetailLen)
		preview := title
		if preview == "" {
			preview = ocToolPreview(tool, inputVal)
		}

		if child, isChild := m.children[sid]; isChild {
			ev := base
			ev.Event = "subagent_start"
			ev.SessionID = m.rootOrParent(child)
			ev.Tool = tool
			ev.ToolInput = toolInput
			ev.ToolOutput = toolOutput
			ev.Text = ocOneLine(preview, openCodeMaxTextLen)
			ev.Child = &agent.DaemonChildEvent{
				ID:      child.ID,
				Type:    child.Type,
				State:   "working",
				Text:    ocOneLine(preview, openCodeMaxTextLen),
				SinceMs: child.SinceMs,
				TsMs:    base.TsMs,
			}
			return []agent.DaemonEvent{ev}
		}

		ev := base
		ev.SessionID = m.setRootSession(sid)
		ev.Tool = tool
		ev.ToolInput = toolInput
		ev.ToolOutput = toolOutput
		switch status {
		case "completed", "done":
			ev.Event = "post_tool"
			ev.State = "working"
			ev.Text = ocOneLine(preview, openCodeMaxTextLen)
		case "error", "failed":
			ev.Event = "tool_failure"
			ev.State = "working"
			if errText != "" {
				ev.Text = "tool failed: " + ocOneLine(errText, openCodeMaxTextLen)
			} else {
				ev.Text = "tool failed: " + ocOneLine(preview, openCodeMaxTextLen)
			}
		default: // "pending", "running", ""
			ev.Event = "pre_tool"
			ev.Text = ocOneLine(preview, openCodeMaxTextLen)
			if ocToolIsAsk(tool) {
				ev.State = "blocked"
				ev.Kind = "question"
			} else {
				ev.State = "working"
			}
		}
		return []agent.DaemonEvent{ev}

	case "text":
		text := ocFirstStr(part, "text")
		if text == "" {
			text = ocFirstStr(props, "delta")
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if _, isChild := m.children[sid]; isChild {
			return nil
		}
		role := meta.Role
		if role == "" {
			role = strings.ToLower(ocFirstStr(part, "role"))
		}
		ev := base
		ev.SessionID = m.setRootSession(sid)
		if role == "user" {
			ev.Event = "prompt"
			ev.State = "working"
			ev.Prompt = text
			ev.Text = "prompt: " + ocOneLine(text, openCodeMaxTextLen)
			return []agent.DaemonEvent{ev}
		}
		ev.Event = "response"
		ev.State = "working"
		ev.Reply = text
		ev.Model = meta.Model
		ev.Text = ocOneLine(text, openCodeMaxTextLen)
		return []agent.DaemonEvent{ev}

	case "subtask":
		subID := ocFirstStr(part, "id", "callID", "sessionID", "session_id")
		if subID == "" {
			subID = "subtask-" + strconv.FormatInt(base.TsMs, 10)
		}
		desc := ocFirstStr(part, "description", "prompt", "agent", "title")
		if desc == "" {
			desc = subID
		}
		status := strings.ToLower(ocFirstStr(part, "status"))

		child, exists := m.children[subID]
		if !exists {
			child = &openCodeChildState{
				ID:       subID,
				ParentID: sid,
				Title:    desc,
				Type:     "subagent",
				State:    "working",
				SinceMs:  base.TsMs,
			}
			m.children[subID] = child
		} else if desc != "" {
			child.Title = desc
		}
		if sid != "" {
			m.setRootSession(sid)
		}

		ev := base
		ev.SessionID = m.rootOrParent(child)
		switch status {
		case "completed", "done":
			child.State = "done"
			ev.Event = "subagent_stop"
			ev.Text = "subagent done: " + ocOneLine(desc, openCodeMaxTextLen)
			ev.Child = &agent.DaemonChildEvent{
				ID:      child.ID,
				Type:    child.Type,
				State:   "done",
				Outcome: "done",
				Text:    desc,
				SinceMs: child.SinceMs,
				TsMs:    base.TsMs,
				EndedMs: base.TsMs,
			}
		case "error", "failed":
			child.State = "failed"
			ev.Event = "subagent_stop"
			ev.Text = "subagent failed: " + ocOneLine(desc, openCodeMaxTextLen)
			ev.Child = &agent.DaemonChildEvent{
				ID:      child.ID,
				Type:    child.Type,
				State:   "failed",
				Outcome: "failed",
				Text:    desc,
				SinceMs: child.SinceMs,
				TsMs:    base.TsMs,
				EndedMs: base.TsMs,
			}
		default:
			child.State = "working"
			ev.Event = "subagent_start"
			ev.State = "working"
			ev.Text = "subagent: " + ocOneLine(desc, openCodeMaxTextLen)
			ev.Child = &agent.DaemonChildEvent{
				ID:      child.ID,
				Type:    child.Type,
				State:   "working",
				Text:    desc,
				SinceMs: child.SinceMs,
				TsMs:    base.TsMs,
			}
		}
		return []agent.DaemonEvent{ev}
	}

	return nil
}

// --- OpenCode JSON extraction helpers ---

func ocMapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	sub, _ := v.(map[string]any)
	return sub
}

func ocFirstVal(m map[string]any, keys ...string) any {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func ocFirstStr(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func ocSessionID(props map[string]any) string {
	if sid := ocFirstStr(props, "sessionID", "session_id"); sid != "" {
		return sid
	}
	if info := ocMapAt(props, "info"); info != nil {
		if sid := ocFirstStr(info, "sessionID", "session_id", "id"); sid != "" {
			return sid
		}
	}
	return ""
}

func ocInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func ocFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n
		}
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
	}
	return 0
}

func ocExtractTimestampMs(evObj, props map[string]any) int64 {
	for _, m := range []map[string]any{evObj, props, ocMapAt(props, "info")} {
		if m == nil {
			continue
		}
		if ts := ocInt64(ocFirstVal(m, "ts_ms", "timestamp_ms", "timestamp")); ts > 0 {
			if ts < 1_000_000_000_000 {
				return ts * 1000
			}
			return ts
		}
		if timeObj := ocMapAt(m, "time"); timeObj != nil {
			if ts := ocInt64(ocFirstVal(timeObj, "updated", "completed", "created", "start")); ts > 0 {
				if ts < 1_000_000_000_000 {
					return ts * 1000
				}
				return ts
			}
		}
	}
	return 0
}

func ocExtractModel(info map[string]any) string {
	if info == nil {
		return ""
	}
	providerID := ocFirstStr(info, "providerID", "provider_id")
	modelID := ocFirstStr(info, "modelID", "model_id")
	if modelID == "" {
		if modelObj := ocMapAt(info, "model"); modelObj != nil {
			if providerID == "" {
				providerID = ocFirstStr(modelObj, "providerID", "provider_id")
			}
			modelID = ocFirstStr(modelObj, "modelID", "model_id", "id", "name")
		} else {
			modelID = ocFirstStr(info, "model")
		}
	}
	if modelID == "" {
		return ""
	}
	if providerID != "" && !strings.HasPrefix(modelID, providerID+"/") {
		return providerID + "/" + modelID
	}
	return modelID
}

func ocExtractUsage(info map[string]any) *agent.DaemonUsage {
	if info == nil {
		return nil
	}
	var u agent.DaemonUsage
	if tokens := ocMapAt(info, "tokens"); tokens != nil {
		u.InputTokens = ocInt64(ocFirstVal(tokens, "input", "input_tokens"))
		u.OutputTokens = ocInt64(ocFirstVal(tokens, "output", "output_tokens"))
		u.ReasoningTokens = ocInt64(ocFirstVal(tokens, "reasoning", "reasoning_tokens"))
		if cache := ocMapAt(tokens, "cache"); cache != nil {
			u.CacheReadTokens = ocInt64(ocFirstVal(cache, "read", "cache_read"))
			u.CacheWriteTokens = ocInt64(ocFirstVal(cache, "write", "cache_write"))
		} else {
			u.CacheReadTokens = ocInt64(ocFirstVal(tokens, "cache_read", "cacheRead"))
			u.CacheWriteTokens = ocInt64(ocFirstVal(tokens, "cache_write", "cacheWrite"))
		}
	}
	u.CostUSD = ocFloat64(ocFirstVal(info, "cost", "cost_usd", "costUSD"))
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.ReasoningTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 && u.CostUSD == 0 {
		return nil
	}
	return &u
}

func ocExtractError(v any) string {
	switch e := v.(type) {
	case string:
		return strings.TrimSpace(e)
	case map[string]any:
		if data := ocMapAt(e, "data"); data != nil {
			if msg := ocFirstStr(data, "message", "error", "detail"); msg != "" {
				return msg
			}
		}
		if msg := ocFirstStr(e, "message", "error", "detail", "reason"); msg != "" {
			return msg
		}
		if name := ocFirstStr(e, "name", "type", "code"); name != "" {
			return name
		}
	}
	return ""
}

func ocToolIsAsk(tool string) bool {
	low := strings.ToLower(strings.TrimSpace(tool))
	switch low {
	case "question", "ask_user", "ask_user_question", "askuserquestion", "request_user_input", "ask":
		return true
	}
	return strings.Contains(low, "ask_user") || strings.Contains(low, "askuser")
}

func ocToolPreview(tool string, input any) string {
	tool = strings.TrimSpace(tool)
	var detail string
	switch v := input.(type) {
	case string:
		detail = strings.TrimSpace(v)
	case map[string]any:
		detail = ocFirstStr(v, "command", "cmd", "filePath", "file_path", "path", "pattern", "query", "url", "description", "question")
	}
	detail = ocOneLine(detail, openCodeMaxTextLen)
	if tool != "" && detail != "" {
		return tool + ": " + detail
	}
	if tool != "" {
		return tool
	}
	return detail
}

func ocFormatPayload(v any, maxLen int) string {
	if v == nil {
		return ""
	}
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		s = string(b)
	}
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

func ocOneLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	fields := strings.Fields(s)
	out := strings.Join(fields, " ")
	if maxLen > 0 && len(out) > maxLen {
		return out[:maxLen]
	}
	return out
}
