package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
)

func TestTemporaryEnrollmentPINClaimsWithNodeTransaction(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "temporary-pin.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	pin := store.TemporaryEnrollmentPIN{
		ID: uuid.NewString(), PINHash: "argon-hash", CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := repository.ReplaceTemporaryEnrollmentPIN(ctx, pin, now); err != nil {
		t.Fatal(err)
	}
	active, err := repository.ActiveTemporaryEnrollmentPIN(ctx, now)
	if err != nil || active.ID != pin.ID || active.UsedAt != nil {
		t.Fatalf("temporary PIN was not active: %#v %v", active, err)
	}
	seedNodeID := uuid.NewString()
	if _, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Metadata: model.EnrollmentMetadata{Name: "existing-node"},
			Config: model.DefaultAgentConfig(),
		},
		NodeID: seedNodeID, TokenID: "existing-token", TokenHash: "existing-hash", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	params := store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Metadata: model.EnrollmentMetadata{Name: "temporary-pin-node"},
			Config: model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "temporary-token", TokenHash: "temporary-token-hash",
		TemporaryPINID: pin.ID, Now: now.Add(time.Minute),
	}
	failed := params
	failed.NodeID = seedNodeID
	if _, err := repository.EnrollNode(ctx, failed); err == nil {
		t.Fatal("node insert failure was unexpectedly accepted")
	}
	afterFailure, err := repository.LatestTemporaryEnrollmentPIN(ctx)
	if err != nil || afterFailure.UsedAt != nil || afterFailure.ClaimedInstallID != "" {
		t.Fatalf("failed node transaction consumed the temporary PIN: %#v %v", afterFailure, err)
	}
	enrolled, err := repository.EnrollNode(ctx, params)
	if err != nil || !enrolled.Created {
		t.Fatalf("temporary PIN enrollment failed: %#v %v", enrolled, err)
	}
	used, err := repository.LatestTemporaryEnrollmentPIN(ctx)
	if err != nil || used.UsedAt == nil || used.ClaimedInstallID != params.Request.InstallID || used.ClaimedTokenID != params.TokenID {
		t.Fatalf("temporary PIN claim was not committed with the node: %#v %v", used, err)
	}
	retry, err := repository.EnrollNode(ctx, params)
	if err != nil || retry.Created || retry.Node.ID != enrolled.Node.ID {
		t.Fatalf("idempotent temporary PIN retry failed: %#v %v", retry, err)
	}
	second := params
	second.Request.InstallID = uuid.NewString()
	second.NodeID = uuid.NewString()
	second.TokenID = "other-token"
	second.TokenHash = "other-token-hash"
	if _, err := repository.EnrollNode(ctx, second); !errors.Is(err, store.ErrTemporaryPINUnavailable) {
		t.Fatalf("used temporary PIN was accepted by a second install: %v", err)
	}
	if _, err := repository.GetNode(ctx, second.NodeID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed temporary PIN enrollment left a node behind: %v", err)
	}
	if err := repository.RevokeTemporaryEnrollmentPIN(ctx, pin.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActiveTemporaryEnrollmentPIN(ctx, now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked temporary PIN remained active: %v", err)
	}
}

func TestReplacingTemporaryEnrollmentPINRevokesPreviousValue(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "replace-temporary-pin.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	first := store.TemporaryEnrollmentPIN{ID: uuid.NewString(), PINHash: "first", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	second := store.TemporaryEnrollmentPIN{ID: uuid.NewString(), PINHash: "second", CreatedAt: now.Add(time.Second), ExpiresAt: now.Add(time.Hour)}
	if err := repository.ReplaceTemporaryEnrollmentPIN(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReplaceTemporaryEnrollmentPIN(ctx, second, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	active, err := repository.ActiveTemporaryEnrollmentPIN(ctx, now.Add(2*time.Second))
	if err != nil || active.ID != second.ID {
		t.Fatalf("replacement temporary PIN is not active: %#v %v", active, err)
	}
	var revokedAt any
	if err := repository.db.QueryRowContext(ctx, `SELECT revoked_at FROM temporary_enrollment_pins WHERE id = ?`, first.ID).Scan(&revokedAt); err != nil || revokedAt == nil {
		t.Fatalf("previous temporary PIN was not revoked: %v %#v", err, revokedAt)
	}
}
