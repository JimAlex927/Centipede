package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestInvitationUUIDsEncodeAsPostgresUUIDArray(t *testing.T) {
	ids := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
	}
	if _, err := pgtype.NewMap().Encode(
		pgtype.UUIDArrayOID,
		pgtype.BinaryFormatCode,
		ids,
		nil,
	); err == nil {
		t.Fatal("expected []string to be rejected for a PostgreSQL uuid[] parameter")
	}

	values, err := invitationUUIDs(ids)
	if err != nil {
		t.Fatalf("invitationUUIDs returned an error: %v", err)
	}

	if _, err := pgtype.NewMap().Encode(
		pgtype.UUIDArrayOID,
		pgtype.BinaryFormatCode,
		values,
		nil,
	); err != nil {
		t.Fatalf("UUID array could not be encoded for pgx: %v", err)
	}
}

func TestInvitationUUIDsRejectsInvalidValue(t *testing.T) {
	if _, err := invitationUUIDs([]string{"not-a-uuid"}); err != ErrInvalidInput {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}
