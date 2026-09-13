package identifier

import "testing"

func TestNewReturnsUUIDv7(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(id) {
		t.Fatalf("invalid UUID %q", id)
	}
	if id[14] != '7' {
		t.Fatalf("UUID version = %q, want 7", id[14])
	}
	if id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b' {
		t.Fatalf("invalid RFC 4122 variant in %q", id)
	}
}

func TestValidRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-00000000000z"} {
		if Valid(value) {
			t.Fatalf("Valid(%q) = true", value)
		}
	}
}

func TestValidV7(t *testing.T) {
	value, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidV7(value) {
		t.Fatalf("generated UUIDv7 rejected: %s", value)
	}
	if ValidV7("550e8400-e29b-41d4-a716-446655440000") {
		t.Fatal("UUIDv4 accepted as UUIDv7")
	}
	if ValidV7("018f6f4c-38f8-7f1f-1f47-5aa4e1c2a311") {
		t.Fatal("UUID with invalid RFC variant accepted")
	}
}
