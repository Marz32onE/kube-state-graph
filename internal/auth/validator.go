package auth

// Validator is the minimal contract the API server depends on. *KeySet is the
// production implementation; tests inject a mockery-generated mock so they
// can exercise auth-enabled code paths without writing keys to disk or
// hot-reloading a file.
type Validator interface {
	Validate(presented string) bool
	Empty() bool
}

// Compile-time assertion that *KeySet satisfies Validator.
var _ Validator = (*KeySet)(nil)
