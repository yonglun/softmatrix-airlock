package control

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedUser(t *testing.T, s UserStore, externalID string) *User {
	t.Helper()
	u, err := s.Upsert(context.Background(), &User{
		ExternalID: externalID, Email: externalID + "@x.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	return u
}

func TestSessionStoreCreateAndGet(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	users := NewPostgresUserStore(db)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()

	u := seedUser(t, users, "s1")
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	require.NoError(t, store.Create(ctx, Session{
		ID: "hash-1", UserID: u.ID, ExpiresAt: exp,
		LastSeenAt: time.Now().UTC(), IP: "1.2.3.4", UserAgent: "curl",
	}))

	got, err := store.Get(ctx, "hash-1")
	require.NoError(t, err)
	require.Equal(t, u.ID, got.UserID)
	require.Equal(t, "1.2.3.4", got.IP)
	require.WithinDuration(t, exp, got.ExpiresAt, time.Second)
}

func TestSessionStoreGetRejectsExpired(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	users := NewPostgresUserStore(db)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()

	u := seedUser(t, users, "s1")
	require.NoError(t, store.Create(ctx, Session{
		ID: "expired", UserID: u.ID,
		ExpiresAt:  time.Now().Add(-time.Minute).UTC(),
		LastSeenAt: time.Now().UTC(),
	}))

	_, err := store.Get(ctx, "expired")
	require.ErrorIs(t, err, ErrSessionNotFound, "过期会话必须查不到，而不是查到后由调用方判断")
}

func TestSessionStoreGetUnknown(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	store := NewPostgresSessionStore(db)

	_, err := store.Get(context.Background(), "nope")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStoreDeleteByUserKicksAllSessions(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	users := NewPostgresUserStore(db)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()

	u := seedUser(t, users, "s1")
	exp := time.Now().Add(time.Hour).UTC()
	for _, id := range []string{"h1", "h2", "h3"} {
		require.NoError(t, store.Create(ctx, Session{
			ID: id, UserID: u.ID, ExpiresAt: exp, LastSeenAt: time.Now().UTC(),
		}))
	}

	n, err := store.DeleteByUser(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	_, err = store.Get(ctx, "h1")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStoreTouchUpdatesLastSeen(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	users := NewPostgresUserStore(db)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()

	u := seedUser(t, users, "s1")
	old := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.Create(ctx, Session{
		ID: "h1", UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Hour).UTC(), LastSeenAt: old,
	}))

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.Touch(ctx, "h1", now))

	got, err := store.Get(ctx, "h1")
	require.NoError(t, err)
	require.WithinDuration(t, now, got.LastSeenAt, time.Second)
}

func TestSessionStoreDeleteExpired(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	users := NewPostgresUserStore(db)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()

	u := seedUser(t, users, "s1")
	require.NoError(t, store.Create(ctx, Session{
		ID: "old", UserID: u.ID,
		ExpiresAt: time.Now().Add(-time.Hour).UTC(), LastSeenAt: time.Now().UTC(),
	}))
	require.NoError(t, store.Create(ctx, Session{
		ID: "fresh", UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Hour).UTC(), LastSeenAt: time.Now().UTC(),
	}))

	n, err := store.DeleteExpired(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	_, err = store.Get(ctx, "fresh")
	require.NoError(t, err, "未过期的会话不该被清理")
}

func TestLoginStateTakeIsOneShot(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	store := NewPostgresLoginStateStore(db)
	ctx := context.Background()

	require.NoError(t, store.Create(ctx, LoginState{
		ID: "ls-1", State: "st", PKCEVerifier: "v", RedirectTo: "/orgs",
		ExpiresAt: time.Now().Add(10 * time.Minute).UTC(),
	}))

	got, err := store.Take(ctx, "ls-1")
	require.NoError(t, err)
	require.Equal(t, "st", got.State)
	require.Equal(t, "v", got.PKCEVerifier)
	require.Equal(t, "/orgs", got.RedirectTo)

	_, err = store.Take(ctx, "ls-1")
	require.ErrorIs(t, err, ErrLoginStateNotFound, "登录状态是一次性的，重放必须失败")
}

func TestLoginStateTakeRejectsExpired(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	store := NewPostgresLoginStateStore(db)
	ctx := context.Background()

	require.NoError(t, store.Create(ctx, LoginState{
		ID: "ls-old", State: "st", PKCEVerifier: "v",
		ExpiresAt: time.Now().Add(-time.Minute).UTC(),
	}))

	_, err := store.Take(ctx, "ls-old")
	require.ErrorIs(t, err, ErrLoginStateNotFound)
}
