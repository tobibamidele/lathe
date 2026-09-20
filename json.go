package lathe

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON holds a raw JSON document read from or written to a JSON column. A nil
// JSON is stored as SQL NULL.
type JSON []byte

// NewJSON marshals v.
func NewJSON(v any) (JSON, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return JSON(b), nil
}

// Decode unmarshals the document into v.
func (j JSON) Decode(v any) error { return json.Unmarshal(j, v) }

// Value implements driver.Valuer.
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// Scan implements sql.Scanner.
func (j *JSON) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case string:
		*j = JSON(v)
	case []byte:
		*j = append((*j)[:0], v...)
	default:
		return fmt.Errorf("lathe: cannot scan %T into JSON", src)
	}
	return nil
}

// MarshalJSON emits the raw document, or null when empty.
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON stores the raw document.
func (j *JSON) UnmarshalJSON(b []byte) error {
	*j = append((*j)[:0], b...)
	return nil
}
