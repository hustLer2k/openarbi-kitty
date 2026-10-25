package client

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)
var roomIdentifier = regexp.MustCompile(`^[a-f0-9]{32}$`)

func Invalid(message string) error { return &APIError{Code: "invalid_request", Message: message} }
func Validate(r PublishRequest) error {
	if r.SourceKind != "room" && r.SourceKind != "local" {
		return Invalid("Unsupported publication source")
	}
	if !identifier.MatchString(r.SessionID) || !identifier.MatchString(r.ThreadID) || !identifier.MatchString(r.TurnID) {
		return Invalid("Session, thread and turn IDs are required and must fit supported bounds")
	}
	if r.SourceKind == "room" && !roomIdentifier.MatchString(r.RoomID) || r.SourceKind == "local" && r.RoomID != "" {
		return Invalid("Invalid publication room")
	}
	switch r.Kind {
	case "progress", "final", "stop":
	default:
		return Invalid("Unsupported publication kind")
	}
	if r.ParseMode != "HTML" && r.ParseMode != "" {
		return Invalid("Unsupported publication parse mode")
	}
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || len(r.Text) > 16000 || utf8.RuneCountInString(r.Text) > 4096 || strings.ContainsRune(r.Text, 0) {
		return Invalid("Publication text must contain 1–4096 characters")
	}
	return nil
}
