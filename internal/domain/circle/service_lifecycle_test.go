package circle_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/moistello/backend/internal/domain/circle"
	circleMocks "github.com/moistello/backend/internal/domain/circle/mocks"
	"github.com/moistello/backend/pkg/apperrors"
)

// recordingBroadcaster records the broadcasts the service emits, so a test can
// assert both that an event was sent and that it was not.
type recordingBroadcaster struct {
	statusChanges [][2]string
	membersLeft   [][2]string
}

func (b *recordingBroadcaster) CircleCreated(context.Context, string, string) {}

func (b *recordingBroadcaster) CircleStatusChanged(_ context.Context, circleID, status string) {
	b.statusChanges = append(b.statusChanges, [2]string{circleID, status})
}

func (b *recordingBroadcaster) MemberJoined(context.Context, string, string) {}

func (b *recordingBroadcaster) MemberLeft(_ context.Context, circleID, userID string) {
	b.membersLeft = append(b.membersLeft, [2]string{circleID, userID})
}

func (b *recordingBroadcaster) ContributionRecorded(context.Context, string, string, int, float64) {}

func (b *recordingBroadcaster) MemberPenalized(context.Context, string, string, int, float64) {}

type lifecycleFixture struct {
	repo        *circleMocks.Repository
	broadcaster *recordingBroadcaster
	svc         circle.Service
	circleID    uuid.UUID
	organizerID uuid.UUID
}

func newLifecycleFixture() *lifecycleFixture {
	repo := new(circleMocks.Repository)
	broadcaster := &recordingBroadcaster{}
	return &lifecycleFixture{
		repo:        repo,
		broadcaster: broadcaster,
		svc:         circle.NewService(repo, nil, circle.Dependencies{Broadcaster: broadcaster}),
		circleID:    uuid.New(),
		organizerID: uuid.New(),
	}
}

func (f *lifecycleFixture) newCircle(status circle.CircleStatus) *circle.Circle {
	return &circle.Circle{
		ID:                 f.circleID,
		Name:               "Savers",
		OrganizerID:        f.organizerID,
		Status:             status,
		PayoutType:         circle.PayoutTypeRandom,
		ContributionAmount: 100,
		Currency:           circle.CurrencyUSDC,
		Frequency:          circle.FrequencyWeekly,
		MaxMembers:         10,
		MaxStrikes:         3,
	}
}

func (f *lifecycleFixture) expectFindCircle(c *circle.Circle) {
	f.repo.On("FindByID", mock.Anything, f.circleID).Return(c, nil)
}

func lifecyclePtr[T any](v T) *T { return &v }

var errLifecycleRepo = errors.New("database unavailable")

// ── Update ──

func TestCircleService_Update_AppliesEveryProvidedField(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(nil)

	got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{
		Name:               lifecyclePtr("Renamed"),
		Description:        lifecyclePtr("Monthly savings"),
		ContributionAmount: lifecyclePtr(250.0),
		Currency:           lifecyclePtr(circle.CurrencyXLM),
		Frequency:          lifecyclePtr(circle.FrequencyMonthly),
		MaxMembers:         lifecyclePtr(20),
		MinMoiScore:        lifecyclePtr(300),
		CollateralPercent:  lifecyclePtr(10.0),
		LateFeePercent:     lifecyclePtr(5.0),
		GracePeriodHours:   lifecyclePtr(48),
		MaxStrikes:         lifecyclePtr(5),
	})

	require.NoError(t, err)
	assert.Same(t, c, got)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, sql.NullString{String: "Monthly savings", Valid: true}, got.Description)
	assert.Equal(t, 250.0, got.ContributionAmount)
	assert.Equal(t, circle.CurrencyXLM, got.Currency)
	assert.Equal(t, circle.FrequencyMonthly, got.Frequency)
	assert.Equal(t, 20, got.MaxMembers)
	assert.Equal(t, 300, got.MinMoiScore)
	assert.Equal(t, 10.0, got.CollateralPercent)
	assert.Equal(t, 5.0, got.LateFeePercent)
	assert.Equal(t, 48, got.GracePeriodHours)
	assert.Equal(t, 5, got.MaxStrikes)
	assert.False(t, got.UpdatedAt.IsZero(), "UpdatedAt must be stamped")
	f.repo.AssertExpectations(t)
}

func TestCircleService_Update_LeavesUnprovidedFieldsAlone(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusActive)
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(nil)

	got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{
		Name: lifecyclePtr("Only the name"),
	})

	require.NoError(t, err)
	assert.Equal(t, "Only the name", got.Name)
	assert.Equal(t, 100.0, got.ContributionAmount)
	assert.Equal(t, circle.CurrencyUSDC, got.Currency)
	assert.Equal(t, 10, got.MaxMembers)
	assert.Equal(t, 3, got.MaxStrikes)
}

func TestCircleService_Update_EmptyDescriptionClearsIt(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	c.Description = sql.NullString{String: "old", Valid: true}
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(nil)

	got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{
		Description: lifecyclePtr(""),
	})

	require.NoError(t, err)
	assert.False(t, got.Description.Valid)
}

func TestCircleService_Update_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()

	_, err := f.svc.Update(context.Background(), "not-a-uuid", f.organizerID.String(), circle.UpdateCircleInput{})
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	_, err = f.svc.Update(context.Background(), f.circleID.String(), "not-a-uuid", circle.UpdateCircleInput{})
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	f.repo.AssertNotCalled(t, "FindByID", mock.Anything, mock.Anything)
}

func TestCircleService_Update_WrapsALookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.repo.On("FindByID", mock.Anything, f.circleID).Return(nil, errLifecycleRepo)

	got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{})

	assert.Nil(t, got)
	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "finding circle for update")
}

func TestCircleService_Update_OnlyTheOrganizerMayUpdate(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusPending))

	got, err := f.svc.Update(context.Background(), f.circleID.String(), uuid.NewString(), circle.UpdateCircleInput{
		Name: lifecyclePtr("hijacked"),
	})

	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrNotOrganizer)
	f.repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}

func TestCircleService_Update_RejectsFinishedCircles(t *testing.T) {
	for _, status := range []circle.CircleStatus{circle.CircleStatusCompleted, circle.CircleStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			f := newLifecycleFixture()
			f.expectFindCircle(f.newCircle(status))

			got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{
				Name: lifecyclePtr("too late"),
			})

			assert.Nil(t, got)
			assert.ErrorIs(t, err, circle.ErrCircleNotActive)
			f.repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_Update_WrapsAPersistFailure(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(errLifecycleRepo)

	got, err := f.svc.Update(context.Background(), f.circleID.String(), f.organizerID.String(), circle.UpdateCircleInput{
		Name: lifecyclePtr("renamed"),
	})

	assert.Nil(t, got)
	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "updating circle")
}

// ── Start ──

func TestCircleService_Start_ActivatesTheCircleSnapshotsRoundOneAndBroadcasts(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("GetMemberCount", mock.Anything, f.circleID).Return(3, nil)
	f.repo.On("Update", mock.Anything, c).Return(nil)
	var snapshot *circle.RoundConfigSnapshot
	f.repo.On("SaveRoundConfigSnapshot", mock.Anything, mock.AnythingOfType("*circle.RoundConfigSnapshot")).
		Run(func(args mock.Arguments) { snapshot = args.Get(1).(*circle.RoundConfigSnapshot) }).
		Return(nil)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	require.NoError(t, err)
	assert.Equal(t, circle.CircleStatusActive, c.Status)
	assert.Equal(t, 1, c.CurrentRound)
	assert.False(t, c.UpdatedAt.IsZero())
	assert.Equal(t, [][2]string{{f.circleID.String(), "active"}}, f.broadcaster.statusChanges)

	require.NotNil(t, snapshot, "round one's configuration must be snapshotted")
	assert.Equal(t, f.circleID, snapshot.CircleID)
	assert.Equal(t, 1, snapshot.RoundNumber)
	assert.Contains(t, snapshot.ConfigJSON, `"name":"Savers"`)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(snapshot.ConfigJSON))), snapshot.ConfigHash)
	f.repo.AssertExpectations(t)
}

func TestCircleService_Start_SnapshotFailureDoesNotBlockStarting(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("GetMemberCount", mock.Anything, f.circleID).Return(2, nil)
	f.repo.On("Update", mock.Anything, c).Return(nil)
	f.repo.On("SaveRoundConfigSnapshot", mock.Anything, mock.Anything).Return(errLifecycleRepo)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	require.NoError(t, err)
	assert.Equal(t, circle.CircleStatusActive, c.Status)
	assert.Len(t, f.broadcaster.statusChanges, 1)
}

func TestCircleService_Start_WorksWithoutABroadcaster(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	f := &lifecycleFixture{circleID: uuid.New(), organizerID: uuid.New()}
	c := f.newCircle(circle.CircleStatusPending)
	repo.On("FindByID", mock.Anything, f.circleID).Return(c, nil)
	repo.On("GetMemberCount", mock.Anything, f.circleID).Return(2, nil)
	repo.On("Update", mock.Anything, c).Return(nil)
	repo.On("SaveRoundConfigSnapshot", mock.Anything, mock.Anything).Return(nil)

	assert.NoError(t, svc.Start(context.Background(), f.circleID.String(), f.organizerID.String()))
}

func TestCircleService_Start_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()

	assert.ErrorIs(t, f.svc.Start(context.Background(), "not-a-uuid", f.organizerID.String()), circle.ErrInvalidUUID)
	assert.ErrorIs(t, f.svc.Start(context.Background(), f.circleID.String(), "not-a-uuid"), circle.ErrInvalidUUID)
}

func TestCircleService_Start_WrapsALookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.repo.On("FindByID", mock.Anything, f.circleID).Return(nil, errLifecycleRepo)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "finding circle for start")
}

func TestCircleService_Start_OnlyTheOrganizerMayStart(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusPending))

	err := f.svc.Start(context.Background(), f.circleID.String(), uuid.NewString())

	assert.ErrorIs(t, err, circle.ErrNotOrganizer)
	assert.Empty(t, f.broadcaster.statusChanges)
}

func TestCircleService_Start_OnlyAPendingCircleCanStart(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, circle.ErrCircleNotActive)
	f.repo.AssertNotCalled(t, "GetMemberCount", mock.Anything, mock.Anything)
}

func TestCircleService_Start_WrapsAMemberCountFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusPending))
	f.repo.On("GetMemberCount", mock.Anything, f.circleID).Return(0, errLifecycleRepo)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "checking member count")
}

func TestCircleService_Start_NeedsAtLeastTwoMembers(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("GetMemberCount", mock.Anything, f.circleID).Return(1, nil)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.EqualError(t, err, "need at least 2 members to start")
	assert.Equal(t, circle.CircleStatusPending, c.Status, "a circle that cannot start must stay pending")
	f.repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}

func TestCircleService_Start_WrapsAPersistFailureWithoutBroadcastingOrSnapshotting(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusPending)
	f.expectFindCircle(c)
	f.repo.On("GetMemberCount", mock.Anything, f.circleID).Return(3, nil)
	f.repo.On("Update", mock.Anything, c).Return(errLifecycleRepo)

	err := f.svc.Start(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "activating circle")
	assert.Empty(t, f.broadcaster.statusChanges)
	f.repo.AssertNotCalled(t, "SaveRoundConfigSnapshot", mock.Anything, mock.Anything)
}

// ── Close ──

func TestCircleService_Close_CompletesAnActiveCircleAndBroadcasts(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusActive)
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(nil)

	err := f.svc.Close(context.Background(), f.circleID.String(), f.organizerID.String())

	require.NoError(t, err)
	assert.Equal(t, circle.CircleStatusCompleted, c.Status)
	assert.False(t, c.UpdatedAt.IsZero())
	assert.Equal(t, [][2]string{{f.circleID.String(), string(circle.CircleStatusCompleted)}}, f.broadcaster.statusChanges)
	f.repo.AssertExpectations(t)
}

func TestCircleService_Close_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()

	assert.ErrorIs(t, f.svc.Close(context.Background(), "not-a-uuid", f.organizerID.String()), circle.ErrInvalidUUID)
	assert.ErrorIs(t, f.svc.Close(context.Background(), f.circleID.String(), "not-a-uuid"), circle.ErrInvalidUUID)
}

func TestCircleService_Close_WrapsALookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.repo.On("FindByID", mock.Anything, f.circleID).Return(nil, errLifecycleRepo)

	err := f.svc.Close(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "finding circle for close")
}

func TestCircleService_Close_OnlyTheOrganizerMayClose(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))

	err := f.svc.Close(context.Background(), f.circleID.String(), uuid.NewString())

	assert.ErrorIs(t, err, circle.ErrNotOrganizer)
	assert.Empty(t, f.broadcaster.statusChanges)
}

func TestCircleService_Close_OnlyAnActiveCircleCanBeClosed(t *testing.T) {
	for _, status := range []circle.CircleStatus{circle.CircleStatusPending, circle.CircleStatusCompleted, circle.CircleStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			f := newLifecycleFixture()
			f.expectFindCircle(f.newCircle(status))

			err := f.svc.Close(context.Background(), f.circleID.String(), f.organizerID.String())

			assert.ErrorIs(t, err, circle.ErrCircleNotActive)
			f.repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_Close_WrapsAPersistFailureWithoutBroadcasting(t *testing.T) {
	f := newLifecycleFixture()
	c := f.newCircle(circle.CircleStatusActive)
	f.expectFindCircle(c)
	f.repo.On("Update", mock.Anything, c).Return(errLifecycleRepo)

	err := f.svc.Close(context.Background(), f.circleID.String(), f.organizerID.String())

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "closing circle")
	assert.Empty(t, f.broadcaster.statusChanges)
}

// ── IsMember ──

func TestCircleService_IsMember_OnlyActiveMembersCount(t *testing.T) {
	tests := []struct {
		name   string
		status circle.MemberStatus
		want   bool
	}{
		{"active member", circle.MemberStatusActive, true},
		{"exited member", circle.MemberStatusExited, false},
		{"removed member", circle.MemberStatusRemoved, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture()
			userID := uuid.New()
			f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, userID).
				Return(&circle.CircleMember{CircleID: f.circleID, UserID: userID, Status: tt.status}, nil)

			got, err := f.svc.IsMember(context.Background(), f.circleID.String(), userID.String())

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCircleService_IsMember_UnknownUserIsNotAMemberAndNotAnError(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, userID).Return(nil, apperrors.ErrNotFound)

	got, err := f.svc.IsMember(context.Background(), f.circleID.String(), userID.String())

	assert.NoError(t, err)
	assert.False(t, got)
}

func TestCircleService_IsMember_NilMemberIsNotAMember(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, userID).Return(nil, nil)

	got, err := f.svc.IsMember(context.Background(), f.circleID.String(), userID.String())

	assert.NoError(t, err)
	assert.False(t, got)
}

func TestCircleService_IsMember_WrapsAnUnexpectedLookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, userID).Return(nil, errLifecycleRepo)

	got, err := f.svc.IsMember(context.Background(), f.circleID.String(), userID.String())

	assert.False(t, got)
	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "checking circle membership")
}

func TestCircleService_IsMember_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()

	got, err := f.svc.IsMember(context.Background(), "not-a-uuid", uuid.NewString())
	assert.False(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	got, err = f.svc.IsMember(context.Background(), f.circleID.String(), "not-a-uuid")
	assert.False(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)
}

// ── RemoveMember ──

func TestCircleService_RemoveMember_OrganizerRemovesAnActiveMemberAndBroadcasts(t *testing.T) {
	f := newLifecycleFixture()
	memberID := uuid.New()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, memberID).
		Return(&circle.CircleMember{CircleID: f.circleID, UserID: memberID, Status: circle.MemberStatusActive}, nil)
	f.repo.On("UpdateMemberStatus", mock.Anything, f.circleID, memberID, circle.MemberStatusRemoved).Return(nil)

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), memberID.String(), "missed three rounds")

	require.NoError(t, err)
	assert.Equal(t, [][2]string{{f.circleID.String(), memberID.String()}}, f.broadcaster.membersLeft)
	f.repo.AssertExpectations(t)
}

func TestCircleService_RemoveMember_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()
	circleID, callerID, memberID := f.circleID.String(), f.organizerID.String(), uuid.NewString()

	assert.ErrorIs(t, f.svc.RemoveMember(context.Background(), "not-a-uuid", callerID, memberID, ""), circle.ErrInvalidUUID)
	assert.ErrorIs(t, f.svc.RemoveMember(context.Background(), circleID, "not-a-uuid", memberID, ""), circle.ErrInvalidUUID)
	assert.ErrorIs(t, f.svc.RemoveMember(context.Background(), circleID, callerID, "not-a-uuid", ""), circle.ErrInvalidUUID)
	f.repo.AssertNotCalled(t, "FindByID", mock.Anything, mock.Anything)
}

func TestCircleService_RemoveMember_WrapsACircleLookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	f.repo.On("FindByID", mock.Anything, f.circleID).Return(nil, errLifecycleRepo)

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), uuid.NewString(), "")

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "finding circle")
}

func TestCircleService_RemoveMember_OnlyTheOrganizerMayRemove(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), uuid.NewString(), uuid.NewString(), "")

	assert.ErrorIs(t, err, circle.ErrNotOrganizer)
	f.repo.AssertNotCalled(t, "UpdateMemberStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCircleService_RemoveMember_OrganizerCannotRemoveThemselves(t *testing.T) {
	f := newLifecycleFixture()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), f.organizerID.String(), "")

	assert.EqualError(t, err, "organizer cannot remove themselves; use cancel or close")
	f.repo.AssertNotCalled(t, "UpdateMemberStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCircleService_RemoveMember_UnknownMemberIsNotAMember(t *testing.T) {
	f := newLifecycleFixture()
	memberID := uuid.New()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, memberID).Return(nil, apperrors.ErrNotFound)

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), memberID.String(), "")

	assert.ErrorIs(t, err, circle.ErrNotMember)
	assert.Empty(t, f.broadcaster.membersLeft)
}

func TestCircleService_RemoveMember_WrapsAMemberLookupFailure(t *testing.T) {
	f := newLifecycleFixture()
	memberID := uuid.New()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, memberID).Return(nil, errLifecycleRepo)

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), memberID.String(), "")

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "finding member")
}

func TestCircleService_RemoveMember_AMemberWhoAlreadyLeftIsNotAMember(t *testing.T) {
	for _, status := range []circle.MemberStatus{circle.MemberStatusExited, circle.MemberStatusRemoved} {
		t.Run(string(status), func(t *testing.T) {
			f := newLifecycleFixture()
			memberID := uuid.New()
			f.expectFindCircle(f.newCircle(circle.CircleStatusActive))
			f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, memberID).
				Return(&circle.CircleMember{CircleID: f.circleID, UserID: memberID, Status: status}, nil)

			err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), memberID.String(), "")

			assert.ErrorIs(t, err, circle.ErrNotMember)
			f.repo.AssertNotCalled(t, "UpdateMemberStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_RemoveMember_WrapsAStatusUpdateFailureWithoutBroadcasting(t *testing.T) {
	f := newLifecycleFixture()
	memberID := uuid.New()
	f.expectFindCircle(f.newCircle(circle.CircleStatusActive))
	f.repo.On("FindMemberByCircleAndUser", mock.Anything, f.circleID, memberID).
		Return(&circle.CircleMember{CircleID: f.circleID, UserID: memberID, Status: circle.MemberStatusActive}, nil)
	f.repo.On("UpdateMemberStatus", mock.Anything, f.circleID, memberID, circle.MemberStatusRemoved).Return(errLifecycleRepo)

	err := f.svc.RemoveMember(context.Background(), f.circleID.String(), f.organizerID.String(), memberID.String(), "")

	assert.ErrorIs(t, err, errLifecycleRepo)
	assert.ErrorContains(t, err, "removing member")
	assert.Empty(t, f.broadcaster.membersLeft)
}

// ── Round config and snapshots ──

func TestCircleService_QueryRoundConfig_ReturnsTheStoredSnapshot(t *testing.T) {
	f := newLifecycleFixture()
	want := &circle.RoundConfigSnapshot{ID: uuid.New(), CircleID: f.circleID, RoundNumber: 2, ConfigHash: "abc"}
	f.repo.On("GetRoundConfigSnapshot", mock.Anything, f.circleID, 2).Return(want, nil)

	got, err := f.svc.QueryRoundConfig(context.Background(), f.circleID.String(), 2)

	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestCircleService_QueryRoundConfig_RejectsAMalformedCircleID(t *testing.T) {
	f := newLifecycleFixture()

	got, err := f.svc.QueryRoundConfig(context.Background(), "not-a-uuid", 1)

	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)
	f.repo.AssertNotCalled(t, "GetRoundConfigSnapshot", mock.Anything, mock.Anything, mock.Anything)
}

func TestCircleService_QueryRoundConfig_PassesRepositoryErrorsThrough(t *testing.T) {
	f := newLifecycleFixture()
	f.repo.On("GetRoundConfigSnapshot", mock.Anything, f.circleID, 9).Return(nil, errLifecycleRepo)

	got, err := f.svc.QueryRoundConfig(context.Background(), f.circleID.String(), 9)

	assert.Nil(t, got)
	assert.Equal(t, errLifecycleRepo, err)
}

func TestCircleService_GetCircleSnapshot_ReturnsTheSnapshotForTheUser(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	want := &circle.CircleSnapshot{UserRole: "member"}
	f.repo.On("GetCircleSnapshot", mock.Anything, f.circleID, userID).Return(want, nil)

	got, err := f.svc.GetCircleSnapshot(context.Background(), f.circleID.String(), userID.String())

	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestCircleService_GetCircleSnapshot_RejectsMalformedIDs(t *testing.T) {
	f := newLifecycleFixture()

	got, err := f.svc.GetCircleSnapshot(context.Background(), "not-a-uuid", uuid.NewString())
	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	got, err = f.svc.GetCircleSnapshot(context.Background(), f.circleID.String(), "not-a-uuid")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)
	f.repo.AssertNotCalled(t, "GetCircleSnapshot", mock.Anything, mock.Anything, mock.Anything)
}

func TestCircleService_GetCircleSnapshot_PassesRepositoryErrorsThrough(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	f.repo.On("GetCircleSnapshot", mock.Anything, f.circleID, userID).Return(nil, errLifecycleRepo)

	got, err := f.svc.GetCircleSnapshot(context.Background(), f.circleID.String(), userID.String())

	assert.Nil(t, got)
	assert.Equal(t, errLifecycleRepo, err)
}

func TestCircleService_GetBulkCircleSnapshots_ParsesIDsInOrder(t *testing.T) {
	f := newLifecycleFixture()
	userID, first, second := uuid.New(), uuid.New(), uuid.New()
	want := []circle.CircleSnapshot{{UserRole: "organizer"}, {UserRole: "member"}}
	f.repo.On("GetBulkCircleSnapshots", mock.Anything, userID, []uuid.UUID{first, second}).Return(want, nil)

	got, err := f.svc.GetBulkCircleSnapshots(context.Background(), userID.String(), []string{first.String(), second.String()})

	require.NoError(t, err)
	assert.Equal(t, want, got)
	f.repo.AssertExpectations(t)
}

func TestCircleService_GetBulkCircleSnapshots_AllowsAnEmptyList(t *testing.T) {
	f := newLifecycleFixture()
	userID := uuid.New()
	f.repo.On("GetBulkCircleSnapshots", mock.Anything, userID, []uuid.UUID{}).Return([]circle.CircleSnapshot{}, nil)

	got, err := f.svc.GetBulkCircleSnapshots(context.Background(), userID.String(), nil)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCircleService_GetBulkCircleSnapshots_RejectsMalformedIDsWithoutQuerying(t *testing.T) {
	f := newLifecycleFixture()

	got, err := f.svc.GetBulkCircleSnapshots(context.Background(), "not-a-uuid", []string{uuid.NewString()})
	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	got, err = f.svc.GetBulkCircleSnapshots(context.Background(), uuid.NewString(), []string{uuid.NewString(), "not-a-uuid"})
	assert.Nil(t, got)
	assert.ErrorIs(t, err, circle.ErrInvalidUUID)

	f.repo.AssertNotCalled(t, "GetBulkCircleSnapshots", mock.Anything, mock.Anything, mock.Anything)
}

func TestCircleService_GetBulkCircleSnapshots_PassesRepositoryErrorsThrough(t *testing.T) {
	f := newLifecycleFixture()
	userID, id := uuid.New(), uuid.New()
	f.repo.On("GetBulkCircleSnapshots", mock.Anything, userID, []uuid.UUID{id}).Return(nil, errLifecycleRepo)

	got, err := f.svc.GetBulkCircleSnapshots(context.Background(), userID.String(), []string{id.String()})

	assert.Nil(t, got)
	assert.Equal(t, errLifecycleRepo, err)
}
