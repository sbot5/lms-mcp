// Package secrets reads and writes named credentials. On Windows the backing
// store is Credential Manager; on other platforms, and in cloud development,
// it is read-only environment variables. Values are never logged.
package secrets

import (
	"errors"
	"os"
	"strings"
)

// ErrReadOnly is returned by Set and Delete on the environment-backed store.
var ErrReadOnly = errors.New("secrets: environment-backed store is read-only")

// Name is a stable credential key.
type Name string

const (
	EdToken      Name = "ed.token"        // env: ED_API_TOKEN
	MoodleCookie Name = "moodle.cookie"   // env: MOODLE_COOKIE
	MoodleICal   Name = "moodle.ical_url" // env: MOODLE_ICAL_URL
	MoodleBase   Name = "moodle.base_url" // env: MOODLE_BASE_URL
)

// envVar maps each Name to the environment variable the env store reads.
var envVar = map[Name]string{
	EdToken:      "ED_API_TOKEN",
	MoodleCookie: "MOODLE_COOKIE",
	MoodleICal:   "MOODLE_ICAL_URL",
	MoodleBase:   "MOODLE_BASE_URL",
}

// Store reads and writes credentials by Name.
type Store interface {
	Get(n Name) (value string, ok bool, err error)
	Set(n Name, value string) error
	Delete(n Name) error
	Backend() string // "wincred" or "env"; for doctor/status output only
}

// Proxy reports whether the environment delegates a provider's auth header to
// the agent proxy (ED_AUTH=proxy or MOODLE_AUTH=proxy). When true, the caller
// sends no Authorization/Cookie header for that provider; the proxy adds it.
func Proxy(provider string) bool {
	switch provider {
	case "ed":
		return strings.EqualFold(os.Getenv("ED_AUTH"), "proxy")
	case "moodle":
		return strings.EqualFold(os.Getenv("MOODLE_AUTH"), "proxy")
	default:
		return false
	}
}

// envStore reads credentials from environment variables and refuses writes.
type envStore struct{}

func (envStore) Backend() string { return "env" }

func (envStore) Get(n Name) (string, bool, error) {
	key, known := envVar[n]
	if !known {
		return "", false, nil
	}
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", false, nil
	}
	return v, true, nil
}

func (envStore) Set(Name, string) error { return ErrReadOnly }
func (envStore) Delete(Name) error      { return ErrReadOnly }
