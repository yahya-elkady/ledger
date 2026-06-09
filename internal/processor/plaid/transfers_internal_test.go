package plaid

import "testing"

func TestSplitToken(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		at, acct, err := splitToken("access-sandbox-123|acc_456")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if at != "access-sandbox-123" || acct != "acc_456" {
			t.Errorf("got (%q,%q), want (access-sandbox-123, acc_456)", at, acct)
		}
	})
	for _, bad := range []string{"", "noseparator", "|missing", "missing|"} {
		t.Run("invalid_"+bad, func(t *testing.T) {
			if _, _, err := splitToken(bad); err == nil {
				t.Errorf("splitToken(%q) should error", bad)
			}
		})
	}
}

func TestFormatAmount(t *testing.T) {
	cases := map[int64]string{1099: "10.99", 5: "0.05", 100: "1.00", 0: "0.00", 123456: "1234.56"}
	for minor, want := range cases {
		if got := formatAmount(minor); got != want {
			t.Errorf("formatAmount(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestModeSelectionRequiresCredentials(t *testing.T) {
	// A client with no configured environments fails fast with an auth error
	// (no network call is attempted).
	c := New(Config{})
	if _, err := c.forMode("live"); err == nil {
		t.Error("expected auth error for unconfigured live mode")
	}
	if _, err := c.forMode("test"); err == nil {
		t.Error("expected auth error for unconfigured sandbox mode")
	}
}
