package opcuaserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

func TestPersisterSavesOnlyWhatChanged(t *testing.T) {
	tags := []Tag{
		tag("A.Str", TypeString, true, true),
		tag("A.Num", TypeInt32, true, true),
	}
	ns, _ := buildWith(t, tags, BuildOptions{Separator: ".", NodeIDs: NodeIDRaw})

	path := filepath.Join(t.TempDir(), "values.json")
	logger, _, err := NewLogger(LogOff, "")
	if err != nil {
		t.Fatal(err)
	}
	p := NewPersister(path, ns, tags, NodeIDRaw, time.Second, logger)
	p.MarkSaved()

	// Nothing has changed since startup, so there is nothing to write.
	if n, err := p.SaveIfChanged(); err != nil || n != 0 {
		t.Fatalf("SaveIfChanged() = (%d, %v), want (0, nil) before any write", n, err)
	}

	st := ns.SetAttribute(ua.NewStringNodeID(ns.ID(), "t|A.Num"),
		ua.AttributeIDValue, server.DataValueFromValue(int32(99)))
	if st != ua.StatusOK {
		t.Fatalf("write returned %v", st)
	}

	n, err := p.SaveIfChanged()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("SaveIfChanged() reported %d changed values, want 1", n)
	}

	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}
	// Every tag is written, not just the changed one, so a restart restores
	// the whole address space.
	if len(stored) != len(tags) {
		t.Errorf("store holds %d values, want %d", len(stored), len(tags))
	}
	got, err := decodeValue(TypeInt32, stored["t|A.Num"].Value)
	if err != nil {
		t.Fatal(err)
	}
	if got != int32(99) {
		t.Errorf("saved value = %v, want 99", got)
	}

	// A second call with nothing further changed must not rewrite the file.
	if n, err := p.SaveIfChanged(); err != nil || n != 0 {
		t.Errorf("SaveIfChanged() = (%d, %v), want (0, nil) when nothing moved", n, err)
	}
}

// Whatever else happens, a value written just before shutdown must reach disk.
func TestPersisterSavesOnShutdown(t *testing.T) {
	tags := []Tag{tag("A.Num", TypeInt32, true, true)}
	ns, _ := buildWith(t, tags, BuildOptions{Separator: ".", NodeIDs: NodeIDRaw})

	path := filepath.Join(t.TempDir(), "values.json")
	logger, _, err := NewLogger(LogOff, "")
	if err != nil {
		t.Fatal(err)
	}
	// An interval far longer than the test, so only the shutdown save can fire.
	p := NewPersister(path, ns, tags, NodeIDRaw, time.Hour, logger)
	p.MarkSaved()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()

	ns.SetAttribute(ua.NewStringNodeID(ns.ID(), "t|A.Num"),
		ua.AttributeIDValue, server.DataValueFromValue(int32(4242)))

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeValue(TypeInt32, stored["t|A.Num"].Value)
	if err != nil {
		t.Fatal(err)
	}
	if got != int32(4242) {
		t.Errorf("value after shutdown = %v, want 4242", got)
	}
}

// The restored value has to be what a client reads back, not just what sits in
// the file.
func TestBuildStartsFromRestoredValues(t *testing.T) {
	tags := []Tag{
		tag("A.Str", TypeString, true, true),
		tag("A.Num", TypeInt32, true, true),
		tag("A.Untouched", TypeDouble, true, true),
	}
	ns, _ := buildWith(t, tags, BuildOptions{
		Separator: ".",
		NodeIDs:   NodeIDRaw,
		Initial: map[string]any{
			"t|A.Str": "restored",
			"t|A.Num": int32(7),
		},
	})

	want := map[string]any{
		"t|A.Str":       "restored",
		"t|A.Num":       int32(7),
		"t|A.Untouched": float64(0), // no saved value, so the type default
	}
	for id, expect := range want {
		dv := ns.Attribute(ua.NewStringNodeID(ns.ID(), id), ua.AttributeIDValue)
		if dv.Status != ua.StatusOK {
			t.Errorf("%s: status %v", id, dv.Status)
			continue
		}
		if got := dv.Value.Value(); got != expect {
			t.Errorf("%s = %#v, want %#v", id, got, expect)
		}
	}
}
