package opcuaserver

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTripsEveryDataType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.json")

	values := map[string]any{
		"bool":   true,
		"sbyte":  int8(-8),
		"byte":   uint8(200),
		"int16":  int16(-4242),
		"uint16": uint16(65535),
		"int32":  int32(123456789),
		"uint32": uint32(4294967295),
		// Beyond 2^53, so a decoder that goes through float64 loses this.
		"int64":  int64(9007199254740993),
		"uint64": uint64(18446744073709551615),
		"float":  float32(1.5),
		"double": float64(78.125),
		"string": "BATCH-2026-0921",
		"time":   time.Date(2026, 9, 21, 11, 7, 13, 0, time.UTC),
	}
	types := map[string]DataType{
		"bool": TypeBoolean, "sbyte": TypeSByte, "byte": TypeByte,
		"int16": TypeInt16, "uint16": TypeUInt16, "int32": TypeInt32,
		"uint32": TypeUInt32, "int64": TypeInt64, "uint64": TypeUInt64,
		"float": TypeFloat, "double": TypeDouble, "string": TypeString,
		"time": TypeDateTime,
	}

	if err := SaveValues(path, values, types); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}

	for k, want := range values {
		sv, ok := stored[k]
		if !ok {
			t.Errorf("%s is missing from the store", k)
			continue
		}
		if sv.Type != types[k] {
			t.Errorf("%s: stored type %s, want %s", k, sv.Type, types[k])
		}
		got, err := decodeValue(types[k], sv.Value)
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if got != want {
			t.Errorf("%s: round tripped to %#v, want %#v", k, got, want)
		}
	}
}

// JSON has no way to write NaN or an infinity, but a client can write either to
// a Float or Double tag.
func TestStoreHandlesNonFiniteFloats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.json")

	values := map[string]any{
		"nan":      math.NaN(),
		"posinf":   math.Inf(1),
		"neginf":   math.Inf(-1),
		"nan32":    float32(math.NaN()),
		"ordinary": float64(1.25),
	}
	types := map[string]DataType{
		"nan": TypeDouble, "posinf": TypeDouble, "neginf": TypeDouble,
		"nan32": TypeFloat, "ordinary": TypeDouble,
	}

	if err := SaveValues(path, values, types); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}

	check := func(key string, want func(any) bool) {
		t.Helper()
		got, err := decodeValue(types[key], stored[key].Value)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !want(got) {
			t.Errorf("%s round tripped to %#v", key, got)
		}
	}
	check("nan", func(v any) bool { f, ok := v.(float64); return ok && math.IsNaN(f) })
	check("nan32", func(v any) bool { f, ok := v.(float32); return ok && math.IsNaN(float64(f)) })
	check("posinf", func(v any) bool { f, ok := v.(float64); return ok && math.IsInf(f, 1) })
	check("neginf", func(v any) bool { f, ok := v.(float64); return ok && math.IsInf(f, -1) })
	check("ordinary", func(v any) bool { return v == float64(1.25) })
}

func TestLoadValuesMissingFileIsNotAnError(t *testing.T) {
	got, err := LoadValues(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing store should just be empty, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d values, want none", len(got))
	}
}

func TestLoadValuesRejectsAnotherFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"values":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadValues(path); err == nil {
		t.Error("a future format version should be rejected rather than misread")
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadValues(path); err == nil {
		t.Error("an unreadable store should be an error")
	}
}

// A save must never leave a half-written file behind for the next start.
func TestSaveValuesReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")

	if err := SaveValues(path, map[string]any{"a": int32(1)}, map[string]DataType{"a": TypeInt32}); err != nil {
		t.Fatal(err)
	}
	if err := SaveValues(path, map[string]any{"a": int32(2)}, map[string]DataType{"a": TypeInt32}); err != nil {
		t.Fatal(err)
	}

	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeValue(TypeInt32, stored["a"].Value)
	if got != int32(2) {
		t.Errorf("value = %v, want the second save", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("save left temporary files behind: %v", entries)
	}
}

func TestRestoreValues(t *testing.T) {
	tags := []Tag{
		tag("A.Str", TypeString, true, true),
		tag("A.Num", TypeInt32, true, true),
		tag("A.Changed", TypeInt32, true, true),
	}
	path := filepath.Join(t.TempDir(), "values.json")
	err := SaveValues(path,
		map[string]any{
			"t|A.Str":     "kept",
			"t|A.Num":     int32(7),
			"t|A.Changed": int32(9),
			"t|A.Gone":    int32(1),
		},
		map[string]DataType{
			"t|A.Str": TypeString, "t|A.Num": TypeInt32,
			// Saved under a type the tag file no longer declares.
			"t|A.Changed": TypeDouble, "t|A.Gone": TypeInt32,
		})
	if err != nil {
		t.Fatal(err)
	}

	stored, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}
	got, warnings := RestoreValues(stored, tags, NodeIDRaw)

	if got["t|A.Str"] != "kept" || got["t|A.Num"] != int32(7) {
		t.Errorf("values were not restored: %#v", got)
	}
	if _, ok := got["t|A.Changed"]; ok {
		t.Error("a value whose data type changed should not be restored")
	}
	if _, ok := got["t|A.Gone"]; ok {
		t.Error("a value for a tag that no longer exists should not be restored")
	}
	if len(warnings) != 2 {
		t.Errorf("want a warning for the type change and one for the removed tag, got %v", warnings)
	}
}

func TestRestoreValuesEmpty(t *testing.T) {
	got, warnings := RestoreValues(nil, []Tag{tag("A.B", TypeInt32, true, true)}, NodeIDRaw)
	if len(got) != 0 || len(warnings) != 0 {
		t.Errorf("an empty store should restore nothing quietly, got %v / %v", got, warnings)
	}
}
