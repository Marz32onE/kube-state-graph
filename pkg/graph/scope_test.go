package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Inventory maps the request's `prune` parameter onto the projection: the
// ZERO Scope must keep the default prune on, so the flag is stored inverted.
func TestNewScope_InventoryFlag(t *testing.T) {
	s := NewScope(nil, nil, false)
	assert.False(t, s.Inventory, "prune=true (the default) leaves Inventory unset")

	s = NewScope(nil, nil, true)
	assert.True(t, s.Inventory)
}

// Empty values are dropped from both sets, so `?cluster=&cluster=c1` and
// `?cluster=c1` build the same scope — the determinism rule that keeps two
// differently-spelled requests byte-identical.
func TestNewScope_DropsEmptyValues(t *testing.T) {
	s := NewScope([]string{"", "c1"}, []string{""}, false)
	assert.Equal(t, map[string]struct{}{"c1": {}}, s.Clusters)
	assert.Empty(t, s.Namespaces)
}
