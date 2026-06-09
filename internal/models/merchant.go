// Package models holds the domain/response shapes returned by the stores and
// rendered by the API. These structs deliberately omit secret material
// (password hashes, key hashes) so they are always safe to serialize to clients.
package models

import "time"

// Merchant is the public view of a merchant account. The bcrypt password hash
// is never part of this struct — it stays inside the store and is only used for
// login verification.
type Merchant struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	BusinessName string    `json:"business_name"`
	Mode         string    `json:"mode"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
