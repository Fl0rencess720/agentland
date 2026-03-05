package utils

import "testing"

func TestNameHash_IsStableAndStrongLength(t *testing.T) {
	t.Parallel()

	got := NameHash("session-abc")
	if len(got) != 32 {
		t.Fatalf("NameHash length = %d, want 32", len(got))
	}
	if got != NameHash("session-abc") {
		t.Fatalf("NameHash must be deterministic")
	}
	if got == NameHash("session-abd") {
		t.Fatalf("different inputs should not produce same hash in this test case")
	}
}
