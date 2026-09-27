package opcuaserver

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Tag is one row of the address space definition file.
type Tag struct {
	Raw       string   // first column as written, e.g. "t|MIX.MIXER1.BatchId"
	Prefix    string   // the source system's item-kind prefix including the bar, e.g. "t|"
	Path      []string // ["MIX", "MIXER1", "BatchId"]
	FullPath  string   // "MIX.MIXER1.BatchId", the path without the prefix
	Name      string   // "BatchId"
	DataType  DataType
	Read      bool
	Write     bool
	Simulated bool
	Line      int
}

// DataType is the subset of OPC UA built-in types the tag file can name.
type DataType string

const (
	TypeBoolean  DataType = "Boolean"
	TypeSByte    DataType = "SByte"
	TypeByte     DataType = "Byte"
	TypeInt16    DataType = "Int16"
	TypeUInt16   DataType = "UInt16"
	TypeInt32    DataType = "Int32"
	TypeUInt32   DataType = "UInt32"
	TypeInt64    DataType = "Int64"
	TypeUInt64   DataType = "UInt64"
	TypeFloat    DataType = "Float"
	TypeDouble   DataType = "Double"
	TypeString   DataType = "String"
	TypeDateTime DataType = "DateTime"
)

// dataTypeAliases maps the spellings seen in tag files to OPC UA built-in
// types. Lookup is done on the lower-cased name with any "io_" prefix removed.
var dataTypeAliases = map[string]DataType{
	"boolean": TypeBoolean, "bool": TypeBoolean, "bit": TypeBoolean,
	"sbyte": TypeSByte, "int8": TypeSByte, "sint": TypeSByte,
	"byte": TypeByte, "uint8": TypeByte, "usint": TypeByte,
	"int16": TypeInt16, "short": TypeInt16, "int": TypeInt16,
	"uint16": TypeUInt16, "word": TypeUInt16, "uint": TypeUInt16,
	"int32": TypeInt32, "dint": TypeInt32, "long": TypeInt32,
	"uint32": TypeUInt32, "dword": TypeUInt32, "udint": TypeUInt32,
	"int64": TypeInt64, "lint": TypeInt64,
	"uint64": TypeUInt64, "ulint": TypeUInt64, "lword": TypeUInt64,
	"float": TypeFloat, "real": TypeFloat, "single": TypeFloat, "float32": TypeFloat,
	"double": TypeDouble, "lreal": TypeDouble, "float64": TypeDouble,
	"string": TypeString, "str": TypeString, "text": TypeString,
	"datetime": TypeDateTime, "date": TypeDateTime, "time": TypeDateTime,
}

// ParseDataType resolves a tag-file type name. ok is false for unknown names,
// in which case String is returned as the fallback type.
func ParseDataType(s string) (dt DataType, ok bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.TrimPrefix(k, "io_")
	if dt, ok := dataTypeAliases[k]; ok {
		return dt, true
	}
	return TypeString, false
}

// ZeroValue is the initial value a tag is served with: the type default, as
// requested, rather than a null value.
func (dt DataType) ZeroValue() any {
	switch dt {
	case TypeBoolean:
		return false
	case TypeSByte:
		return int8(0)
	case TypeByte:
		return uint8(0)
	case TypeInt16:
		return int16(0)
	case TypeUInt16:
		return uint16(0)
	case TypeInt32:
		return int32(0)
	case TypeUInt32:
		return uint32(0)
	case TypeInt64:
		return int64(0)
	case TypeUInt64:
		return uint64(0)
	case TypeFloat:
		return float32(0)
	case TypeDouble:
		return float64(0)
	case TypeDateTime:
		return time.Time{}
	default:
		return ""
	}
}

// column names accepted for each field, lower-cased and space-stripped.
var columnAliases = map[string][]string{
	"name":      {"tagname", "tag", "name", "item", "itemid", "nodename"},
	"datatype":  {"datatype", "type"},
	"access":    {"accessrights", "access", "rights", "accesslevel"},
	"simulated": {"simulated", "simulate", "simulation"},
}

// layout maps a field name to its column index in the file.
type layout map[string]int

// positional is the layout assumed when the file has no header row.
var positional = layout{"name": 0, "datatype": 1, "access": 2, "simulated": 3}

// detectHeader returns the column layout described by rec, or nil if rec does
// not look like a header row.
func detectHeader(rec []string) layout {
	l := layout{}
	for i, cell := range rec {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(cell), " ", ""))
		for field, aliases := range columnAliases {
			for _, a := range aliases {
				if key == a {
					if _, dup := l[field]; !dup {
						l[field] = i
					}
				}
			}
		}
	}
	// A header is only credible if it at least names the tag column.
	if _, ok := l["name"]; !ok {
		return nil
	}
	return l
}

func (l layout) get(rec []string, field string) string {
	i, ok := l[field]
	if !ok || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// parseAccess reads an access-rights cell such as "RW", "RO" or "R". An empty
// cell means read/write.
func parseAccess(s string) (read, write bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return true, true
	}
	// "RO" is read-only despite containing no W; treat the O as a no-op.
	return strings.ContainsRune(s, 'R'), strings.ContainsRune(s, 'W')
}

// LoadTags reads the address space definition at path. Recoverable problems
// (unknown data types, duplicate tag names, unusable rows) are returned as
// warnings; in strict mode the first one is returned as an error instead.
func LoadTags(path, separator string, strict bool) (tags []Tag, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // rows are validated per-field below
	r.TrimLeadingSpace = true

	var l layout
	seen := make(map[string]int) // FullPath -> line of first definition
	line := 0

	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	for {
		rec, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		line++
		if rerr != nil {
			if strict {
				return nil, warnings, fmt.Errorf("%s:%d: %w", path, line, rerr)
			}
			warn("%s:%d: skipped unreadable row: %v", path, line, rerr)
			continue
		}

		if l == nil {
			if h := detectHeader(rec); h != nil {
				l = h
				continue
			}
			l = positional
		}

		raw := l.get(rec, "name")
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue // blank line or comment
		}

		// Split off the source system's item-kind prefix ("t|" for tag). It
		// is kept so that NodeIDs can reproduce the original identifier.
		full, prefix := raw, ""
		if i := strings.Index(full, "|"); i >= 0 {
			prefix, full = full[:i+1], full[i+1:]
		}
		full = strings.Trim(strings.TrimSpace(full), separator)
		if full == "" {
			warn("%s:%d: skipped %q: empty tag path", path, line, raw)
			continue
		}

		parts := strings.Split(full, separator)
		for i, p := range parts {
			if strings.TrimSpace(p) == "" {
				warn("%s:%d: skipped %q: empty path segment %d", path, line, raw, i+1)
				parts = nil
				break
			}
		}
		if parts == nil {
			continue
		}

		if first, dup := seen[full]; dup {
			if strict {
				return nil, warnings, fmt.Errorf("%s:%d: duplicate tag %q, first defined on line %d", path, line, full, first)
			}
			warn("%s:%d: ignored duplicate tag %q (first defined on line %d)", path, line, full, first)
			continue
		}
		seen[full] = line

		rawType := l.get(rec, "datatype")
		dt, known := ParseDataType(rawType)
		if !known {
			if strict {
				return nil, warnings, fmt.Errorf("%s:%d: tag %q has unknown data type %q", path, line, full, rawType)
			}
			warn("%s:%d: tag %q has unknown data type %q, using %s", path, line, full, rawType, dt)
		}

		read, write := parseAccess(l.get(rec, "access"))
		if !read && !write {
			warn("%s:%d: tag %q has no access rights (%q), serving it read-only", path, line, full, l.get(rec, "access"))
			read = true
		}

		sim := strings.EqualFold(l.get(rec, "simulated"), "true")

		tags = append(tags, Tag{
			Raw:       raw,
			Prefix:    prefix,
			Path:      parts,
			FullPath:  full,
			Name:      parts[len(parts)-1],
			DataType:  dt,
			Read:      read,
			Write:     write,
			Simulated: sim,
			Line:      line,
		})
	}

	if len(tags) == 0 {
		return nil, warnings, fmt.Errorf("%s: no tags found", path)
	}
	return tags, warnings, nil
}

// NodeIDFormat selects the identifier used for a tag's string NodeID.
type NodeIDFormat string

const (
	// NodeIDRaw keeps the identifier exactly as the tag file writes it,
	// prefix included: "t|MIX.MIXER1.BatchId".
	NodeIDRaw NodeIDFormat = "raw"
	// NodeIDPath drops the prefix: "MIX.MIXER1.BatchId".
	NodeIDPath NodeIDFormat = "path"
)

// ParseNodeIDFormat validates a -nodeid flag value.
func ParseNodeIDFormat(s string) (NodeIDFormat, error) {
	switch NodeIDFormat(strings.ToLower(strings.TrimSpace(s))) {
	case NodeIDRaw:
		return NodeIDRaw, nil
	case NodeIDPath:
		return NodeIDPath, nil
	default:
		return "", fmt.Errorf("unknown node ID format %q, want %q or %q", s, NodeIDRaw, NodeIDPath)
	}
}

// NodeID returns the string identifier of the tag's Variable node.
func (t Tag) NodeID(f NodeIDFormat) string {
	if f == NodeIDPath {
		return t.FullPath
	}
	return t.Prefix + t.FullPath
}
