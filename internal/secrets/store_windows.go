//go:build windows

package secrets

import (
	"errors"
	"fmt"

	"github.com/danieljoos/wincred"
)

// Open returns a Windows Credential Manager store. If ED_AUTH/MOODLE_AUTH or a
// credential is supplied through the environment instead, callers still reach
// those via the env lookups in config; this store owns the persisted secrets.
func Open(target string) (Store, error) {
	if target == "" {
		return nil, errors.New("secrets: empty Credential Manager target")
	}
	return winStore{target: target}, nil
}

type winStore struct{ target string }

func (winStore) Backend() string { return "wincred" }

func (w winStore) key(n Name) string { return w.target + ":" + string(n) }

func (w winStore) Get(n Name) (string, bool, error) {
	c, err := wincred.GetGenericCredential(w.key(n))
	if err != nil {
		// Not found is reported as an error by wincred; treat it as absent.
		return "", false, nil
	}
	return string(c.CredentialBlob), len(c.CredentialBlob) > 0, nil
}

func (w winStore) Set(n Name, value string) error {
	c := wincred.NewGenericCredential(w.key(n))
	c.CredentialBlob = []byte(value)
	c.UserName = string(n)
	c.Persist = wincred.PersistLocalMachine
	if err := c.Write(); err != nil {
		return fmt.Errorf("secrets: write %s failed", n) // never include the value
	}
	return nil
}

func (w winStore) Delete(n Name) error {
	c, err := wincred.GetGenericCredential(w.key(n))
	if err != nil {
		return nil // already absent
	}
	if err := c.Delete(); err != nil {
		return fmt.Errorf("secrets: delete %s failed", n)
	}
	return nil
}
