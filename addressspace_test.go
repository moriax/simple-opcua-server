package opcuaserver

import (
	"context"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// build creates the address space on a running server. The server is started
// on an OS-assigned local port because writing an attribute notifies the
// subscription machinery, which only exists once the server has started.
func build(t *testing.T, tags []Tag) (*server.NodeNameSpace, *BuildResult) {
	t.Helper()
	return buildWith(t, tags, BuildOptions{Separator: ".", NodeIDs: NodeIDPath})
}

func buildWith(t *testing.T, tags []Tag, opts BuildOptions) (*server.NodeNameSpace, *BuildResult) {
	t.Helper()
	srv := server.New(server.EndPoint("127.0.0.1", 0))
	ns := server.NewNodeNameSpace(srv, "urn:test:tags")
	res, err := Build(srv, ns, tags, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return ns, res
}

func tag(path string, dt DataType, read, write bool) Tag {
	parts := splitPath(path)
	return Tag{
		Raw: "t|" + path, Prefix: "t|", Path: parts, FullPath: path, Name: parts[len(parts)-1],
		DataType: dt, Read: read, Write: write,
	}
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

func TestBuildHierarchy(t *testing.T) {
	ns, res := build(t, []Tag{
		tag("MIX.MIXER1.BatchId", TypeString, true, true),
		tag("MIX.MIXER1.FUNCTION.Speed", TypeInt16, true, true),
		tag("MIX.MIXER2.BatchId", TypeString, true, true),
		tag("TST.Flag", TypeBoolean, true, true),
	})

	if res.Variables != 4 {
		t.Errorf("Variables = %d, want 4", res.Variables)
	}
	// MIX, MIX.MIXER1, MIX.MIXER1.FUNCTION, MIX.MIXER2, TST
	if res.Folders != 5 {
		t.Errorf("Folders = %d, want 5", res.Folders)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}

	for _, id := range []string{"MIX", "MIX.MIXER1", "MIX.MIXER1.FUNCTION", "MIX.MIXER2", "TST"} {
		n := ns.Node(ua.NewStringNodeID(ns.ID(), id))
		if n == nil {
			t.Fatalf("folder %q missing", id)
		}
		if n.NodeClass() != ua.NodeClassObject {
			t.Errorf("folder %q has node class %v, want Object", id, n.NodeClass())
		}
	}

	v := ns.Node(ua.NewStringNodeID(ns.ID(), "MIX.MIXER1.FUNCTION.Speed"))
	if v == nil {
		t.Fatal("variable MIX.MIXER1.FUNCTION.Speed missing")
	}
	if v.NodeClass() != ua.NodeClassVariable {
		t.Errorf("node class = %v, want Variable", v.NodeClass())
	}
	if got := v.BrowseName().Name; got != "Speed" {
		t.Errorf("BrowseName = %q, want the leaf name Speed", got)
	}
}

func TestBuildBrowseReachesChildren(t *testing.T) {
	ns, _ := build(t, []Tag{
		tag("MIX.MIXER1.BatchId", TypeString, true, true),
		tag("MIX.MIXER1.Level", TypeInt16, true, true),
		tag("MIX.MIXER2.BatchId", TypeString, true, true),
	})

	children := func(nodeID *ua.NodeID) []string {
		res := ns.Browse(&ua.BrowseDescription{
			NodeID:          nodeID,
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
			IncludeSubtypes: true,
			ResultMask:      uint32(ua.BrowseResultMaskAll),
			NodeClassMask:   uint32(ua.NodeClassObject | ua.NodeClassVariable),
		})
		var names []string
		for _, r := range res.References {
			names = append(names, r.BrowseName.Name)
		}
		return names
	}

	root := children(ua.NewNumericNodeID(ns.ID(), 85)) // the namespace Objects folder
	if len(root) != 1 || root[0] != "MIX" {
		t.Errorf("namespace root children = %v, want [MIX]", root)
	}
	if got := children(ua.NewStringNodeID(ns.ID(), "MIX")); len(got) != 2 {
		t.Errorf("MIX children = %v, want MIXER1 and MIXER2", got)
	}
	if got := children(ua.NewStringNodeID(ns.ID(), "MIX.MIXER1")); len(got) != 2 {
		t.Errorf("MIX.MIXER1 children = %v, want BatchId and Level", got)
	}
}

func TestBuildValuesAndAccessLevels(t *testing.T) {
	ns, _ := build(t, []Tag{
		tag("A.Str", TypeString, true, true),
		tag("A.Num", TypeDouble, true, true),
		tag("A.Flag", TypeBoolean, true, true),
		tag("A.ReadOnly", TypeInt32, true, false),
	})

	want := map[string]any{
		"A.Str": "", "A.Num": float64(0), "A.Flag": false, "A.ReadOnly": int32(0),
	}
	for path, wantVal := range want {
		dv := ns.Attribute(ua.NewStringNodeID(ns.ID(), path), ua.AttributeIDValue)
		if dv.Status != ua.StatusOK {
			t.Errorf("%s: status = %v, want Good", path, dv.Status)
		}
		if got := dv.Value.Value(); got != wantVal {
			t.Errorf("%s: value = %#v, want %#v", path, got, wantVal)
		}
		if dv.SourceTimestamp.IsZero() || dv.ServerTimestamp.IsZero() {
			t.Errorf("%s: initial value has no timestamps", path)
		}
	}

	rw := ns.Attribute(ua.NewStringNodeID(ns.ID(), "A.Str"), ua.AttributeIDAccessLevel)
	if got := rw.Value.Value(); got != uint8(3) {
		t.Errorf("RW access level = %#v, want 3 (read|write)", got)
	}
	ro := ns.Attribute(ua.NewStringNodeID(ns.ID(), "A.ReadOnly"), ua.AttributeIDAccessLevel)
	if got := ro.Value.Value(); got != uint8(1) {
		t.Errorf("read-only access level = %#v, want 1 (read)", got)
	}
}

func TestBuildWriteIsRejectedOnReadOnlyTag(t *testing.T) {
	ns, _ := build(t, []Tag{
		tag("A.Writable", TypeInt32, true, true),
		tag("A.ReadOnly", TypeInt32, true, false),
	})

	dv := server.DataValueFromValue(int32(7))

	if got := ns.SetAttribute(ua.NewStringNodeID(ns.ID(), "A.Writable"), ua.AttributeIDValue, dv); got != ua.StatusOK {
		t.Errorf("write to a writable tag returned %v, want Good", got)
	}
	back := ns.Attribute(ua.NewStringNodeID(ns.ID(), "A.Writable"), ua.AttributeIDValue)
	if got := back.Value.Value(); got != int32(7) {
		t.Errorf("value after write = %#v, want 7", got)
	}

	if got := ns.SetAttribute(ua.NewStringNodeID(ns.ID(), "A.ReadOnly"), ua.AttributeIDValue, dv); got == ua.StatusOK {
		t.Error("write to a read-only tag should be rejected")
	}
}

func TestBuildDataTypeAttribute(t *testing.T) {
	ns, _ := build(t, []Tag{
		tag("A.Str", TypeString, true, true),
		tag("A.Num", TypeDouble, true, true),
		tag("A.Small", TypeInt16, true, true),
	})
	want := map[string]uint32{"A.Str": 12, "A.Num": 11, "A.Small": 4}
	for path, wantID := range want {
		dv := ns.Attribute(ua.NewStringNodeID(ns.ID(), path), ua.AttributeIDDataType)
		nid, ok := dv.Value.Value().(*ua.NodeID)
		if !ok {
			t.Fatalf("%s: DataType attribute is %T, want *ua.NodeID", path, dv.Value.Value())
		}
		if uint32(nid.IntID()) != wantID || nid.Namespace() != 0 {
			t.Errorf("%s: DataType = %v, want ns=0;i=%d", path, nid, wantID)
		}
	}
}

// A tag whose path is also a prefix of another tag's path cannot get its own
// folder. It must keep its Variable node and gain the children instead of being
// dropped.
func TestBuildTagThatIsAlsoAPathPrefix(t *testing.T) {
	ns, res := build(t, []Tag{
		tag("A.B", TypeInt32, true, true),
		tag("A.B.C", TypeInt32, true, true),
	})
	if res.Variables != 2 {
		t.Fatalf("Variables = %d, want both tags kept", res.Variables)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}

	parent := ns.Node(ua.NewStringNodeID(ns.ID(), "A.B"))
	if parent == nil || parent.NodeClass() != ua.NodeClassVariable {
		t.Fatalf("A.B should still be a Variable, got %v", parent)
	}
	res2 := ns.Browse(&ua.BrowseDescription{
		NodeID:          ua.NewStringNodeID(ns.ID(), "A.B"),
		BrowseDirection: ua.BrowseDirectionForward,
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
		IncludeSubtypes: true,
		ResultMask:      uint32(ua.BrowseResultMaskAll),
		NodeClassMask:   uint32(ua.NodeClassVariable),
	})
	var found bool
	for _, r := range res2.References {
		if r.BrowseName.Name == "C" {
			found = true
		}
	}
	if !found {
		t.Error("A.B.C is not reachable by browsing A.B")
	}
}

// The tags must be addressable by the identifier the source system uses, so
// the tag file's "t|" prefix is kept in the NodeID by default.
func TestBuildRawNodeIDsKeepThePrefix(t *testing.T) {
	tags := []Tag{tag("MIX.MIXER1.BatchId", TypeString, true, true)}
	ns, res := buildWith(t, tags, BuildOptions{Separator: ".", NodeIDs: NodeIDRaw})
	if res.Variables != 1 {
		t.Fatalf("Variables = %d, want 1", res.Variables)
	}

	if n := ns.Node(ua.NewStringNodeID(ns.ID(), "t|MIX.MIXER1.BatchId")); n == nil {
		t.Error("tag is not addressable as ns=x;s=t|MIX.MIXER1.BatchId")
	}
	if n := ns.Node(ua.NewStringNodeID(ns.ID(), "MIX.MIXER1.BatchId")); n != nil {
		t.Error("tag should not also answer to the unprefixed identifier")
	}
	// Folders are not tags, so they never carry the prefix.
	if n := ns.Node(ua.NewStringNodeID(ns.ID(), "MIX.MIXER1")); n == nil {
		t.Error("folder should keep the plain dotted path as its identifier")
	}
	// The browse tree is built from the path, so the prefix must not leak
	// into the names a client sees.
	v := ns.Node(ua.NewStringNodeID(ns.ID(), "t|MIX.MIXER1.BatchId"))
	if got := v.BrowseName().Name; got != "BatchId" {
		t.Errorf("BrowseName = %q, want BatchId", got)
	}
}

func TestBuildPathNodeIDsDropThePrefix(t *testing.T) {
	tags := []Tag{tag("MIX.MIXER1.BatchId", TypeString, true, true)}
	ns, _ := buildWith(t, tags, BuildOptions{Separator: ".", NodeIDs: NodeIDPath})
	if n := ns.Node(ua.NewStringNodeID(ns.ID(), "MIX.MIXER1.BatchId")); n == nil {
		t.Error("tag is not addressable as ns=x;s=MIX.MIXER1.BatchId")
	}
}
