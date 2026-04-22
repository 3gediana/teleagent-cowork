package llm

// Registry + resolution tests. Uses lightweight fake Provider to
// exercise behaviour without touching HTTP.

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	id     ProviderID
	name   string
	models []ModelInfo
	called bool
}

func (f *fakeProvider) ID() ProviderID     { return f.id }
func (f *fakeProvider) Name() string       { return f.name }
func (f *fakeProvider) Models() []ModelInfo { return f.models }
func (f *fakeProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	f.called = true
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Type: EvMessageStop, StopReason: StopEnd}
	close(ch)
	return ch, nil
}

func newReg() *Registry { return &Registry{entries: map[string]*Entry{}} }

func TestRegistry_RegisterGet(t *testing.T) {
	r := newReg()
	fp := &fakeProvider{id: ProviderAnthropic, name: "test"}
	r.Register(&Entry{EndpointID: "ep1", Format: ProviderAnthropic, DefaultModel: "m1", Provider: fp})
	got, err := r.Get("ep1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != fp {
		t.Error("provider identity lost through registry")
	}
}

func TestRegistry_RegisterOverwrites(t *testing.T) {
	r := newReg()
	p1 := &fakeProvider{id: ProviderAnthropic}
	p2 := &fakeProvider{id: ProviderAnthropic}
	r.Register(&Entry{EndpointID: "ep1", Provider: p1})
	r.Register(&Entry{EndpointID: "ep1", Provider: p2})
	got, _ := r.Get("ep1")
	if got.Provider != p2 {
		t.Error("second Register should overwrite the first")
	}
}

func TestRegistry_RemoveAndGet(t *testing.T) {
	r := newReg()
	r.Register(&Entry{EndpointID: "ep1", Provider: &fakeProvider{}})
	r.Remove("ep1")
	if _, err := r.Get("ep1"); err == nil {
		t.Error("Get after Remove should fail")
	}
}

func TestRegistry_RemoveMissingIsNoop(t *testing.T) {
	r := newReg()
	// Should not panic or error even though ep-zzz was never registered.
	r.Remove("ep-zzz")
}

func TestRegistry_ResolveRoleWithExplicitModel(t *testing.T) {
	r := newReg()
	r.Register(&Entry{EndpointID: "ep1", DefaultModel: "m-default", Provider: &fakeProvider{}})
	e, m, err := r.ResolveRole("ep1", "m-override")
	if err != nil {
		t.Fatal(err)
	}
	if e.EndpointID != "ep1" {
		t.Errorf("wrong endpoint: %s", e.EndpointID)
	}
	if m != "m-override" {
		t.Errorf("caller model should win; got %q", m)
	}
}

func TestRegistry_ResolveRoleFallsBackToDefault(t *testing.T) {
	r := newReg()
	r.Register(&Entry{EndpointID: "ep1", DefaultModel: "m-default", Provider: &fakeProvider{}})
	_, m, err := r.ResolveRole("ep1", "")
	if err != nil {
		t.Fatal(err)
	}
	if m != "m-default" {
		t.Errorf("should fall back to endpoint default; got %q", m)
	}
}

func TestRegistry_ResolveRoleFirstEntryWhenNoEndpointGiven(t *testing.T) {
	r := newReg()
	// Multiple entries; the one with a DefaultModel wins.
	r.Register(&Entry{EndpointID: "ep-empty", Provider: &fakeProvider{}})
	r.Register(&Entry{EndpointID: "ep-default", DefaultModel: "fallback", Provider: &fakeProvider{}})
	e, m, err := r.ResolveRole("", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.EndpointID != "ep-default" {
		t.Errorf("should pick the entry with a default, got %s", e.EndpointID)
	}
	if m != "fallback" {
		t.Errorf("should use that entry's default; got %q", m)
	}
}

func TestRegistry_ResolveRoleEmptyRegistryReturnsErr(t *testing.T) {
	r := newReg()
	_, _, err := r.ResolveRole("", "")
	if !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("empty registry should return ErrNoEndpoint, got %v", err)
	}
}

func TestRegistry_ChatStreamFallsThroughToProvider(t *testing.T) {
	r := newReg()
	fp := &fakeProvider{id: ProviderAnthropic}
	r.Register(&Entry{EndpointID: "ep1", DefaultModel: "m1", Provider: fp})
	ch, err := r.ChatStream(context.Background(), "ep1", ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if !fp.called {
		t.Error("provider.ChatStream should have been invoked")
	}
}
