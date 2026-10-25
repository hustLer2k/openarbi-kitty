package desktop

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

func (m *Manager) startOperation() (Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return Workspace{}, errors.New("desktop broker is stopping; reopen OpenArbi after updating")
	}
	m.operations++
	return clone(m.state), nil
}
func (m *Manager) finishOperation() { m.mu.Lock(); m.operations--; m.mu.Unlock() }

// BeginShutdown fences new UI and API work before the server closes its listener.
// Existing windows and in-flight network work must finish before an update can
// retire the shared tunnel. No restoration state is removed.
func (m *Manager) BeginShutdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return nil
	}
	if len(m.live) != 0 {
		return errors.New("close OpenArbi windows before updating; the workspace will be restored")
	}
	if m.operations != 0 {
		return errors.New("OpenArbi has an operation in progress; wait for it before updating")
	}
	m.stopping = true
	return nil
}

func (m *Manager) manageRoom(ctx context.Context, c Command) (Result, error) {
	snapshot, err := m.startOperation()
	if err != nil {
		return Result{}, err
	}
	defer m.finishOperation()
	result := Result{Workspace: snapshot}
	if m.api == nil {
		return result, errors.New("room management is not configured")
	}
	if _, err = m.api.DesktopCapabilities(ctx); err != nil {
		return result, err
	}
	if c.Action == "rooms" {
		result.Rooms, err = m.api.Rooms(ctx)
		return result, err
	}
	if c.RoomID == "" {
		return result, errors.New("room ID is required")
	}
	if c.Action == "open-room" && !slices.ContainsFunc(snapshot.Windows, func(w Window) bool { return w.ID == c.WindowID }) {
		return result, errors.New("window is not in this workspace")
	}
	// Resolve through the authenticated endpoint before recording a direct room
	// reference or acting on it. A missing/revoked room never becomes a new claim.
	room, err := m.api.Room(ctx, c.RoomID)
	if err != nil {
		return result, err
	}
	if room.ID != c.RoomID {
		return result, errors.New("server returned a different room; selection stopped")
	}
	switch c.Action {
	case "open-room":
		m.mu.Lock()
		defer m.mu.Unlock()
		s := clone(m.state)
		index := slices.IndexFunc(s.Windows, func(w Window) bool { return w.ID == c.WindowID })
		if index < 0 {
			return Result{Workspace: s}, errors.New("window closed while the selected room was loading")
		}
		tab := Tab{ID: uuid.NewString(), Kind: "room", Binding: "existing", RoomID: room.ID}
		s.Windows[index].Tabs = append(s.Windows[index].Tabs, tab)
		s.Windows[index].SelectedTab = tab.ID
		if err = validate(s); err != nil {
			return result, err
		}
		if err = m.store.Save(s); err != nil {
			return result, err
		}
		m.state = s
		result.Workspace = clone(s)
		result.TabID = tab.ID
		result.Windows = []string{c.WindowID}
	case "stop-room":
		room, err = m.api.StopRoom(ctx, room.ID)
	case "resume-room":
		room, err = m.api.ResumeRoom(ctx, room.ID)
	default:
		return result, errors.New("unknown room management action")
	}
	if err != nil {
		return result, err
	}
	if room.ID != c.RoomID {
		return result, errors.New("server returned a different room after the action")
	}
	result.Room = &room
	result.RoomID = room.ID
	result.Status, err = roomStatus(room, room.State == "ready" && room.DesiredState == "running")
	return result, err
}
func roomStatus(room client.Room, ready bool) (string, error) {
	if ready {
		return "ready", nil
	}
	switch room.State {
	case "pending", "starting":
		switch room.Reason {
		case "storage_capacity_unavailable", "storage_admission_rejected":
			return "waiting-for-storage", nil
		default:
			return "preparing", nil // Reason text is an open diagnostic field.
		}
	case "stopped", "stopping":
		return "stopped", nil
	case "attention":
		return "recovery-required", nil
	case "ready":
		if room.DesiredState == "stopped" {
			return "stopped", nil
		}
		return "", errors.New("server room readiness is inconsistent; update the server and client")
	default:
		return "", errors.New("server returned an unknown room state; update the server and client")
	}
}
