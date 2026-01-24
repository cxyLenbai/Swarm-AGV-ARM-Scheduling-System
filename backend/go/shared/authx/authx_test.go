package authx

import "testing"

func TestParseRoles(t *testing.T) {
	claims := map[string]any{
		"roles": []any{"admin", "operator"},
		"scp":   "read write",
	}
	roles := parseRoles(claims)
	if len(roles) < 3 {
		t.Fatalf("expected roles to include entries, got %v", roles)
	}
}

func TestNewJWTVerifierValidation(t *testing.T) {
	if _, err := NewJWTVerifier("", "aud", "", 60, 0); err == nil {
		t.Fatalf("expected error for missing issuer")
	}
}
