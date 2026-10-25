// Wire representations are owned by the public API client. Keep JSON fields
// compatible with the versioned backend; no server implementation is imported.
package client

import "time"

const ProtocolVersion = "1"
const CodexVersion = "0.164.0"
const Workspace = "/workspace/project"

type Profile struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Modes    []string `json:"modes"`
	ReadOnly bool     `json:"read_only"`
}

type Capabilities struct {
	Version          string    `json:"version"`
	MaxPromptBytes   int       `json:"max_prompt_bytes"`
	MaxHistoryBytes  int       `json:"max_history_bytes"`
	MaxSnapshotBytes int       `json:"max_snapshot_bytes"`
	Profiles         []Profile `json:"profiles"`
	CommandKinds     []string  `json:"command_kinds"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

type Room struct {
	ID           string    `json:"id"`
	TeamID       string    `json:"team_id"`
	Name         string    `json:"name"`
	OwnerID      string    `json:"owner_id"`
	Provider     string    `json:"provider"`
	DesiredState string    `json:"desired_state"`
	State        string    `json:"state"`
	Generation   int64     `json:"generation"`
	ThreadID     string    `json:"thread_id,omitempty"`
	PodName      string    `json:"pod_name,omitempty"`
	PodUID       string    `json:"pod_uid,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateRequest struct {
	Name           string `json:"name"`
	IdempotencyKey string `json:"idempotency_key"`
}

type Member struct {
	IdentityID string `json:"identity_id"`
	Owner      bool   `json:"owner"`
}

type AttachInfo struct {
	RoomID          string    `json:"room_id"`
	ThreadID        string    `json:"thread_id"`
	Workspace       string    `json:"workspace"`
	CodexVersion    string    `json:"codex_version"`
	SSHAddress      string    `json:"ssh_address"`
	SSHHostKey      string    `json:"ssh_host_key"`
	Ticket          string    `json:"ticket"`
	TicketExpiresAt time.Time `json:"ticket_expires_at"`
	WebSocketToken  string    `json:"websocket_token"`
}

type PublishRequest struct {
	SourceKind string `json:"source_kind"`
	RoomID     string `json:"room_id,omitempty"`
	SessionID  string `json:"session_id"`
	ThreadID   string `json:"thread_id"`
	TurnID     string `json:"turn_id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	ParseMode  string `json:"parse_mode"`
}

type PublishResponse struct {
	Status    string `json:"status"`
	MessageID int64  `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
}
