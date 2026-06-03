package payments

import (
	"errors"
	"fmt"
	"time"
)

// Direction indicates whether an entry is a debit or a credit.
// These are typed constants rather than a bool so call-sites read clearly:
// entry.Direction == Debit, not entry.IsDebit == true.
type Direction int8

const (
	Debit  Direction = 1
	Credit Direction = -1
)

// String returns a human-readable direction label.
func (d Direction) String() string {
	switch d {
	case Debit:
		return "debit"
	case Credit:
		return "credit"
	default:
		return fmt.Sprintf("Direction(%d)", int(d))
	}
}

// Entry is one leg of a double-entry transaction.
//
// Amount is always positive; Direction carries the semantic sign.
// Entries are immutable once written — corrections are made by posting
// a reversing EntrySet, never by updating an existing entry.
type Entry struct {
	ID        string
	AccountID string
	Amount    Money     // always positive
	Direction Direction // Debit or Credit
	Memo      string    // optional human-readable description
	CreatedAt time.Time
}

// Validate returns an error if the entry is missing required fields or
// contains an invalid amount.
func (e Entry) Validate() error {
	var errs []error
	if e.ID == "" {
		errs = append(errs, errors.New("entry ID is required"))
	}
	if e.AccountID == "" {
		errs = append(errs, errors.New("entry account ID is required"))
	}
	if e.Amount.IsZero() {
		errs = append(errs, errors.New("entry amount must be non-zero"))
	}
	if e.Amount.Amount < 0 {
		// Belt-and-suspenders: Money should already prevent this,
		// but we check here too since Entry is the public boundary.
		errs = append(errs, ErrNegativeAmount)
	}
	if e.Direction != Debit && e.Direction != Credit {
		errs = append(errs, fmt.Errorf("unknown direction %v", e.Direction))
	}
	if e.CreatedAt.IsZero() {
		errs = append(errs, errors.New("entry created_at is required"))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// EntrySet is a proposed set of entries representing a single transaction.
// It enforces the double-entry invariant: the sum of all debit amounts must
// equal the sum of all credit amounts before the set may be persisted.
//
// EntrySet is the unit of atomicity — the repository writes all entries in
// a single database transaction or none of them.
type EntrySet struct {
	entries []Entry
}

// NewEntrySet constructs an EntrySet from a slice of entries and immediately
// validates each entry and the balance invariant.
func NewEntrySet(entries []Entry) (EntrySet, error) {
	if len(entries) < 2 {
		return EntrySet{}, errors.New("entry set must contain at least two entries")
	}

	// Validate individual entries first so balance errors aren't confused
	// with structural errors.
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return EntrySet{}, fmt.Errorf("entry[%d] invalid: %w", i, err)
		}
	}

	es := EntrySet{entries: entries}
	if err := es.validate(); err != nil {
		return EntrySet{}, err
	}
	return es, nil
}

// Entries returns a copy of the entries slice.
// Callers may not mutate the returned slice to preserve immutability.
func (es EntrySet) Entries() []Entry {
	out := make([]Entry, len(es.entries))
	copy(out, es.entries)
	return out
}

// TotalDebits returns the sum of all debit entry amounts.
// Returns a zero Money if there are no debit entries; currency is derived
// from the first debit entry found.
func (es EntrySet) TotalDebits() (Money, error) {
	return es.sumByDirection(Debit)
}

// TotalCredits returns the sum of all credit entry amounts.
func (es EntrySet) TotalCredits() (Money, error) {
	return es.sumByDirection(Credit)
}

// validate checks the balance invariant: total debits must equal total credits.
// Also asserts that all entries share the same currency — cross-currency entry
// sets are not supported here (currency conversion belongs in the payment layer).
func (es EntrySet) validate() error {
	// Assert single currency across all entries.
	currency := es.entries[0].Amount.Currency
	for i, e := range es.entries[1:] {
		if e.Amount.Currency != currency {
			return fmt.Errorf(
				"entry set has mixed currencies: entry[0]=%s entry[%d]=%s; cross-currency sets are not supported",
				currency, i+1, e.Amount.Currency,
			)
		}
	}

	debits, err := es.sumByDirection(Debit)
	if err != nil {
		return fmt.Errorf("summing debits: %w", err)
	}
	credits, err := es.sumByDirection(Credit)
	if err != nil {
		return fmt.Errorf("summing credits: %w", err)
	}

	if !debits.Equal(credits) {
		return fmt.Errorf(
			"unbalanced entry set: debits=%v credits=%v difference=%d %s",
			debits, credits,
			abs(debits.Amount-credits.Amount), currency,
		)
	}
	return nil
}

// sumByDirection sums all entry amounts with the given direction.
// Returns a zero-value Money in the entry set's currency if no entries
// match the direction.
func (es EntrySet) sumByDirection(dir Direction) (Money, error) {
	if len(es.entries) == 0 {
		return Money{}, nil
	}
	total := es.entries[0].Amount.Zero() // zero in the right currency
	for _, e := range es.entries {
		if e.Direction != dir {
			continue
		}
		var err error
		total, err = total.Add(e.Amount)
		if err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}