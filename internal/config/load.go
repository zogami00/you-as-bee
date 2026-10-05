package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// Validator is implemented by every config type that can check itself after
// decoding. Load calls it automatically when the destination implements it.
type Validator interface {
	Validate() error
}

// defaulter is implemented by config types that want their defaults applied
// before the JSON document is decoded. Applying defaults first means an
// explicit value in the file (including an explicit `false` for a boolean that
// defaults to `true`) always wins over the default.
type defaulter interface {
	setDefaults()
}

// Duration is a time.Duration that encodes to and from JSON as a Go duration
// string such as "2s" or "2m30s". A bare JSON number is rejected.
type Duration time.Duration

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String returns the Go duration string form.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON encodes the value as a duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts only a JSON string that time.ParseDuration understands.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"2s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv replaces every ${NAME} reference in s with the value of the
// environment variable NAME. Unset variables expand to the empty string.
// Only the braced ${...} form is recognised, so a lone "$" in a config value
// is left untouched.
func ExpandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(match string) string {
		name := envRef.FindStringSubmatch(match)[1]
		return os.Getenv(name)
	})
}

// Load reads a strict JSON config file.
//
// It expands ${ENV} references in the raw text, applies the destination's
// defaults (when it implements defaulter), decodes with unknown fields
// disallowed, rejects any trailing data after the JSON document, and finally
// calls Validate when the destination implements Validator.
//
// dst must be a non-nil pointer.
func Load(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	expanded := ExpandEnv(string(raw))

	if d, ok := dst.(defaulter); ok {
		d.setDefaults()
	}

	dec := json.NewDecoder(strings.NewReader(expanded))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}

	// Reject anything after the first complete JSON document.
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("config %s: unexpected trailing data after JSON document", path)
		}
		return fmt.Errorf("config %s: trailing data: %w", path, err)
	}

	if v, ok := dst.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	}

	return nil
}
