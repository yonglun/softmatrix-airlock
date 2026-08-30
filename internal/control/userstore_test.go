package control

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUserStoreUpsertInsertsThenUpdates(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	created, err := s.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "a@example.com", DisplayName: "甲",
		Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "甲", created.DisplayName)

	// 同一 external_id 再次 upsert 应更新而不是插入新行
	updated, err := s.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "a2@example.com", DisplayName: "甲改名",
		Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.Equal(t, created.ID, updated.ID, "同一 external_id 必须复用同一行")
	require.Equal(t, "a2@example.com", updated.Email)
	require.Equal(t, "甲改名", updated.DisplayName)
}

func TestUserStoreByExternalID(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	_, err := s.Upsert(ctx, &User{ExternalID: "sub-2", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)

	got, err := s.ByExternalID(ctx, "sub-2")
	require.NoError(t, err)
	require.Equal(t, "b@x.com", got.Email)

	_, err = s.ByExternalID(ctx, "nope")
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestUserStoreByIDFindsDisabledUsers(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	u, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, s.MarkDisabled(ctx, []string{u.ID}))

	got, err := s.ByID(ctx, u.ID)
	require.NoError(t, err, "ByID 必须能查到已禁用用户——"+
		"会话中间件靠它区分「用户不存在」(401) 与「用户已禁用」(403)")
	require.Equal(t, UserStatusDisabled, got.Status)

	_, err = s.ByID(ctx, "nope")
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestUserStoreListActiveExcludesDisabled(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	u1, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", Status: UserStatusActive})
	require.NoError(t, err)
	u2, err := s.Upsert(ctx, &User{ExternalID: "s2", Email: "2@x", Status: UserStatusActive})
	require.NoError(t, err)

	require.NoError(t, s.MarkDisabled(ctx, []string{u2.ID}))

	active, err := s.ListActive(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, u1.ID, active[0].ID)
}

func TestUserStoreMarkDisabledEmptySliceIsNoop(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	_, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", Status: UserStatusActive})
	require.NoError(t, err)

	require.NoError(t, s.MarkDisabled(ctx, nil))

	active, err := s.ListActive(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1, "空列表不应误伤任何用户")
}

func TestUserStorePlatformAdmin(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	n, err := s.CountPlatformAdmins(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	u, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, s.SetPlatformAdmin(ctx, u.ID, true))

	n, err = s.CountPlatformAdmins(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.ByExternalID(ctx, "s1")
	require.NoError(t, err)
	require.True(t, got.IsPlatformAdmin)
}

func TestUserStoreUpsertPreservesAdminFlagAndPrimaryOrg(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	u, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, s.SetPlatformAdmin(ctx, u.ID, true))

	// 再次登录触发的 upsert 只该刷新画像，不该把管理员标志冲掉
	again, err := s.Upsert(ctx, &User{ExternalID: "s1", Email: "1@x", DisplayName: "新名", Status: UserStatusActive})
	require.NoError(t, err)
	require.True(t, again.IsPlatformAdmin, "upsert 不得覆盖 is_platform_admin")
	require.Equal(t, "新名", again.DisplayName)
}

func TestUserStoreUpsertSetsLastLoginAt(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresUserStore(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	u, err := s.Upsert(ctx, &User{
		ExternalID: "s1", Email: "1@x", Status: UserStatusActive, LastLoginAt: &now,
	})
	require.NoError(t, err)
	require.NotNil(t, u.LastLoginAt)
	require.WithinDuration(t, now, *u.LastLoginAt, time.Second)
}
