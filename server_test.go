package opcuaserver

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

func exampleTags(t *testing.T) []Tag {
	t.Helper()
	tags, _, err := LoadTags("config/AddressSpace.example.csv", ".", false)
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

// freePort asks the OS for a port that is free right now. Binding to port 0
// would work, but the server would then advertise "opc.tcp://host:0" and no
// client could find its way back.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newTestServer starts a server on a free port and stops it with the test.
func newTestServer(t *testing.T, tags []Tag, opts ...Option) *Server {
	t.Helper()
	opts = append([]Option{WithPort(freePort(t)), WithoutSecurity(), WithoutPersistence()}, opts...)
	srv, err := New(tags, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop() })
	return srv
}

// The four calls the package promises: New, Start, Set, Stop.
func TestServerLifecycle(t *testing.T) {
	tags := exampleTags(t)
	srv, err := New(tags, WithPort(freePort(t)), WithoutSecurity(), WithoutPersistence())
	if err != nil {
		t.Fatal(err)
	}

	// Set works before Start, so a program can seed values and then serve them.
	if err := srv.Set("t|TST.TEST1.MyTestTagInt32", 7); err != nil {
		t.Fatalf("Set before Start: %v", err)
	}
	if got, err := srv.Get("t|TST.TEST1.MyTestTagInt32"); err != nil || got != int32(7) {
		t.Errorf("Get before Start = (%#v, %v), want 7", got, err)
	}

	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err == nil {
		t.Error("starting twice should be an error")
	}

	if err := srv.Set("t|TST.TEST1.MyTestTagInt32", 9); err != nil {
		t.Fatalf("Set while running: %v", err)
	}
	if got, _ := srv.Get("t|TST.TEST1.MyTestTagInt32"); got != int32(9) {
		t.Errorf("Get = %#v, want 9", got)
	}

	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	// Stopping twice is a no-op rather than an error.
	if err := srv.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	tags := exampleTags(t)
	cases := map[string][]Option{
		"no tags":         nil,
		"port":            {WithPort(70000)},
		"namespace index": {WithNamespaceIndex(0)},
		"separator":       {WithSeparator("")},
		"auth":            {WithAuth()},
		"save interval":   {WithPersistence("x.json", time.Millisecond)},
	}
	for name, opts := range cases {
		in := tags
		if name == "no tags" {
			in = nil
		}
		base := []Option{WithPort(freePort(t)), WithoutSecurity(), WithoutPersistence()}
		if _, err := New(in, append(base, opts...)...); err == nil {
			t.Errorf("New with a bad %s was accepted", name)
		}
	}
}

// A tag can be named by its NodeID identifier or by its dotted path.
func TestSetAcceptsEitherName(t *testing.T) {
	srv := newTestServer(t, exampleTags(t))

	if err := srv.Set("t|TST.TEST1.MyTestTagString", "by node id"); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Get("TST.TEST1.MyTestTagString"); got != "by node id" {
		t.Errorf("reading by path gave %#v", got)
	}

	if err := srv.Set("TST.TEST1.MyTestTagString", "by path"); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Get("t|TST.TEST1.MyTestTagString"); got != "by path" {
		t.Errorf("reading by node id gave %#v", got)
	}

	if err := srv.Set("TST.TEST1.NoSuchTag", 1); err == nil {
		t.Error("an unknown tag should be an error")
	}
	if _, err := srv.Get("TST.TEST1.NoSuchTag"); err == nil {
		t.Error("getting an unknown tag should be an error")
	}
}

// Set converts a plain Go value to the tag's declared type, and refuses one
// that would not survive the trip.
func TestSetConvertsAndRangeChecks(t *testing.T) {
	srv := newTestServer(t, exampleTags(t))

	ok := []struct {
		tag  string
		in   any
		want any
	}{
		{"t|TST.TEST1.MyTestTagInt16", 42, int16(42)},
		{"t|TST.TEST1.MyTestTagInt16", int64(-100), int16(-100)},
		{"t|TST.TEST1.MyTestTagInt32", 123456789, int32(123456789)},
		{"t|TST.TEST1.MyTestTagByte", 255, uint8(255)},
		{"t|TST.TEST1.MyTestTagDouble", 5, float64(5)},
		{"t|TST.TEST1.MyTestTagDouble", float32(1.5), float64(1.5)},
		{"t|TST.TEST1.MyTestTagBoolean", true, true},
		{"t|TST.TEST1.MyTestTagString", "hello", "hello"},
	}
	for _, c := range ok {
		if err := srv.Set(c.tag, c.in); err != nil {
			t.Errorf("Set(%s, %#v) = %v", c.tag, c.in, err)
			continue
		}
		got, _ := srv.Get(c.tag)
		if got != c.want {
			t.Errorf("Set(%s, %#v) stored %#v, want %#v", c.tag, c.in, got, c.want)
		}
	}

	bad := []struct {
		tag string
		in  any
	}{
		{"t|TST.TEST1.MyTestTagInt16", 40000}, // out of range
		{"t|TST.TEST1.MyTestTagByte", -1},     // out of range
		{"t|TST.TEST1.MyTestTagByte", 256},    // out of range
		{"t|TST.TEST1.MyTestTagInt32", 1.5},   // not a whole number
		{"t|TST.TEST1.MyTestTagBoolean", 1},   // not a bool
		{"t|TST.TEST1.MyTestTagInt32", "12"},  // not a number
		{"t|TST.TEST1.MyTestTagString", 12},   // not a string
		{"t|TST.TEST1.MyTestTagInt32", nil},   // nothing
	}
	for _, c := range bad {
		if err := srv.Set(c.tag, c.in); err == nil {
			t.Errorf("Set(%s, %#v) was accepted, want an error", c.tag, c.in)
		}
	}
}

// Set is for the program hosting the server, so it is not bound by the access
// level that governs client writes.
func TestSetIgnoresAccessLevel(t *testing.T) {
	tags := []Tag{tag("A.ReadOnly", TypeInt32, true, false)}
	srv := newTestServer(t, tags)

	if err := srv.Set("t|A.ReadOnly", 5); err != nil {
		t.Fatalf("Set on a client-read-only tag = %v", err)
	}
	if got, _ := srv.Get("t|A.ReadOnly"); got != int32(5) {
		t.Errorf("value = %#v, want 5", got)
	}
}

// An embedded server has to be reachable by a real OPC UA client.
func TestServerServesTagsToAClient(t *testing.T) {
	srv := newTestServer(t, exampleTags(t))
	if err := srv.Set("t|MIX.MIXER1.BatchId", "BATCH-1"); err != nil {
		t.Fatal(err)
	}

	urls := srv.EndpointURLs()
	if len(urls) == 0 {
		t.Fatal("no endpoint URLs")
	}

	ctx := context.Background()
	c, err := opcua.NewClient(urls[0], opcua.SecurityMode(ua.MessageSecurityModeNone))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)

	id := ua.NewStringNodeID(srv.NamespaceIndex(), "t|MIX.MIXER1.BatchId")
	resp, err := c.Read(ctx, &ua.ReadRequest{
		NodesToRead: []*ua.ReadValueID{{NodeID: id, AttributeID: ua.AttributeIDValue}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Results[0].Value.Value(); got != "BATCH-1" {
		t.Errorf("client read %#v, want BATCH-1", got)
	}

	// What a client writes must be visible through Get.
	if _, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: id, AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant("BATCH-2")},
	}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Get("t|MIX.MIXER1.BatchId"); got != "BATCH-2" {
		t.Errorf("Get after a client write = %#v, want BATCH-2", got)
	}
}

// Values set through the library have to survive a restart too.
func TestServerPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.json")
	tags := exampleTags(t)

	first, err := New(tags, WithPort(freePort(t)), WithoutSecurity(), WithPersistence(path, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Set("t|MIX.MIXER1.BatchId", "PERSISTED"); err != nil {
		t.Fatal(err)
	}
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}

	second, err := New(tags, WithPort(freePort(t)), WithoutSecurity(), WithPersistence(path, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Stop() })
	if second.RestoredValues() == 0 {
		t.Fatal("nothing was restored")
	}
	if got, _ := second.Get("t|MIX.MIXER1.BatchId"); got != "PERSISTED" {
		t.Errorf("after restart = %#v, want PERSISTED", got)
	}
}

func TestServerNodeIDsAndAddressSpace(t *testing.T) {
	srv := newTestServer(t, exampleTags(t), WithNamespaceIndex(3))

	if got := srv.NamespaceIndex(); got != 3 {
		t.Errorf("NamespaceIndex() = %d, want 3", got)
	}
	got, err := srv.NodeID("t|MIX.MIXER1.BatchId")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ns=3;s=t|MIX.MIXER1.BatchId"; got != want {
		t.Errorf("NodeID() = %q, want %q", got, want)
	}

	as := srv.AddressSpace()
	if as.Variables != len(srv.Tags()) {
		t.Errorf("built %d variables for %d tags", as.Variables, len(srv.Tags()))
	}
	if as.Folders == 0 {
		t.Error("no folders were built")
	}
}

func TestWithNodeIDFormatPath(t *testing.T) {
	srv := newTestServer(t, exampleTags(t), WithNodeIDFormat(NodeIDPath))

	if got, err := srv.NodeID("MIX.MIXER1.BatchId"); err != nil || got != "ns=2;s=MIX.MIXER1.BatchId" {
		t.Errorf("NodeID() = (%q, %v)", got, err)
	}
	if err := srv.Set("MIX.MIXER1.BatchId", "x"); err != nil {
		t.Errorf("Set by path = %v", err)
	}
}
