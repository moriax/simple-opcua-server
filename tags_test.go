package opcuaserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tags.csv")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseDataType(t *testing.T) {
	cases := []struct {
		in    string
		want  DataType
		known bool
	}{
		{"IO_String", TypeString, true},
		{"IO_Int16", TypeInt16, true},
		{"IO_Int32", TypeInt32, true},
		{"IO_Double", TypeDouble, true},
		{"IO_Boolean", TypeBoolean, true},
		{"IO_Byte", TypeByte, true},
		{" io_double ", TypeDouble, true},
		{"LREAL", TypeDouble, true},
		{"", TypeString, false},
		// The duplicated-suffix typo that appears in AddressSpace.csv.
		{"IO_StringIO_String", TypeString, false},
	}
	for _, c := range cases {
		got, known := ParseDataType(c.in)
		if got != c.want || known != c.known {
			t.Errorf("ParseDataType(%q) = (%v, %v), want (%v, %v)", c.in, got, known, c.want, c.known)
		}
	}
}

func TestZeroValueTypes(t *testing.T) {
	cases := map[DataType]any{
		TypeBoolean: false,
		TypeByte:    uint8(0),
		TypeInt16:   int16(0),
		TypeInt32:   int32(0),
		TypeDouble:  float64(0),
		TypeString:  "",
	}
	for dt, want := range cases {
		if got := dt.ZeroValue(); got != want {
			t.Errorf("%s.ZeroValue() = %#v, want %#v", dt, got, want)
		}
	}
}

func TestParseAccess(t *testing.T) {
	cases := []struct {
		in          string
		read, write bool
	}{
		{"RW", true, true},
		{"rw", true, true},
		{"R", true, false},
		{"RO", true, false},
		{"W", false, true},
		{"", true, true},
	}
	for _, c := range cases {
		r, w := parseAccess(c.in)
		if r != c.read || w != c.write {
			t.Errorf("parseAccess(%q) = (%v, %v), want (%v, %v)", c.in, r, w, c.read, c.write)
		}
	}
}

func TestLoadTagsHeaderAndPrefix(t *testing.T) {
	// CRLF line endings and no trailing newline, as in the real file.
	p := writeTemp(t, "Tag Name,Data Type,AccessRights,Simulated\r\n"+
		"t|MIX.MIXER1.BatchId,IO_String,RW,FALSE\r\n"+
		"t|MIX.MIXER1.CleaningLevel,IO_Int16,RW,FALSE")

	tags, warnings, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2", len(tags))
	}

	got := tags[0]
	if got.FullPath != "MIX.MIXER1.BatchId" {
		t.Errorf("FullPath = %q, want the t| prefix stripped", got.FullPath)
	}
	if got.Name != "BatchId" {
		t.Errorf("Name = %q, want BatchId", got.Name)
	}
	if len(got.Path) != 3 {
		t.Errorf("Path = %v, want 3 segments", got.Path)
	}
	if got.DataType != TypeString || !got.Read || !got.Write || got.Simulated {
		t.Errorf("unexpected tag fields: %+v", got)
	}
}

func TestLoadTagsWithoutHeader(t *testing.T) {
	p := writeTemp(t, "t|A.B,IO_Double,RW,FALSE\n")
	tags, _, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].FullPath != "A.B" || tags[0].DataType != TypeDouble {
		t.Fatalf("positional layout not applied: %+v", tags)
	}
}

func TestLoadTagsReordersColumnsFromHeader(t *testing.T) {
	p := writeTemp(t, "DataType,Simulated,TagName\nIO_Boolean,TRUE,t|X.Y\n")
	tags, _, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Fatalf("got %d tags, want 1", len(tags))
	}
	if tags[0].DataType != TypeBoolean || tags[0].FullPath != "X.Y" || !tags[0].Simulated {
		t.Errorf("columns not mapped from header: %+v", tags[0])
	}
	// No AccessRights column at all, so the tag defaults to read/write.
	if !tags[0].Read || !tags[0].Write {
		t.Errorf("missing access column should default to RW, got %+v", tags[0])
	}
}

func TestLoadTagsDuplicatesAndUnknownTypes(t *testing.T) {
	p := writeTemp(t, "Tag Name,Data Type,AccessRights,Simulated\n"+
		"t|A.B,IO_String,RW,FALSE\n"+
		"t|A.B,IO_Int16,RW,FALSE\n"+
		"t|A.C,IO_StringIO_String,RW,FALSE\n")

	tags, warnings, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2 (duplicate dropped)", len(tags))
	}
	if tags[0].DataType != TypeString {
		t.Errorf("first definition should win, got %v", tags[0].DataType)
	}
	if tags[1].DataType != TypeString {
		t.Errorf("unknown type should fall back to String, got %v", tags[1].DataType)
	}

	var sawDup, sawType bool
	for _, w := range warnings {
		if strings.Contains(w, "duplicate") {
			sawDup = true
		}
		if strings.Contains(w, "unknown data type") {
			sawType = true
		}
	}
	if !sawDup || !sawType {
		t.Errorf("missing warnings, got %v", warnings)
	}
}

func TestLoadTagsStrictRejects(t *testing.T) {
	dup := writeTemp(t, "Tag Name,Data Type\nt|A.B,IO_String\nt|A.B,IO_String\n")
	if _, _, err := LoadTags(dup, ".", true); err == nil {
		t.Error("strict mode should reject a duplicate tag")
	}

	bad := writeTemp(t, "Tag Name,Data Type\nt|A.B,IO_Nonsense\n")
	if _, _, err := LoadTags(bad, ".", true); err == nil {
		t.Error("strict mode should reject an unknown data type")
	}
}

func TestLoadTagsSkipsBlankAndMalformedPaths(t *testing.T) {
	p := writeTemp(t, "Tag Name,Data Type\n"+
		"t|A.B,IO_String\n"+
		"\n"+
		"#t|Commented.Out,IO_String\n"+
		"t|,IO_String\n"+
		"t|A..C,IO_String\n")

	tags, warnings, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Fatalf("got %d tags (%+v), want 1", len(tags), tags)
	}
	if len(warnings) != 2 {
		t.Errorf("want a warning for the empty path and the empty segment, got %v", warnings)
	}
}

func TestLoadTagsEmptyFile(t *testing.T) {
	p := writeTemp(t, "Tag Name,Data Type\n")
	if _, _, err := LoadTags(p, ".", false); err == nil {
		t.Error("a file with no tag rows should be an error")
	}
}

// TestLoadExampleAddressSpace parses the tag file shipped with the repository.
// It carries the same defects real exports do - two duplicated rows and one
// malformed data type - so this covers how they are handled.
func TestLoadExampleAddressSpace(t *testing.T) {
	const path = "config/AddressSpace.example.csv"
	tags, warnings, err := LoadTags(path, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 59 {
		t.Errorf("got %d tags, want 59", len(tags))
	}
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want 3: %v", len(warnings), warnings)
	}

	seen := map[DataType]bool{}
	for _, tag := range tags {
		if !tag.Read || !tag.Write {
			t.Errorf("tag %q is not RW", tag.FullPath)
		}
		if _, ok := dataTypeNodeID[tag.DataType]; !ok {
			t.Errorf("tag %q has data type %v with no NodeID mapping", tag.FullPath, tag.DataType)
		}
		seen[tag.DataType] = true
	}
	// The example is meant to exercise every type the server maps.
	for _, want := range []DataType{TypeBoolean, TypeByte, TypeInt16, TypeInt32, TypeDouble, TypeString} {
		if !seen[want] {
			t.Errorf("the example tag file has no %s tag", want)
		}
	}

	// Strict mode must reject the very defects the example carries.
	if _, _, err := LoadTags(path, ".", true); err == nil {
		t.Error("strict mode should reject the example file's duplicate rows")
	}
}

func TestTagNodeIDFormats(t *testing.T) {
	p := writeTemp(t, "Tag Name,Data Type\nt|MIX.MIXER1.BatchId,IO_String\nMIX.MIXER2.BatchId,IO_String\n")
	tags, _, err := LoadTags(p, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2", len(tags))
	}

	if got := tags[0].NodeID(NodeIDRaw); got != "t|MIX.MIXER1.BatchId" {
		t.Errorf("raw NodeID = %q, want the prefix kept", got)
	}
	if got := tags[0].NodeID(NodeIDPath); got != "MIX.MIXER1.BatchId" {
		t.Errorf("path NodeID = %q, want the prefix dropped", got)
	}
	// A row with no prefix is identical in both formats.
	if got := tags[1].NodeID(NodeIDRaw); got != "MIX.MIXER2.BatchId" {
		t.Errorf("raw NodeID of an unprefixed tag = %q", got)
	}
}

func TestParseNodeIDFormat(t *testing.T) {
	for _, in := range []string{"raw", "RAW", " path "} {
		if _, err := ParseNodeIDFormat(in); err != nil {
			t.Errorf("ParseNodeIDFormat(%q) = %v", in, err)
		}
	}
	if _, err := ParseNodeIDFormat("nonsense"); err == nil {
		t.Error("an unknown format should be rejected")
	}
}
