package auth

import "testing"

func TestGoogleConfigBuiltIn(t *testing.T) {
	googleClientID, googleClientSecret = "", ""
	if _, err := GoogleConfig(""); err == nil {
		t.Fatal("expected an error without a built-in client or client_secret_file")
	}

	googleClientID, googleClientSecret = "id.apps.googleusercontent.com", "secret"
	t.Cleanup(func() { googleClientID, googleClientSecret = "", "" })
	cfg, err := GoogleConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != googleClientID || cfg.ClientSecret != "secret" || len(cfg.Scopes) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Endpoint.TokenURL == "" {
		t.Fatal("missing token endpoint")
	}
}
