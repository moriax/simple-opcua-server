package opcuaserver

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// storeVersion is written into the file so a later format change can be
// recognised rather than misread.
const storeVersion = 1

type storedValue struct {
	Type DataType `json:"type"`
	// Value is kept raw so that each type is decoded by the tag's declared
	// data type. Decoding into any would turn every number into a float64 and
	// lose precision on 64 bit integers.
	Value json.RawMessage `json:"value"`
}

type storeFile struct {
	Version int                    `json:"version"`
	Saved   time.Time              `json:"saved"`
	Values  map[string]storedValue `json:"values"`
}

// LoadValues reads persisted tag values, keyed by NodeID identifier. A missing
// file is not an error: it just means nothing has been saved yet.
func LoadValues(path string) (map[string]storedValue, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is not readable as a value store: %w", path, err)
	}
	if f.Version != storeVersion {
		return nil, fmt.Errorf("%s has format version %d, this build writes version %d", path, f.Version, storeVersion)
	}
	return f.Values, nil
}

// SaveValues writes the values atomically, so that a crash midway through
// leaves the previous file intact rather than a truncated one.
func SaveValues(path string, values map[string]any, types map[string]DataType) error {
	out := storeFile{
		Version: storeVersion,
		Saved:   time.Now().UTC(),
		Values:  make(map[string]storedValue, len(values)),
	}

	// Sorted keys keep the file stable between saves, which makes it readable
	// and diffable.
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		raw, err := encodeValue(values[k])
		if err != nil {
			return fmt.Errorf("cannot encode %s: %w", k, err)
		}
		out.Values[k] = storedValue{Type: types[k], Value: raw}
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".values-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeded

	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// encodeValue turns a tag value into JSON. Floats need care because JSON has no
// way to write NaN or an infinity, and a client is free to write either.
func encodeValue(v any) (json.RawMessage, error) {
	switch x := v.(type) {
	case float32:
		if s, ok := specialFloat(float64(x)); ok {
			return json.Marshal(s)
		}
	case float64:
		if s, ok := specialFloat(x); ok {
			return json.Marshal(s)
		}
	}
	return json.Marshal(v)
}

// decodeValue turns a stored value back into the Go type the tag's data type
// calls for.
func decodeValue(dt DataType, raw json.RawMessage) (any, error) {
	switch dt {
	case TypeBoolean:
		return unmarshal[bool](raw)
	case TypeSByte:
		return unmarshal[int8](raw)
	case TypeByte:
		return unmarshal[uint8](raw)
	case TypeInt16:
		return unmarshal[int16](raw)
	case TypeUInt16:
		return unmarshal[uint16](raw)
	case TypeInt32:
		return unmarshal[int32](raw)
	case TypeUInt32:
		return unmarshal[uint32](raw)
	case TypeInt64:
		return unmarshal[int64](raw)
	case TypeUInt64:
		return unmarshal[uint64](raw)
	case TypeFloat:
		if f, ok, err := decodeSpecialFloat(raw); ok {
			return float32(f), err
		}
		return unmarshal[float32](raw)
	case TypeDouble:
		if f, ok, err := decodeSpecialFloat(raw); ok {
			return f, err
		}
		return unmarshal[float64](raw)
	case TypeString:
		return unmarshal[string](raw)
	case TypeDateTime:
		return unmarshal[time.Time](raw)
	default:
		return nil, fmt.Errorf("no decoder for data type %s", dt)
	}
}

func unmarshal[T any](raw json.RawMessage) (any, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func specialFloat(f float64) (string, bool) {
	switch {
	case math.IsNaN(f):
		return "NaN", true
	case math.IsInf(f, 1):
		return "+Inf", true
	case math.IsInf(f, -1):
		return "-Inf", true
	}
	return "", false
}

// decodeSpecialFloat reports whether raw is one of the JSON strings used for a
// value that JSON numbers cannot express.
func decodeSpecialFloat(raw json.RawMessage) (float64, bool, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false, nil // an ordinary number, not a special value
	}
	switch s {
	case "NaN":
		return math.NaN(), true, nil
	case "+Inf":
		return math.Inf(1), true, nil
	case "-Inf":
		return math.Inf(-1), true, nil
	}
	return 0, true, fmt.Errorf("%q is not a number", s)
}
