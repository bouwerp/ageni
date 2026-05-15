package roles_test

import (
	"testing"

	"github.com/bouwerp/ageni/internal/roles"
)

func TestEmbeddedRolesLoad(t *testing.T) {
	reg, err := roles.Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	expected := []struct {
		name      string
		tier      string
		minBudget int
	}{
		{"architect", "opus", 50},
		{"senior-engineer", "sonnet", 100},
		{"qa-engineer", "haiku", 50},
		{"reviewer", "sonnet", 50},
		{"devops-engineer", "sonnet", 100},
		{"product-owner", "haiku", 50},
		{"security-auditor", "sonnet", 50},
		{"tech-writer", "haiku", 50},
	}

	for _, tc := range expected {
		def, ok := reg.Lookup(tc.name)
		if !ok {
			t.Errorf("role %q not found", tc.name)
			continue
		}
		if def.ModelTier != tc.tier {
			t.Errorf("role %q: got tier %q, want %q", tc.name, def.ModelTier, tc.tier)
		}
		if def.BudgetToolCalls < tc.minBudget {
			t.Errorf("role %q: budget %d < minimum %d", tc.name, def.BudgetToolCalls, tc.minBudget)
		}
		if def.SystemPromptAddition == "" {
			t.Errorf("role %q: missing system_prompt_addition", tc.name)
		}
		if def.Description == "" {
			t.Errorf("role %q: missing description", tc.name)
		}
	}
}

func TestCatalogNonEmpty(t *testing.T) {
	reg, err := roles.Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	cat := reg.Catalog()
	if cat == "" {
		t.Fatal("catalog is empty")
	}
	// Should contain all 8 roles.
	if n := len(reg.Names()); n != 8 {
		t.Errorf("expected 8 roles, got %d", n)
	}
}
