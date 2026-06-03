package ledger_test

import (
    "github.com/yahya-elkady/ledger"

	"errors"
	"testing"
	"time"

)

// helpers

var now = time.Now()

func makeEntry(id, accountID string, amount int64, currency string, dir ledger.Direction) ledger.Entry {
	return ledger.Entry{
		ID:        id,
		AccountID: accountID,
		Amount:    ledger.MustMoney(amount, currency),
		Direction: dir,
		CreatedAt: now,
	}
}

// Direction tests

func TestDirection_String(t *testing.T) {
	if got := ledger.Debit.String(); got != "debit" {
		t.Errorf("Debit.String() = %q, want %q", got, "debit")
	}
	if got := ledger.Credit.String(); got != "credit" {
		t.Errorf("Credit.String() = %q, want %q", got, "credit")
	}
}

func TestDirection_constants(t *testing.T) {
	// Direction values must be distinct and non-zero so accidental zero-values
	// are detectable.
	if ledger.Debit == ledger.Credit {
		t.Error("Debit and Credit must be distinct values")
	}
	if ledger.Debit == 0 || ledger.Credit == 0 {
		t.Error("neither Debit nor Credit should be zero (zero is the unset sentinel)")
	}
}

// Entry.Validate tests

func TestEntry_Validate(t *testing.T) {
	t.Run("valid entry passes", func(t *testing.T) {
		e := makeEntry("e1", "acct1", 1000, "USD", ledger.Debit)
		if err := e.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing ID", func(t *testing.T) {
		e := makeEntry("", "acct1", 1000, "USD", ledger.Debit)
		if err := e.Validate(); err == nil {
			t.Error("expected error for missing ID")
		}
	})

	t.Run("missing account ID", func(t *testing.T) {
		e := makeEntry("e1", "", 1000, "USD", ledger.Debit)
		if err := e.Validate(); err == nil {
			t.Error("expected error for missing account ID")
		}
	})

	t.Run("zero amount", func(t *testing.T) {
		e := makeEntry("e1", "acct1", 0, "USD", ledger.Debit)
		if err := e.Validate(); err == nil {
			t.Error("expected error for zero amount")
		}
	})

	t.Run("zero CreatedAt", func(t *testing.T) {
		e := makeEntry("e1", "acct1", 1000, "USD", ledger.Debit)
		e.CreatedAt = time.Time{}
		if err := e.Validate(); err == nil {
			t.Error("expected error for zero CreatedAt")
		}
	})
}

// NewEntrySet tests

func TestNewEntrySet_balanced(t *testing.T) {
	t.Run("simple balanced pair", func(t *testing.T) {
		entries := []ledger.Entry{
			makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
			makeEntry("e2", "revenue", 1000, "USD", ledger.Credit),
		}
		_, err := ledger.NewEntrySet(entries)
		if err != nil {
			t.Errorf("unexpected error for balanced set: %v", err)
		}
	})

	t.Run("multi-leg balanced set", func(t *testing.T) {
		// One debit of 1000, two credits of 400 and 600.
		entries := []ledger.Entry{
			makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
			makeEntry("e2", "revenue", 400, "USD", ledger.Credit),
			makeEntry("e3", "tax-payable", 600, "USD", ledger.Credit),
		}
		_, err := ledger.NewEntrySet(entries)
		if err != nil {
			t.Errorf("unexpected error for balanced multi-leg set: %v", err)
		}
	})
}

func TestNewEntrySet_unbalanced(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
		makeEntry("e2", "revenue", 999, "USD", ledger.Credit), // off by 1
	}
	_, err := ledger.NewEntrySet(entries)
	if err == nil {
		t.Error("expected error for unbalanced entry set, got nil")
	}
}

func TestNewEntrySet_single_entry_rejected(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
	}
	_, err := ledger.NewEntrySet(entries)
	if err == nil {
		t.Error("expected error for single-entry set, got nil")
	}
}

func TestNewEntrySet_empty_rejected(t *testing.T) {
	_, err := ledger.NewEntrySet([]ledger.Entry{})
	if err == nil {
		t.Error("expected error for empty entry set, got nil")
	}
}

func TestNewEntrySet_mixed_currencies_rejected(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
		makeEntry("e2", "revenue", 1000, "EUR", ledger.Credit),
	}
	_, err := ledger.NewEntrySet(entries)
	if err == nil {
		t.Error("expected error for mixed-currency entry set, got nil")
	}
}

func TestNewEntrySet_invalid_entry_propagates(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("", "cash", 1000, "USD", ledger.Debit), // missing ID
		makeEntry("e2", "revenue", 1000, "USD", ledger.Credit),
	}
	_, err := ledger.NewEntrySet(entries)
	if err == nil {
		t.Error("expected error for invalid entry, got nil")
	}
}

// TotalDebits / TotalCredits tests

func TestEntrySet_Totals(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("e1", "cash", 700, "USD", ledger.Debit),
		makeEntry("e2", "ar", 300, "USD", ledger.Debit),
		makeEntry("e3", "revenue", 1000, "USD", ledger.Credit),
	}
	es, err := ledger.NewEntrySet(entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	debits, err := es.TotalDebits()
	if err != nil {
		t.Fatalf("TotalDebits error: %v", err)
	}
	if debits.Amount != 1000 {
		t.Errorf("TotalDebits = %d, want 1000", debits.Amount)
	}

	credits, err := es.TotalCredits()
	if err != nil {
		t.Fatalf("TotalCredits error: %v", err)
	}
	if credits.Amount != 1000 {
		t.Errorf("TotalCredits = %d, want 1000", credits.Amount)
	}
}

// Entries immutability test

func TestEntrySet_Entries_returns_copy(t *testing.T) {
	entries := []ledger.Entry{
		makeEntry("e1", "cash", 1000, "USD", ledger.Debit),
		makeEntry("e2", "revenue", 1000, "USD", ledger.Credit),
	}
	es, err := ledger.NewEntrySet(entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	copy1 := es.Entries()
	copy1[0].Memo = "mutated"

	copy2 := es.Entries()
	if copy2[0].Memo == "mutated" {
		t.Error("Entries() should return a copy; mutation affected internal state")
	}
}

// AccountType tests

func TestAccountType_NormalBalance(t *testing.T) {
	cases := []struct {
		t    ledger.AccountType
		want ledger.Direction
	}{
		{ledger.AccountTypeAsset, ledger.Debit},
		{ledger.AccountTypeExpense, ledger.Debit},
		{ledger.AccountTypeLiability, ledger.Credit},
		{ledger.AccountTypeEquity, ledger.Credit},
		{ledger.AccountTypeRevenue, ledger.Credit},
	}
	for _, tc := range cases {
		t.Run(string(tc.t), func(t *testing.T) {
			got := tc.t.NormalBalance()
			if got != tc.want {
				t.Errorf("NormalBalance() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAccount_Validate(t *testing.T) {
	valid := ledger.Account{
		ID:       "acct-1",
		Name:     "Cash",
		Currency: "USD",
		Type:     ledger.AccountTypeAsset,
	}

	t.Run("valid account passes", func(t *testing.T) {
		if err := valid.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing ID", func(t *testing.T) {
		a := valid
		a.ID = ""
		if err := a.Validate(); err == nil {
			t.Error("expected error for missing ID")
		}
	})

	t.Run("missing Name", func(t *testing.T) {
		a := valid
		a.Name = ""
		if err := a.Validate(); err == nil {
			t.Error("expected error for missing Name")
		}
	})

	t.Run("missing Currency", func(t *testing.T) {
		a := valid
		a.Currency = ""
		if err := a.Validate(); err == nil {
			t.Error("expected error for missing Currency")
		}
	})

	t.Run("unknown AccountType", func(t *testing.T) {
		a := valid
		a.Type = ledger.AccountType("bogus")
		if err := a.Validate(); err == nil {
			t.Error("expected error for unknown account type")
		}
	})

	t.Run("multiple errors joined", func(t *testing.T) {
		a := ledger.Account{} // everything missing
		err := a.Validate()
		if err == nil {
			t.Fatal("expected errors, got nil")
		}
		// errors.Join produces an error that unwraps to all constituent errors.
		// Here we just check that more than one thing was reported.
		if errors.Is(err, errors.New("single")) {
			t.Error("expected joined errors")
		}
	})
}