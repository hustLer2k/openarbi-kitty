// Package desktop owns the durable desktop workspace and the local broker.
// The Manager serializes every mutation; returned snapshots never alias its state.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

const StateVersion = 1

type Geometry struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
type Tab struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Binding    string `json:"binding,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RequestKey string `json:"request_key,omitempty"`
	RoomID     string `json:"room_id,omitempty"`
}
type Window struct {
	ID          string   `json:"id"`
	Geometry    Geometry `json:"geometry"`
	SelectedTab string   `json:"selected_tab"`
	Tabs        []Tab    `json:"tabs"`
}
type Workspace struct {
	Version  int      `json:"version"`
	DeviceID string   `json:"device_id"`
	Origin   string   `json:"origin"`
	Windows  []Window `json:"windows"`
	Closed   []Tab    `json:"closed"`
}
type Command struct {
	Action   string    `json:"action"`
	WindowID string    `json:"window_id,omitempty"`
	TabID    string    `json:"tab_id,omitempty"`
	RoomID   string    `json:"room_id,omitempty"`
	Geometry *Geometry `json:"geometry,omitempty"`
	Order    []string  `json:"order,omitempty"`
}
type Result struct {
	Workspace Workspace     `json:"workspace"`
	Windows   []string      `json:"windows,omitempty"`
	TabID     string        `json:"tab_id,omitempty"`
	Restored  bool          `json:"restored,omitempty"`
	Status    string        `json:"status,omitempty"`
	RoomID    string        `json:"room_id,omitempty"`
	Error     string        `json:"error,omitempty"`
	Rooms     []client.Room `json:"rooms,omitempty"`
	Room      *client.Room  `json:"room,omitempty"`
}
type Persistence interface {
	Load() (Workspace, error)
	Save(Workspace) error
}
type Allocation interface {
	DesktopCapabilities(context.Context) (client.DesktopAllocation, error)
	ClaimRoom(context.Context, client.ClaimRequest) (client.RoomBinding, error)
	RoomBinding(context.Context, string, string) (client.RoomBinding, error)
	Rooms(context.Context) ([]client.Room, error)
	Room(context.Context, string) (client.Room, error)
	StopRoom(context.Context, string) (client.Room, error)
	ResumeRoom(context.Context, string) (client.Room, error)
}

// Manager owns both durable state and the live-window set. No state is changed
// in memory until its replacement is saved. Network preparation uses a snapshot
// outside the lock and applies its result only while the same tab still exists.
type Manager struct {
	mu         sync.Mutex
	state      Workspace
	store      Persistence
	api        Allocation
	live       map[string]bool
	stopping   bool
	operations int
}

func NewManager(store Persistence, api Allocation, origin string) (*Manager, error) {
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	if state.Version == 0 {
		state = Workspace{Version: StateVersion, DeviceID: uuid.NewString(), Origin: origin}
		if err = store.Save(state); err != nil {
			return nil, err
		}
	}
	if err = validate(state); err != nil {
		return nil, err
	}
	if state.Origin != origin {
		return nil, errors.New("saved workspace belongs to a different API origin; use its original server")
	}
	return &Manager{state: state, store: store, api: api, live: make(map[string]bool)}, nil
}
func clone(s Workspace) Workspace {
	s.Windows = slices.Clone(s.Windows)
	for i := range s.Windows {
		s.Windows[i].Tabs = slices.Clone(s.Windows[i].Tabs)
	}
	s.Closed = slices.Clone(s.Closed)
	return s
}
func (m *Manager) Snapshot() Workspace { m.mu.Lock(); defer m.mu.Unlock(); return clone(m.state) }
func newTab(kind string) Tab {
	t := Tab{ID: uuid.NewString(), Kind: kind}
	if kind == "room" {
		t.SessionID = uuid.NewString()
		t.RequestKey = uuid.NewString()
	}
	return t
}
func newWindow() Window {
	t := newTab("room")
	return Window{ID: uuid.NewString(), Geometry: Geometry{Width: 1100, Height: 760}, SelectedTab: t.ID, Tabs: []Tab{t}}
}
func (m *Manager) Apply(ctx context.Context, c Command) (Result, error) {
	switch c.Action {
	case "prepare":
		return m.prepare(ctx, c.TabID)
	case "rooms", "open-room", "stop-room", "resume-room":
		return m.manageRoom(ctx, c)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := clone(m.state)
	if m.stopping && c.Action != "status" {
		return Result{Workspace: s}, errors.New("desktop broker is stopping; reopen OpenArbi after updating")
	}
	result := Result{}
	live := make(map[string]bool, len(m.live))
	for id, on := range m.live {
		live[id] = on
	}
	wi := slices.IndexFunc(s.Windows, func(w Window) bool { return w.ID == c.WindowID })
	window := func() (*Window, error) {
		if wi < 0 {
			return nil, errors.New("window is not in this workspace")
		}
		return &s.Windows[wi], nil
	}
	switch c.Action {
	case "status":
		return Result{Workspace: s}, nil
	case "sync-windows":
		clear(live)
		for _, id := range c.Order {
			if live[id] || !slices.ContainsFunc(s.Windows, func(w Window) bool { return w.ID == id }) {
				return result, errors.New("live windows contain an unknown or repeated window")
			}
			live[id] = true
		}
		m.live = live
		return Result{Workspace: s}, nil
	case "launch":
		if len(live) == 0 && len(s.Windows) > 0 {
			result.Restored = true
			for _, w := range s.Windows {
				live[w.ID] = true
				result.Windows = append(result.Windows, w.ID)
			}
			break
		}
		fallthrough
	case "new-window":
		w := newWindow()
		s.Windows = append(s.Windows, w)
		live[w.ID] = true
		result.Windows = []string{w.ID}
		result.TabID = w.SelectedTab
	case "new-tab", "local-tab":
		w, err := window()
		if err != nil {
			return result, err
		}
		kind := "room"
		if c.Action == "local-tab" {
			kind = "local"
		}
		t := newTab(kind)
		w.Tabs = append(w.Tabs, t)
		w.SelectedTab = t.ID
		result.TabID = t.ID
	case "close-tab":
		w, err := window()
		if err != nil {
			return result, err
		}
		ti := slices.IndexFunc(w.Tabs, func(t Tab) bool { return t.ID == c.TabID })
		if ti < 0 {
			return result, errors.New("tab is not in this window")
		}
		s.Closed = append(s.Closed, w.Tabs[ti])
		if len(s.Closed) > 64 {
			s.Closed = s.Closed[len(s.Closed)-64:]
		}
		w.Tabs = slices.Delete(w.Tabs, ti, ti+1)
		if len(w.Tabs) == 0 {
			s.Windows = slices.Delete(s.Windows, wi, wi+1)
			delete(live, c.WindowID)
		} else if w.SelectedTab == c.TabID {
			w.SelectedTab = w.Tabs[min(ti, len(w.Tabs)-1)].ID
		}
	case "close-window":
		if _, err := window(); err != nil {
			return result, err
		}
		// Close callbacks after an application Quit must not erase the saved layout.
		if !live[c.WindowID] {
			return Result{Workspace: s}, nil
		}
		if len(live) > 1 {
			for _, t := range s.Windows[wi].Tabs {
				s.Closed = append(s.Closed, t)
			}
			if len(s.Closed) > 64 {
				s.Closed = s.Closed[len(s.Closed)-64:]
			}
			s.Windows = slices.Delete(s.Windows, wi, wi+1)
		}
		delete(live, c.WindowID)
	case "quit":
		clear(live)
	case "reopen-tab":
		w, err := window()
		if err != nil {
			return result, err
		}
		if len(s.Closed) == 0 {
			return result, errors.New("no closed tab to reopen")
		}
		t := s.Closed[len(s.Closed)-1]
		s.Closed = s.Closed[:len(s.Closed)-1]
		w.Tabs = append(w.Tabs, t)
		w.SelectedTab = t.ID
		result.TabID = t.ID
	case "select-tab":
		w, err := window()
		if err != nil {
			return result, err
		}
		if !slices.ContainsFunc(w.Tabs, func(t Tab) bool { return t.ID == c.TabID }) {
			return result, errors.New("tab is not in this window")
		}
		w.SelectedTab = c.TabID
	case "geometry":
		w, err := window()
		if err != nil {
			return result, err
		}
		if c.Geometry == nil || c.Geometry.Width < 1 || c.Geometry.Height < 1 {
			return result, errors.New("positive window geometry is required")
		}
		w.Geometry = *c.Geometry
	case "reorder-tabs":
		w, err := window()
		if err != nil {
			return result, err
		}
		if len(c.Order) != len(w.Tabs) {
			return result, errors.New("tab order must contain every tab exactly once")
		}
		byID := make(map[string]Tab, len(w.Tabs))
		for _, t := range w.Tabs {
			byID[t.ID] = t
		}
		ordered := make([]Tab, 0, len(w.Tabs))
		for _, id := range c.Order {
			t, ok := byID[id]
			if !ok {
				return result, errors.New("tab order contains an unknown or repeated tab")
			}
			ordered = append(ordered, t)
			delete(byID, id)
		}
		w.Tabs = ordered
	case "reorder-windows":
		if len(c.Order) != len(s.Windows) {
			return result, errors.New("window order must contain every window exactly once")
		}
		byID := make(map[string]Window, len(s.Windows))
		for _, w := range s.Windows {
			byID[w.ID] = w
		}
		ordered := make([]Window, 0, len(s.Windows))
		for _, id := range c.Order {
			w, ok := byID[id]
			if !ok {
				return result, errors.New("window order contains an unknown or repeated window")
			}
			ordered = append(ordered, w)
			delete(byID, id)
		}
		s.Windows = ordered
	default:
		return result, fmt.Errorf("unknown desktop action %q", c.Action)
	}
	if err := validate(s); err != nil {
		return result, err
	}
	if err := m.store.Save(s); err != nil {
		return result, err
	}
	m.state = s
	m.live = live
	result.Workspace = clone(s)
	return result, nil
}
func findTab(s *Workspace, id string) *Tab {
	for i := range s.Windows {
		for j := range s.Windows[i].Tabs {
			if s.Windows[i].Tabs[j].ID == id {
				return &s.Windows[i].Tabs[j]
			}
		}
	}
	return nil
}
func (m *Manager) prepare(ctx context.Context, id string) (Result, error) {
	// The intent was saved by launch/new-tab before it became visible. Work on an
	// immutable copy while the UI continues to close, move and select windows.
	snapshot, err := m.startOperation()
	if err != nil {
		return Result{}, err
	}
	defer m.finishOperation()
	result := Result{Workspace: snapshot, TabID: id}
	tab := findTab(&snapshot, id)
	if tab == nil {
		return result, errors.New("tab is not in this workspace")
	}
	if tab.Kind == "local" {
		result.Status = "local"
		return result, nil
	}
	if m.api == nil {
		return result, errors.New("desktop allocation is not configured")
	}
	if _, err := m.api.DesktopCapabilities(ctx); err != nil {
		return result, err
	}
	var binding client.RoomBinding
	if tab.Binding == "existing" {
		room, lookupErr := m.api.Room(ctx, tab.RoomID)
		err = lookupErr
		binding = client.RoomBinding{DeviceID: snapshot.DeviceID, SessionID: tab.SessionID, Room: room, Ready: room.State == "ready" && room.DesiredState == "running"}
	} else if tab.RoomID == "" {
		binding, err = m.api.ClaimRoom(ctx, client.ClaimRequest{DeviceID: snapshot.DeviceID, SessionID: tab.SessionID, IdempotencyKey: tab.RequestKey})
	} else {
		binding, err = m.api.RoomBinding(ctx, snapshot.DeviceID, tab.SessionID)
	}
	if err != nil {
		return result, err
	}
	if binding.DeviceID != snapshot.DeviceID || binding.SessionID != tab.SessionID || binding.Room.ID == "" || (tab.RoomID != "" && tab.RoomID != binding.Room.ID) {
		return result, errors.New("server returned a mismatched room binding; restoration stopped")
	}
	autoWoke := false
	if binding.Room.State == "stopped" && binding.Room.DesiredState == "stopped" && binding.Room.Reason == "idle_suspended" {
		// A delayed lookup may finish after the UI closed this tab. Do not wake a
		// room that no longer has a requesting tab. Resume itself uses server fencing.
		m.mu.Lock()
		current := findTab(&m.state, id)
		stillOpen := current != nil && *current == *tab && !m.stopping
		m.mu.Unlock()
		if !stillOpen {
			return result, errors.New("tab closed or changed before its workspace could wake")
		}
		room, resumeErr := m.api.ResumeRoom(ctx, binding.Room.ID)
		if resumeErr != nil {
			return result, resumeErr
		}
		if room.ID != binding.Room.ID {
			return result, errors.New("server resumed a different room; attachment stopped")
		}
		binding.Room = room
		binding.Ready = room.State == "ready" && room.DesiredState == "running"
		autoWoke = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := clone(m.state)
	current := findTab(&s, id)
	if current == nil {
		return Result{Workspace: s, TabID: id}, errors.New("tab closed while its room was preparing; the room remains available in the picker")
	}
	if current.Binding != tab.Binding || current.SessionID != tab.SessionID || current.RequestKey != tab.RequestKey || (current.RoomID != "" && current.RoomID != binding.Room.ID) {
		return Result{Workspace: s, TabID: id}, errors.New("tab binding changed while preparing; attachment stopped")
	}
	current.RoomID = binding.Room.ID
	// Persist the acknowledgement before exposing it to the native terminal.
	if err = m.store.Save(s); err != nil {
		return result, err
	}
	m.state = s
	result.Workspace = clone(s)
	result.RoomID = binding.Room.ID
	result.Room = &binding.Room
	result.Status, err = roomStatus(binding.Room, binding.Ready)
	if err != nil {
		return result, err
	}
	if autoWoke && result.Status == "preparing" {
		result.Status = "waking"
	}

	return result, nil
}
func validate(s Workspace) error {
	if s.Version != StateVersion {
		return errors.New("unsupported workspace version; update OpenArbi")
	}
	if _, err := uuid.Parse(s.DeviceID); err != nil {
		return errors.New("invalid workspace device identity")
	}
	if _, err := client.NormalizeOrigin(s.Origin); err != nil {
		return errors.New("invalid workspace API origin")
	}
	seen := map[string]bool{}
	sessions := map[string]bool{}
	checkTab := func(t Tab) error {
		if _, err := uuid.Parse(t.ID); err != nil || seen[t.ID] {
			return errors.New("invalid or repeated tab identity")
		}
		seen[t.ID] = true
		switch t.Kind {
		case "room":
			switch t.Binding {
			case "existing":
				if t.RoomID == "" || t.SessionID != "" || t.RequestKey != "" {
					return errors.New("invalid existing-room binding")
				}
				return nil
			case "": // Existing allocation snapshots predate the explicit binding field.
			default:
				return errors.New("unknown room binding type")
			}
			for _, id := range []string{t.SessionID, t.RequestKey} {
				if _, err := uuid.Parse(id); err != nil {
					return errors.New("invalid allocation intent")
				}
			}
			if sessions[t.SessionID] {
				return errors.New("repeated session identity")
			}
			sessions[t.SessionID] = true
		case "local":
			if t.Binding != "" || t.SessionID != "" || t.RequestKey != "" || t.RoomID != "" {
				return errors.New("local tab contains remote identity")
			}
		default:
			return errors.New("unknown tab kind")
		}
		return nil
	}
	for _, w := range s.Windows {
		if _, err := uuid.Parse(w.ID); err != nil || seen[w.ID] {
			return errors.New("invalid or repeated window identity")
		}
		seen[w.ID] = true
		if len(w.Tabs) == 0 || w.Geometry.Width < 1 || w.Geometry.Height < 1 {
			return errors.New("invalid window layout")
		}
		if !slices.ContainsFunc(w.Tabs, func(t Tab) bool { return t.ID == w.SelectedTab }) {
			return errors.New("selected tab is absent")
		}
		for _, t := range w.Tabs {
			if err := checkTab(t); err != nil {
				return err
			}
		}
	}
	for _, t := range s.Closed {
		if err := checkTab(t); err != nil {
			return err
		}
	}
	return nil
}
