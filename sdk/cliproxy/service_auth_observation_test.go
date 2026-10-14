package cliproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPrepareCoreAuth_OldFileObservationCannotOverwriteNewManagementSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		registered, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID: "observed-prefix", Provider: "codex", Prefix: "old", Status: coreauth.StatusActive,
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		observation := registered.Clone()
		observation.Generation = 0
		observation.RegistrationEpoch = 0
		// Advance virtual time, avoiding platform clock-resolution assumptions.
		time.Sleep(time.Second)
		observation.UpdatedAt = time.Now()
		time.Sleep(time.Second)
		newAuth := registered.Clone()
		newAuth.Prefix = "new"
		if _, errUpdate := manager.Update(context.Background(), newAuth); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		// The persisted management update is published, but its watcher hook has not
		// yet advanced the watcher revision when the older file scan is consumed.
		service := &Service{coreManager: manager}
		prepared := service.prepareCoreAuthForModelRegistration(coreauth.WithSkipPersist(context.Background()), observation)
		current, _ := manager.GetByID(registered.ID)
		if prepared == nil || prepared.Prefix != "new" || current.Prefix != "new" {
			t.Fatalf("old file observation overwrote management prefix: prepared=%v current=%s", prepared != nil, current.Prefix)
		}
	})
}

func TestPrepareCoreAuth_DelayedObservationsApplyInObservationOrder(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "delayed-observations", Provider: "codex", Prefix: "initial", Status: coreauth.StatusActive,
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	first := registered.Clone()
	first.Generation, first.RegistrationEpoch = 0, 0
	first.Prefix = "first"
	first.UpdatedAt = time.Now()
	second := first.Clone()
	second.Prefix = "second"
	second.UpdatedAt = first.UpdatedAt.Add(time.Nanosecond)
	service := &Service{coreManager: manager}
	ctx := coreauth.WithSkipPersist(context.Background())
	service.prepareCoreAuthForModelRegistration(ctx, first)
	service.prepareCoreAuthForModelRegistration(ctx, second)
	current, _ := manager.GetByID(registered.ID)
	if current.Prefix != "second" {
		t.Fatalf("newer delayed observation rejected: prefix=%s", current.Prefix)
	}
}
