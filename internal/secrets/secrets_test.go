package secrets

import "testing"

func TestEnvStoreGet(t *testing.T) {
	s, err := openEnvForTest()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Backend(); got != "env" {
		t.Fatalf("Backend() = %q, want env", got)
	}

	t.Setenv("ED_API_TOKEN", "  tok-123  ")
	v, ok, err := s.Get(EdToken)
	if err != nil || !ok {
		t.Fatalf("Get(EdToken) ok=%v err=%v", ok, err)
	}
	if v != "tok-123" {
		t.Fatalf("Get(EdToken) = %q, want trimmed tok-123", v)
	}

	t.Setenv("MOODLE_COOKIE", "")
	if _, ok, _ := s.Get(MoodleCookie); ok {
		t.Fatal("empty env var should read as absent")
	}
}

func TestEnvStoreReadOnly(t *testing.T) {
	s := envStore{}
	if err := s.Set(EdToken, "x"); err != ErrReadOnly {
		t.Fatalf("Set err = %v, want ErrReadOnly", err)
	}
	if err := s.Delete(EdToken); err != ErrReadOnly {
		t.Fatalf("Delete err = %v, want ErrReadOnly", err)
	}
}

func TestProxy(t *testing.T) {
	t.Setenv("ED_AUTH", "proxy")
	t.Setenv("MOODLE_AUTH", "PROXY")
	if !Proxy("ed") || !Proxy("moodle") {
		t.Fatal("Proxy should be true for ed and moodle (case-insensitive)")
	}
	if Proxy("other") {
		t.Fatal("Proxy(other) should be false")
	}
	t.Setenv("ED_AUTH", "")
	if Proxy("ed") {
		t.Fatal("Proxy(ed) should be false when ED_AUTH is empty")
	}
}

// openEnvForTest returns the env store regardless of GOOS, so the shared
// behaviour is testable everywhere (the Windows store needs a real vault).
func openEnvForTest() (Store, error) { return envStore{}, nil }
