package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

const testOrigin = "http://127.0.0.1:8090"

type memoryStore struct {
	state Workspace
	fail  bool
}

func (s *memoryStore) Load() (Workspace, error) { return clone(s.state), nil }
func (s *memoryStore) Save(w Workspace) error {
	if s.fail {
		return errors.New("disk full")
	}
	s.state = clone(w)
	return nil
}

type fakeAllocation struct {
	unmanagedRooms
	store       *memoryStore
	claims      []client.ClaimRequest
	lookups     int
	binding     client.RoomBinding
	loseReply   bool
	revoked     bool
	unsupported bool
}

func (f *fakeAllocation) DesktopCapabilities(context.Context) (client.DesktopAllocation, error) {
	if f.unsupported {
		return client.DesktopAllocation{}, errors.New("update server")
	}
	return client.DesktopAllocation{Version: 1, MaxActiveRooms: 4}, nil
}
func (f *fakeAllocation) ClaimRoom(_ context.Context, in client.ClaimRequest) (client.RoomBinding, error) {
	found := false
	for _, w := range f.store.state.Windows {
		for _, tab := range w.Tabs {
			if tab.SessionID == in.SessionID && tab.RequestKey == in.IdempotencyKey {
				found = true
			}
		}
	}
	if !found || f.store.state.DeviceID != in.DeviceID {
		return client.RoomBinding{}, errors.New("claim intent was not persisted")
	}
	f.claims = append(f.claims, in)
	if f.binding.Room.ID == "" {
		f.binding = client.RoomBinding{DeviceID: in.DeviceID, SessionID: in.SessionID, Room: client.Room{ID: "room-one"}, Ready: true}
	}
	if f.loseReply {
		f.loseReply = false
		return client.RoomBinding{}, &client.UncertainError{Cause: errors.New("connection lost")}
	}
	return f.binding, nil
}
func (f *fakeAllocation) RoomBinding(_ context.Context, device, session string) (client.RoomBinding, error) {
	f.lookups++
	if f.revoked {
		return client.RoomBinding{}, errors.New("binding unavailable")
	}
	return f.binding, nil
}
func setup(t *testing.T) (*Manager, *memoryStore, *fakeAllocation) {
	t.Helper()
	store := &memoryStore{}
	api := &fakeAllocation{store: store}
	manager, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, api
}
func apply(t *testing.T, m *Manager, c Command) Result {
	t.Helper()
	r, err := m.Apply(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestAllocationCrashReusesDurableIntent(t *testing.T) {
	m, store, api := setup(t)
	launch := apply(t, m, Command{Action: "launch"})
	api.loseReply = true
	if _, err := m.Apply(t.Context(), Command{Action: "prepare", TabID: launch.TabID}); err == nil {
		t.Fatal("expected lost reply")
	}
	if store.state.Windows[0].Tabs[0].RoomID != "" {
		t.Fatal("unacknowledged binding persisted")
	}
	restarted, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	restored := apply(t, restarted, Command{Action: "launch"})
	if !restored.Restored || restored.Workspace.DeviceID != launch.Workspace.DeviceID {
		t.Fatal("device/workspace not restored")
	}
	ready := apply(t, restarted, Command{Action: "prepare", TabID: launch.TabID})
	if ready.Status != "ready" || len(api.claims) != 2 || api.claims[0] != api.claims[1] {
		t.Fatalf("request identity changed: %+v", api.claims)
	}
	restarted, err = NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, restarted, Command{Action: "prepare", TabID: launch.TabID})
	if len(api.claims) != 2 || api.lookups != 1 {
		t.Fatal("restoration created a replacement")
	}
}
func TestMissingBindingNeverCreatesReplacement(t *testing.T) {
	m, _, api := setup(t)
	launch := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "prepare", TabID: launch.TabID})
	api.revoked = true
	before := m.Snapshot()
	if _, err := m.Apply(t.Context(), Command{Action: "prepare", TabID: launch.TabID}); err == nil {
		t.Fatal("missing binding accepted")
	}
	if len(api.claims) != 1 || !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("missing binding changed saved state")
	}
}
func TestCapabilityFailureLeavesIntentWithoutClaim(t *testing.T) {
	m, store, api := setup(t)
	launch := apply(t, m, Command{Action: "launch"})
	api.unsupported = true
	if _, err := m.Apply(t.Context(), Command{Action: "prepare", TabID: launch.TabID}); err == nil {
		t.Fatal("unsupported capability accepted")
	}
	if len(api.claims) != 0 || store.state.Windows[0].Tabs[0].RequestKey == "" {
		t.Fatal("allocation not capability gated")
	}
}
func TestConcurrentLaunchesCreateDistinctWindowsAndRooms(t *testing.T) {
	m, _, _ := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for range 24 {
		wg.Go(func() { _, err := m.Apply(t.Context(), Command{Action: "launch"}); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state := m.Snapshot()
	if len(state.Windows) != 24 {
		t.Fatalf("got %d windows", len(state.Windows))
	}
	if err := validate(state); err != nil {
		t.Fatal(err)
	}
}
func TestQuitPreservesAllWindowsAndIgnoresLateWindowClose(t *testing.T) {
	m, store, api := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	second := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "quit"})
	for _, id := range []string{first.Windows[0], second.Windows[0]} {
		apply(t, m, Command{Action: "close-window", WindowID: id})
	}
	restarted, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	result := apply(t, restarted, Command{Action: "launch"})
	if !result.Restored || len(result.Windows) != 2 {
		t.Fatal("quit lost windows")
	}
}
func TestNonLastWindowCloseRemovesLayoutLastClosePreserves(t *testing.T) {
	m, _, _ := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	second := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "close-window", WindowID: first.Windows[0]})
	last := apply(t, m, Command{Action: "close-window", WindowID: second.Windows[0]})
	if len(last.Workspace.Windows) != 1 || last.Workspace.Windows[0].ID != second.Windows[0] {
		t.Fatal("wrong close semantics")
	}
	restored := apply(t, m, Command{Action: "launch"})
	if !restored.Restored || len(restored.Windows) != 1 {
		t.Fatal("last window not restored")
	}
}
func TestExplicitTabCloseAndReopenPreserveBinding(t *testing.T) {
	m, _, _ := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	ready := apply(t, m, Command{Action: "prepare", TabID: first.TabID})
	tab := ready.Workspace.Windows[0].Tabs[0]
	second := apply(t, m, Command{Action: "new-tab", WindowID: first.Windows[0]})
	closed := apply(t, m, Command{Action: "close-tab", WindowID: first.Windows[0], TabID: first.TabID})
	if len(closed.Workspace.Windows[0].Tabs) != 1 || closed.Workspace.Windows[0].SelectedTab != second.TabID {
		t.Fatal("closed tab retained")
	}
	reopened := apply(t, m, Command{Action: "reopen-tab", WindowID: first.Windows[0]})
	if reopened.Workspace.Windows[0].Tabs[1] != tab || reopened.Workspace.Windows[0].SelectedTab != tab.ID {
		t.Fatal("reopen changed stable binding")
	}
}
func TestExplicitLastTabCloseDoesNotRestore(t *testing.T) {
	m, _, _ := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "close-tab", WindowID: first.Windows[0], TabID: first.TabID})
	next := apply(t, m, Command{Action: "launch"})
	if next.Restored || next.TabID == first.TabID {
		t.Fatal("explicitly closed tab restored")
	}
}
func TestLayoutIsSavedAndSnapshotsAreIsolated(t *testing.T) {
	m, store, api := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	second := apply(t, m, Command{Action: "new-tab", WindowID: first.Windows[0]})
	apply(t, m, Command{Action: "reorder-tabs", WindowID: first.Windows[0], Order: []string{second.TabID, first.TabID}})
	apply(t, m, Command{Action: "select-tab", WindowID: first.Windows[0], TabID: first.TabID})
	geometry := Geometry{X: 10, Y: 20, Width: 900, Height: 600}
	apply(t, m, Command{Action: "geometry", WindowID: first.Windows[0], Geometry: &geometry})
	s := m.Snapshot()
	s.Windows[0].Tabs[0].SessionID = "mutated"
	restored, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	w := restored.Snapshot().Windows[0]
	if w.Geometry != geometry || w.Tabs[0].ID != second.TabID || w.SelectedTab != first.TabID || w.Tabs[0].SessionID == "mutated" {
		t.Fatal("layout not durably isolated")
	}
}
func TestSaveFailurePreventsClaimAndMemoryMutation(t *testing.T) {
	m, store, api := setup(t)
	before := m.Snapshot()
	store.fail = true
	if _, err := m.Apply(t.Context(), Command{Action: "launch"}); err == nil {
		t.Fatal("write failure ignored")
	}
	if !reflect.DeepEqual(before, m.Snapshot()) || len(api.claims) != 0 {
		t.Fatal("state changed without durability")
	}
}
func TestUnknownCommandsAndMalformedOrderFail(t *testing.T) {
	m, _, _ := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	for _, c := range []Command{{Action: "surprise"}, {Action: "reorder-tabs", WindowID: first.Windows[0], Order: []string{"missing"}}, {Action: "select-tab", WindowID: first.Windows[0], TabID: "missing"}} {
		if _, err := m.Apply(t.Context(), c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}
func TestFileStoreRoundTripAndCorruption(t *testing.T) {
	dir := privateTemp(t)
	store := FileStore{Directory: dir}
	m, err := NewManager(store, nil, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	original := apply(t, m, Command{Action: "launch"})
	loaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(original.Workspace, loaded) {
		t.Fatalf("round trip: %v", err)
	}
	path := filepath.Join(dir, "workspace.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("workspace is not private")
	}
	if err = os.WriteFile(path, []byte("{bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewManager(store, nil, testOrigin); err == nil {
		t.Fatal("corruption silently reset state")
	}
}
func privateTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "oa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(fmt.Errorf("cleanup: %w", err))
		}
	})
	return dir
}

type blockedAllocation struct {
	unmanagedRooms
	entered chan client.ClaimRequest
	release chan struct{}
}

func (b blockedAllocation) DesktopCapabilities(context.Context) (client.DesktopAllocation, error) {
	return client.DesktopAllocation{Version: 1, MaxActiveRooms: 4}, nil
}
func (b blockedAllocation) ClaimRoom(ctx context.Context, in client.ClaimRequest) (client.RoomBinding, error) {
	b.entered <- in
	select {
	case <-ctx.Done():
		return client.RoomBinding{}, ctx.Err()
	case <-b.release:
		return client.RoomBinding{DeviceID: in.DeviceID, SessionID: in.SessionID, Room: client.Room{ID: "remote-room"}, Ready: true}, nil
	}
}
func (b blockedAllocation) RoomBinding(context.Context, string, string) (client.RoomBinding, error) {
	return client.RoomBinding{}, errors.New("unexpected restore")
}
func TestBlockedClaimDoesNotFreezeOrResurrectClosedTab(t *testing.T) {
	store := &memoryStore{}
	api := blockedAllocation{entered: make(chan client.ClaimRequest, 1), release: make(chan struct{})}
	m, err := NewManager(store, api, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	first := apply(t, m, Command{Action: "launch"})
	finished := make(chan error, 1)
	go func() {
		_, err := m.Apply(t.Context(), Command{Action: "prepare", TabID: first.TabID})
		finished <- err
	}()
	<-api.entered
	changed := make(chan error, 1)
	go func() {
		_, err := m.Apply(t.Context(), Command{Action: "new-window"})
		if err == nil {
			_, err = m.Apply(t.Context(), Command{Action: "close-tab", WindowID: first.Windows[0], TabID: first.TabID})
		}
		changed <- err
	}()
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(api.release)
		t.Fatal("network blocked window actions")
	}
	close(api.release)
	if err := <-finished; err == nil {
		t.Fatal("closed tab was prepared")
	}
	state := m.Snapshot()
	if len(state.Windows) != 1 || findTab(&state, first.TabID) != nil {
		t.Fatal("closed tab resurrected")
	}
}
func TestSyncWindowsRecoversAfterKittyCrash(t *testing.T) {
	m, _, _ := setup(t)
	first := apply(t, m, Command{Action: "launch"})
	apply(t, m, Command{Action: "sync-windows", Order: []string{}})
	next := apply(t, m, Command{Action: "launch"})
	if !next.Restored || len(next.Windows) != 1 || next.Windows[0] != first.Windows[0] {
		t.Fatal("crash created a replacement layout")
	}
}

func TestStoppedAndLostWorkersDoNotSpinOrCreateReplacement(t *testing.T) {
	for _, tc := range []struct{ state, status string }{{"stopped", "stopped"}, {"stopping", "stopped"}, {"attention", "recovery-required"}} {
		t.Run(tc.state, func(t *testing.T) {
			m, _, api := setup(t)
			launch := apply(t, m, Command{Action: "launch"})
			apply(t, m, Command{Action: "prepare", TabID: launch.TabID})
			api.binding.Ready = false
			api.binding.Room.State = tc.state
			result := apply(t, m, Command{Action: "prepare", TabID: launch.TabID})
			if result.Status != tc.status || len(api.claims) != 1 {
				t.Fatal("nonready room was not handled explicitly")
			}
		})
	}
}

// Allocation-only fixtures reject unexpected lifecycle calls explicitly.
type unmanagedRooms struct{}

func (unmanagedRooms) Rooms(context.Context) ([]client.Room, error) {
	return nil, errors.New("unexpected room listing")
}
func (unmanagedRooms) Room(context.Context, string) (client.Room, error) {
	return client.Room{}, errors.New("unexpected direct room lookup")
}
func (unmanagedRooms) StopRoom(context.Context, string) (client.Room, error) {
	return client.Room{}, errors.New("unexpected stop")
}
func (unmanagedRooms) ResumeRoom(context.Context, string) (client.Room, error) {
	return client.Room{}, errors.New("unexpected resume")
}
