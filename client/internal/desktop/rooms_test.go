package desktop

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

type managedRooms struct {
	*fakeAllocation
	available   map[string]client.Room
	directReads int
	stops       int
	resumes     int
	resumeErr   error
}

func (f *managedRooms) Rooms(context.Context) ([]client.Room, error) {
	out := make([]client.Room, 0, len(f.available))
	for _, room := range f.available {
		out = append(out, room)
	}
	return out, nil
}
func (f *managedRooms) Room(_ context.Context, id string) (client.Room, error) {
	f.directReads++
	room, ok := f.available[id]
	if !ok {
		return client.Room{}, errors.New("room unavailable")
	}
	return room, nil
}
func (f *managedRooms) StopRoom(_ context.Context, id string) (client.Room, error) {
	f.stops++
	room, ok := f.available[id]
	if !ok {
		return client.Room{}, errors.New("room unavailable")
	}
	room.State = "stopping"
	room.DesiredState = "stopped"
	room.Reason = ""
	f.available[id] = room
	return room, nil
}
func (f *managedRooms) ResumeRoom(_ context.Context, id string) (client.Room, error) {
	f.resumes++
	if f.resumeErr != nil {
		return client.Room{}, f.resumeErr
	}
	room, ok := f.available[id]
	if !ok {
		return client.Room{}, errors.New("room unavailable")
	}
	room.State = "pending"
	room.DesiredState = "running"
	room.Reason = ""
	f.available[id] = room
	if f.binding.Room.ID == id {
		f.binding.Room = room
		f.binding.Ready = false
	}
	return room, nil
}
func managedSetup(t *testing.T) (*Manager, *memoryStore, *managedRooms) {
	t.Helper()
	store := &memoryStore{}
	api := &managedRooms{fakeAllocation: &fakeAllocation{store: store}, available: map[string]client.Room{"existing-room": {ID: "existing-room", Name: "Retained workspace", State: "ready", DesiredState: "running"}}}
	m, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return m, store, api
}
func TestPickerPersistsExactRoomWithoutClaimAndRestoresByAuthenticatedLookup(t *testing.T) {
	m, store, api := managedSetup(t)
	first := apply(t, m, Command{Action: "launch"})
	listed := apply(t, m, Command{Action: "rooms"})
	if len(listed.Rooms) != 1 || listed.Rooms[0].ID != "existing-room" {
		t.Fatal("picker rooms missing")
	}
	opened := apply(t, m, Command{Action: "open-room", WindowID: first.Windows[0], RoomID: "existing-room"})
	tab := opened.Workspace.Windows[0].Tabs[1]
	if tab.ID != opened.TabID || tab.Kind != "room" || tab.Binding != "existing" || tab.RoomID != "existing-room" || tab.SessionID != "" || tab.RequestKey != "" || len(api.claims) != 0 {
		t.Fatalf("selected tab not a direct room reference: %+v", tab)
	}
	restarted, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	ready := apply(t, restarted, Command{Action: "prepare", TabID: tab.ID})
	if ready.Status != "ready" || ready.RoomID != "existing-room" || api.directReads != 2 || api.lookups != 0 || len(api.claims) != 0 {
		t.Fatal("existing room restoration used allocation")
	}
	delete(api.available, "existing-room")
	before := restarted.Snapshot()
	if _, err = restarted.Apply(t.Context(), Command{Action: "prepare", TabID: tab.ID}); err == nil {
		t.Fatal("revoked room accepted")
	}
	if len(api.claims) != 0 || !reflect.DeepEqual(before, restarted.Snapshot()) {
		t.Fatal("revoked existing-room tab was replaced")
	}
}
func TestPickerRejectsUnavailableRoomBeforeChangingLayout(t *testing.T) {
	m, _, api := managedSetup(t)
	first := apply(t, m, Command{Action: "launch"})
	before := m.Snapshot()
	if _, err := m.Apply(t.Context(), Command{Action: "open-room", WindowID: first.Windows[0], RoomID: "missing"}); err == nil {
		t.Fatal("missing room selected")
	}
	if len(api.claims) != 0 || !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("missing selection mutated workspace")
	}
}
func TestIdleWakeRequiresExactStoppedReason(t *testing.T) {
	for _, tc := range []struct {
		name, state, desired, reason, status string
		wakes                                int
	}{
		{"idle", "stopped", "stopped", "idle_suspended", "waking", 1},
		{"explicit stop", "stopped", "stopped", "", "stopped", 0},
		{"other reason", "stopped", "stopped", "user_stopped", "stopped", 0},
		{"still stopping", "stopping", "stopped", "idle_suspended", "stopped", 0},
		{"unexpected loss", "attention", "running", "idle_suspended", "recovery-required", 0},
		{"not deliberately stopped", "stopped", "running", "idle_suspended", "stopped", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, api := managedSetup(t)
			first := apply(t, m, Command{Action: "launch"})
			room := api.available["existing-room"]
			room.State = tc.state
			room.DesiredState = tc.desired
			room.Reason = tc.reason
			api.available[room.ID] = room
			selected := apply(t, m, Command{Action: "open-room", WindowID: first.Windows[0], RoomID: room.ID})
			result := apply(t, m, Command{Action: "prepare", TabID: selected.TabID})
			if result.Status != tc.status || api.resumes != tc.wakes || len(api.claims) != 0 {
				t.Fatalf("status %s; resumes %d", result.Status, api.resumes)
			}
			if tc.wakes > 0 {
				again := apply(t, m, Command{Action: "prepare", TabID: selected.TabID})
				if again.Status != "preparing" || api.resumes != 1 {
					t.Fatal("idle wake repeated after resume accepted")
				}
			}
		})
	}
}
func TestAllocatedRoomIdleWakeKeepsSessionBinding(t *testing.T) {
	m, _, api := managedSetup(t)
	first := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "prepare", TabID: first.TabID})
	room := client.Room{ID: "room-one", State: "stopped", DesiredState: "stopped", Reason: "idle_suspended"}
	api.available[room.ID] = room
	api.binding.Room = room
	api.binding.Ready = false
	before := m.Snapshot().Windows[0].Tabs[0]
	result := apply(t, m, Command{Action: "prepare", TabID: first.TabID})
	if result.Status != "waking" || api.resumes != 1 || len(api.claims) != 1 || api.lookups != 1 || m.Snapshot().Windows[0].Tabs[0] != before {
		t.Fatal("wake changed allocation binding")
	}
}
func TestIdleResumeFailureRemainsExplicitWithoutClaim(t *testing.T) {
	m, _, api := managedSetup(t)
	first := apply(t, m, Command{Action: "launch"})
	api.available["existing-room"] = client.Room{ID: "existing-room", State: "stopped", DesiredState: "stopped", Reason: "idle_suspended"}
	selected := apply(t, m, Command{Action: "open-room", WindowID: first.Windows[0], RoomID: "existing-room"})
	api.resumeErr = &client.UncertainError{Cause: errors.New("lost acknowledgement")}
	if _, err := m.Apply(t.Context(), Command{Action: "prepare", TabID: selected.TabID}); err == nil {
		t.Fatal("uncertain resume ignored")
	}
	if api.resumes != 1 || len(api.claims) != 0 {
		t.Fatal("uncertain resume allocated a replacement")
	}
}
func TestPickerStopAndExplicitRecoveryUseNormalLifecycleAPI(t *testing.T) {
	m, _, api := managedSetup(t)
	stopped := apply(t, m, Command{Action: "stop-room", RoomID: "existing-room"})
	if stopped.Status != "stopped" || api.stops != 1 {
		t.Fatal("stop not dispatched")
	}
	api.available["existing-room"] = client.Room{ID: "existing-room", State: "attention", DesiredState: "running", Reason: "worker_lost"}
	resumed := apply(t, m, Command{Action: "resume-room", RoomID: "existing-room"})
	if resumed.Status != "preparing" || api.resumes != 1 || len(api.claims) != 0 {
		t.Fatal("explicit recovery not dispatched")
	}
}
func TestStorageAdmissionReportsWaitingState(t *testing.T) {
	for _, reason := range []string{"storage_capacity_unavailable", "storage_admission_rejected"} {
		t.Run(reason, func(t *testing.T) {
			m, _, api := managedSetup(t)
			first := apply(t, m, Command{Action: "launch"})
			api.available["existing-room"] = client.Room{ID: "existing-room", State: "pending", DesiredState: "running", Reason: reason}
			selected := apply(t, m, Command{Action: "open-room", WindowID: first.Windows[0], RoomID: "existing-room"})
			result := apply(t, m, Command{Action: "prepare", TabID: selected.TabID})
			if result.Status != "waiting-for-storage" || api.resumes != 0 || len(api.claims) != 0 {
				t.Fatal("storage waiting hidden")
			}
		})
	}
}
func TestShutdownRefusesWindowsAndPendingWorkThenFencesNewWork(t *testing.T) {
	m, _, _ := setup(t)
	apply(t, m, Command{Action: "launch"})
	if err := m.BeginShutdown(); err == nil {
		t.Fatal("live window not protected")
	}
	apply(t, m, Command{Action: "quit"})
	if _, err := m.startOperation(); err != nil {
		t.Fatal(err)
	}
	if err := m.BeginShutdown(); err == nil {
		t.Fatal("in-flight work not protected")
	}
	m.finishOperation()
	saved := m.Snapshot()
	if err := m.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(t.Context(), Command{Action: "launch"}); err == nil {
		t.Fatal("launch accepted during shutdown")
	}
	if _, err := m.Apply(t.Context(), Command{Action: "rooms"}); err == nil {
		t.Fatal("network accepted during shutdown")
	}
	if !reflect.DeepEqual(saved, m.Snapshot()) {
		t.Fatal("shutdown changed restoration state")
	}
}
