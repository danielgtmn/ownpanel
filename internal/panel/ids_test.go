package panel

import "testing"

func TestStoreID(t *testing.T) {
	if n, err := storeID("42", "redirect"); err != nil || n != 42 {
		t.Fatalf("storeID(42) = %d, %v", n, err)
	}
	for _, bad := range []string{"", "0", "-1", "042", "+42", " 42", "4.2", "abc", "9223372036854775808"} {
		_, err := storeID(bad, "redirect")
		if KindOf(err) != NotFound || err.Error() != "redirect not found" {
			t.Errorf("storeID(%q) = %v, want redirect not found", bad, err)
		}
	}
	if idString(42) != "42" || idString(0) != "" {
		t.Fatalf("idString: %q %q", idString(42), idString(0))
	}
}
