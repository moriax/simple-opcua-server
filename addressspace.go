package opcuaserver

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/server/attrs"
	"github.com/gopcua/opcua/ua"
)

// dataTypeNodeID maps a tag data type to its OPC UA built-in DataType NodeID.
var dataTypeNodeID = map[DataType]uint32{
	TypeBoolean:  id.Boolean,
	TypeSByte:    id.SByte,
	TypeByte:     id.Byte,
	TypeInt16:    id.Int16,
	TypeUInt16:   id.UInt16,
	TypeInt32:    id.Int32,
	TypeUInt32:   id.UInt32,
	TypeInt64:    id.Int64,
	TypeUInt64:   id.UInt64,
	TypeFloat:    id.Float,
	TypeDouble:   id.Double,
	TypeString:   id.String,
	TypeDateTime: id.DateTime,
}

// typeDefRef builds the HasTypeDefinition reference every Object and Variable
// node needs so that clients can resolve what kind of node they are browsing.
func typeDefRef(typeID uint32, name string, nc ua.NodeClass) *ua.ReferenceDescription {
	eid := ua.NewNumericExpandedNodeID(0, typeID)
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition),
		IsForward:       true,
		NodeID:          eid,
		BrowseName:      attrs.BrowseName(name),
		DisplayName:     attrs.DisplayName(name, ""),
		NodeClass:       nc,
		TypeDefinition:  eid,
	}
}

// newFolder creates an Object node of type FolderType used to mirror one level
// of the dotted tag path.
func newFolder(nsID uint16, path, name string) *server.Node {
	return server.NewNode(
		ua.NewStringNodeID(nsID, path),
		server.Attributes{
			ua.AttributeIDNodeClass:     server.DataValueFromValue(uint32(ua.NodeClassObject)),
			ua.AttributeIDBrowseName:    server.DataValueFromValue(attrs.BrowseName(name)),
			ua.AttributeIDDisplayName:   server.DataValueFromValue(attrs.DisplayName(name, "")),
			ua.AttributeIDDescription:   server.DataValueFromValue(attrs.DisplayName(path, "")),
			ua.AttributeIDEventNotifier: server.DataValueFromValue(byte(0)),
			ua.AttributeIDWriteMask:     server.DataValueFromValue(uint32(0)),
			ua.AttributeIDUserWriteMask: server.DataValueFromValue(uint32(0)),
		},
		server.References{typeDefRef(id.FolderType, "FolderType", ua.NodeClassObjectType)},
		nil,
	)
}

// newVariable creates the Variable node for a tag, initialised to the type
// default and carrying the access level the tag file asked for.
func newVariable(nsID uint16, t Tag, f NodeIDFormat, initial any) (*server.Node, error) {
	if initial == nil {
		initial = t.DataType.ZeroValue()
	}
	v, err := ua.NewVariant(initial)
	if err != nil {
		return nil, fmt.Errorf("cannot encode initial %s value: %w", t.DataType, err)
	}

	now := time.Now()
	init := &ua.DataValue{
		EncodingMask:    ua.DataValueValue | ua.DataValueStatusCode | ua.DataValueSourceTimestamp | ua.DataValueServerTimestamp,
		Value:           v,
		Status:          ua.StatusOK,
		SourceTimestamp: now,
		ServerTimestamp: now,
	}

	var access uint8
	if t.Read {
		access |= uint8(ua.AccessLevelTypeCurrentRead)
	}
	if t.Write {
		access |= uint8(ua.AccessLevelTypeCurrentWrite)
	}

	dtID, ok := dataTypeNodeID[t.DataType]
	if !ok {
		return nil, fmt.Errorf("no NodeID known for data type %s", t.DataType)
	}

	return server.NewNode(
		ua.NewStringNodeID(nsID, t.NodeID(f)),
		server.Attributes{
			ua.AttributeIDNodeClass:       server.DataValueFromValue(uint32(ua.NodeClassVariable)),
			ua.AttributeIDBrowseName:      server.DataValueFromValue(attrs.BrowseName(t.Name)),
			ua.AttributeIDDisplayName:     server.DataValueFromValue(attrs.DisplayName(t.Name, "")),
			ua.AttributeIDDescription:     server.DataValueFromValue(attrs.DisplayName(t.FullPath, "")),
			ua.AttributeIDDataType:        server.DataValueFromValue(ua.NewNumericNodeID(0, dtID)),
			ua.AttributeIDValueRank:       server.DataValueFromValue(int32(-1)), // -1 = Scalar
			ua.AttributeIDAccessLevel:     server.DataValueFromValue(access),
			ua.AttributeIDUserAccessLevel: server.DataValueFromValue(access),
			ua.AttributeIDHistorizing:     server.DataValueFromValue(false),
			ua.AttributeIDWriteMask:       server.DataValueFromValue(uint32(0)),
			ua.AttributeIDUserWriteMask:   server.DataValueFromValue(uint32(0)),
		},
		server.References{typeDefRef(id.BaseDataVariableType, "BaseDataVariableType", ua.NodeClassVariableType)},
		func() *ua.DataValue { return init },
	), nil
}

// BuildOptions controls how the address space is laid out.
type BuildOptions struct {
	// Separator splits a tag name into path levels.
	Separator string
	// NodeIDs selects whether a tag's NodeID keeps the tag file's prefix.
	NodeIDs NodeIDFormat
	// Initial holds restored values keyed by NodeID identifier. A tag that is
	// absent here starts at its data type's default.
	Initial map[string]any
}

// BuildResult reports what Build put into the address space.
type BuildResult struct {
	Folders   int
	Variables int
	Warnings  []string
}

// Build populates ns with a folder hierarchy mirroring the dotted tag paths and
// a Variable node per tag, and hangs the whole tree off the server's Objects
// folder so that clients can browse to it from the standard root.
func Build(srv *server.Server, ns *server.NodeNameSpace, tags []Tag, opts BuildOptions) (*BuildResult, error) {
	res := &BuildResult{}
	nsID := ns.ID()
	separator := opts.Separator

	root := ns.Objects()
	if root == nil {
		return nil, fmt.Errorf("namespace %d has no Objects folder", nsID)
	}

	// nodes maps a dotted path to the node that owns it, so that a child can
	// find its parent regardless of whether that parent is a folder or (in a
	// malformed file where a tag is also a path prefix) a variable.
	nodes := map[string]*server.Node{}

	claimed := make(map[string]bool, len(tags))
	for _, t := range tags {
		claimed[t.FullPath] = true
	}

	// Collect every intermediate path that needs a folder. A path claimed by a
	// tag keeps its Variable node and gains children instead; OPC UA allows a
	// Variable to have components.
	folderSet := map[string]bool{}
	for _, t := range tags {
		for i := 1; i < len(t.Path); i++ {
			p := strings.Join(t.Path[:i], separator)
			if !claimed[p] {
				folderSet[p] = true
			}
		}
	}

	folders := make([]string, 0, len(folderSet))
	for p := range folderSet {
		folders = append(folders, p)
	}
	// Shallow paths first so a folder's parent always exists before it is wired.
	sort.Slice(folders, func(i, j int) bool {
		di := strings.Count(folders[i], separator)
		dj := strings.Count(folders[j], separator)
		if di != dj {
			return di < dj
		}
		return folders[i] < folders[j]
	})

	for _, p := range folders {
		parts := strings.Split(p, separator)
		n := newFolder(nsID, p, parts[len(parts)-1])
		ns.AddNode(n)
		nodes[p] = n
		res.Folders++
	}

	for _, t := range tags {
		n, err := newVariable(nsID, t, opts.NodeIDs, opts.Initial[t.NodeID(opts.NodeIDs)])
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped tag %q: %v", t.FullPath, err))
			continue
		}
		ns.AddNode(n)
		nodes[t.FullPath] = n
		res.Variables++
	}

	// Wire each node to its parent. Done after every node exists because
	// AddRef snapshots the child's BrowseName, NodeClass and type definition.
	attach := func(path string, n *server.Node) {
		parts := strings.Split(path, separator)
		if len(parts) == 1 {
			root.AddRef(n, id.HasComponent, true)
			return
		}
		parentPath := strings.Join(parts[:len(parts)-1], separator)
		parent, ok := nodes[parentPath]
		if !ok {
			// Cannot happen for folders (created parent-first) but keeps a
			// malformed file from silently dropping nodes out of the tree.
			res.Warnings = append(res.Warnings, fmt.Sprintf("%q has no parent node %q, attaching to the namespace root", path, parentPath))
			root.AddRef(n, id.HasComponent, true)
			return
		}
		parent.AddRef(n, id.HasComponent, true)
	}

	for _, p := range folders {
		attach(p, nodes[p])
	}
	for _, t := range tags {
		if n, ok := nodes[t.FullPath]; ok {
			attach(t.FullPath, n)
		}
	}

	// Finally make the namespace reachable from the standard Objects folder.
	rootNS, err := srv.Namespace(0)
	if err != nil {
		return nil, fmt.Errorf("cannot access namespace 0: %w", err)
	}
	rootNS.Objects().AddRef(root, id.HasComponent, true)

	return res, nil
}
