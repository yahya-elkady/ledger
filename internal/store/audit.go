package store

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// AuditEntry is one row of the append-only audit log. It deliberately carries no
// PII — only ids, an action label, and the actor's network address.
type AuditEntry struct {
	MerchantID string // owning merchant (may be empty for system actions)
	ActorType  string // api_key | jwt | system
	ActorID    string // api key id or merchant id
	Action     string // e.g. "merchant.created", "apikey.revoked"
	Resource   string // table name, e.g. "merchants"
	ResourceID string // affected row id (may be empty)
	IP         string // client IP (may be empty)
}

// AuditStore appends rows to audit_logs. The table is INSERT-only for the app
// role; this store never updates or deletes audit history.
type AuditStore struct {
	queries *db.Queries
}

// NewAuditStore constructs an AuditStore from an open pool.
func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{queries: db.New(pool)}
}

// WriteAuditLog appends one audit entry. Invalid/empty optional ids and IPs are
// stored as SQL NULL rather than failing the write.
func (s *AuditStore) WriteAuditLog(ctx context.Context, e AuditEntry) error {
	_, err := s.queries.CreateAuditLog(ctx, db.CreateAuditLogParams{
		MerchantID: optionalUUID(e.MerchantID),
		ActorType:  e.ActorType,
		ActorID:    e.ActorID,
		Action:     e.Action,
		Resource:   e.Resource,
		ResourceID: optionalUUID(e.ResourceID),
		Diff:       nil, // no diff payload yet; never include PII here
		IpAddress:  optionalIP(e.IP),
	})
	if err != nil {
		return fmt.Errorf("writing audit log %q: %w", e.Action, err)
	}
	return nil
}

// optionalUUID parses s into a pgtype.UUID, returning a NULL UUID if s is empty
// or unparseable (audit logging must never block the primary operation).
func optionalUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	u, err := textToUUID(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

// optionalIP parses s into a *netip.Addr, returning nil (SQL NULL) if empty or
// invalid.
func optionalIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}
