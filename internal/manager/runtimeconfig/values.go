package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/search"
)

const (
	StageOff   = "off"
	StageAdmin = "admin"
	StageBeta  = "beta"
	StageGA    = "ga"
)

const maxBetaEmails = 500

// Feature is the stored value of a feature gate.
type Feature struct {
	Stage      string   `json:"stage"`
	BetaEmails []string `json:"beta_emails"`
}

func (f Feature) validate() error {
	switch f.Stage {
	case StageOff, StageAdmin, StageBeta, StageGA:
	default:
		return errors.New("stage must be off, admin, beta, or ga")
	}
	if len(f.BetaEmails) > maxBetaEmails {
		return fmt.Errorf("beta_emails must contain at most %d addresses", maxBetaEmails)
	}
	for _, email := range f.BetaEmails {
		if !strings.Contains(email, "@") || len(email) > 254 ||
			strings.ContainsAny(email, " ,\r\n") {
			return fmt.Errorf("beta_emails contains an invalid address")
		}
	}
	return nil
}

// normalized lowercases, trims, de-duplicates, and sorts beta emails.
func (f Feature) normalized() Feature {
	seen := map[string]bool{}
	emails := make([]string, 0, len(f.BetaEmails))
	for _, raw := range f.BetaEmails {
		email := domain.NormalizeEmail(raw)
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return Feature{Stage: strings.ToLower(strings.TrimSpace(f.Stage)), BetaEmails: emails}
}

// Allows is the pure stage decision for one principal.
func (f Feature) Allows(p domain.UserPrincipal) bool {
	switch f.Stage {
	case StageGA:
		return p.User.UserID != ""
	case StageBeta:
		if p.IsAdmin {
			return true
		}
		email := domain.NormalizeEmail(p.User.Email)
		if email == "" {
			return false
		}
		for _, candidate := range f.BetaEmails {
			if candidate == email {
				return true
			}
		}
		return false
	case StageAdmin:
		return p.IsAdmin
	default:
		return false
	}
}

// decodeValue parses an API or database JSON value into the entry's typed
// value and validates it. Errors never contain the value.
func decodeValue(e *Entry, raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, errors.New("value is required")
	}
	var value any
	switch e.Type {
	case TypeString, TypeURL, TypeEnum, TypeDuration:
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, errors.New("must be a string")
		}
		text = strings.TrimSpace(text)
		if !utf8.ValidString(text) {
			return nil, errors.New("must be valid UTF-8")
		}
		value = text
	case TypeBool:
		var flag bool
		if json.Unmarshal(raw, &flag) != nil {
			return nil, errors.New("must be a boolean")
		}
		value = flag
	case TypeInt:
		var number int64
		if json.Unmarshal(raw, &number) != nil {
			return nil, errors.New("must be an integer")
		}
		value = number
	case TypeFeature:
		var f Feature
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&f) != nil {
			return nil, errors.New(`must be {"stage": ..., "beta_emails": [...]}`)
		}
		value = f.normalized()
	default:
		return nil, fmt.Errorf("unsupported type %q", e.Type)
	}
	if err := validateTyped(e, value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateTyped(e *Entry, value any) error {
	switch e.Type {
	case TypeEnum:
		text, _ := value.(string)
		found := false
		for _, option := range e.Options {
			if option == text {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("must be one of %s", strings.Join(e.Options, ", "))
		}
	case TypeDuration:
		text, _ := value.(string)
		if d, err := time.ParseDuration(text); err != nil || d < 0 {
			return errors.New("must be a non-negative duration such as 30s")
		}
	case TypeURL:
		if e.Validate == nil {
			text, _ := value.(string)
			if err := validateURL(text, "http", "https"); err != nil {
				return err
			}
		}
	}
	if e.Validate != nil {
		return e.Validate(value)
	}
	return nil
}

// encodeValue returns the canonical JSON stored for a non-secret value.
func encodeValue(value any) (json.RawMessage, error) {
	return json.Marshal(value)
}

// parseEnv converts an environment value. Environment values keep their
// historical semantics and are not checked against registry validation.
func parseEnv(e *Entry, raw string) (any, error) {
	if e.fromEnv != nil {
		return e.fromEnv(raw)
	}
	switch e.Type {
	case TypeBool:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return true, nil
		default:
			return false, nil
		}
	case TypeInt:
		return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	default:
		return raw, nil
	}
}

// display renders a value for the admin UI and audit log.
func display(e *Entry, value any) *string {
	if value == nil {
		return nil
	}
	var text string
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		text = typed
	case bool:
		text = strconv.FormatBool(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case Feature:
		text = typed.Stage
		if len(typed.BetaEmails) > 0 {
			text += " (beta: " + strings.Join(typed.BetaEmails, ", ") + ")"
		}
	default:
		text = fmt.Sprint(typed)
	}
	return &text
}

func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("must be an absolute URL without credentials or fragment")
	}
	for _, scheme := range schemes {
		if u.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("scheme must be one of %s", strings.Join(schemes, ", "))
}

func validateSearchURL(v any) error {
	text, _ := v.(string)
	if text == "" {
		return nil
	}
	return search.ValidateConfig(text, "pax-search-v1")
}

func validateSearchIndex(v any) error {
	text, _ := v.(string)
	return search.ValidateConfig("http://search.invalid", text)
}
