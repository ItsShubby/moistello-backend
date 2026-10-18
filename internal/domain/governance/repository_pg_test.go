package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moistello/backend/pkg/apperrors"
)

var errPGBoom = errors.New("connection reset")

// pgProposalColumns is the column order proposalColumns projects and
// pgRepository scans, so a column added there must be added here too.
var pgProposalColumns = []string{
	"id", "title", "description", "proposal_type", "creator_id", "status",
	"for_votes", "against_votes", "for_weight", "against_weight",
	"snapshot_at", "snapshot_total_weight", "executed_at", "executable_at", "cancelled_at",
	"created_at", "updated_at",
}

func newPGRepoForTest(t *testing.T) (Repository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewRepository(sqlx.NewDb(db, "sqlmock")), mock
}

func samplePGProposal() Proposal {
	snapshotAt := baseTime
	return Proposal{
		ID:                  uuid.New(),
		Title:               "Raise the late fee cap",
		Description:         "Raise it to 10%",
		ProposalType:        "parameter",
		CreatorID:           uuid.New(),
		Status:              ProposalStatusPending,
		ForVotes:            3,
		AgainstVotes:        1,
		ForWeight:           30,
		AgainstWeight:       10,
		SnapshotAt:          &snapshotAt,
		SnapshotTotalWeight: 100,
		CreatedAt:           baseTime,
		UpdatedAt:           baseTime,
	}
}

func pgProposalRows(proposals ...Proposal) *sqlmock.Rows {
	rows := sqlmock.NewRows(pgProposalColumns)
	for _, p := range proposals {
		rows.AddRow(
			p.ID, p.Title, p.Description, p.ProposalType, p.CreatorID, string(p.Status),
			p.ForVotes, p.AgainstVotes, p.ForWeight, p.AgainstWeight,
			p.SnapshotAt, p.SnapshotTotalWeight, p.ExecutedAt, p.ExecutableAt, p.CancelledAt,
			p.CreatedAt, p.UpdatedAt,
		)
	}
	return rows
}

func pgCountRows(n int) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"count"}).AddRow(n)
}

func pgWeightRows(weights map[uuid.UUID]int64) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"voter_id", "weight"})
	for voterID, weight := range weights {
		rows.AddRow(voterID, weight)
	}
	return rows
}

const (
	pgSelectWeights       = "SELECT voter_id, weight FROM governance_weight_snapshots"
	pgSelectCancelCount   = `SELECT COUNT\(\*\) FROM governance_cancellation_votes`
	pgSelectProposalCount = `SELECT COUNT\(\*\) FROM governance_proposals`
)

// ── CreateProposal / insertWeight ──

func TestPGRepository_NewRepositoryReturnsAPostgresRepository(t *testing.T) {
	repo, _ := newPGRepoForTest(t)

	assert.IsType(t, &pgRepository{}, repo)
}

func TestPGRepository_CreateProposal_InsertsTheProposalAndEveryWeightSnapshot(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	// The snapshot map iterates in random order, so the two weight inserts
	// may arrive in either order.
	mock.MatchExpectationsInOrder(false)
	p := samplePGProposal()
	voterA, voterB := uuid.New(), uuid.New()
	p.WeightSnapshot = map[uuid.UUID]int64{voterA: 5, voterB: 7}

	mock.ExpectExec("INSERT INTO governance_proposals").
		WithArgs(p.ID, p.Title, p.Description, p.ProposalType, p.CreatorID, string(p.Status),
			p.ForVotes, p.AgainstVotes, p.ForWeight, p.AgainstWeight,
			p.SnapshotAt, p.SnapshotTotalWeight, p.CreatedAt, p.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO governance_weight_snapshots").
		WithArgs(p.ID, voterA, int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO governance_weight_snapshots").
		WithArgs(p.ID, voterB, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.CreateProposal(context.Background(), &p))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_CreateProposal_WithoutASnapshotWritesOnlyTheProposal(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	mock.ExpectExec("INSERT INTO governance_proposals").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.CreateProposal(context.Background(), &p))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_CreateProposal_WrapsAnInsertFailureAndWritesNoSnapshot(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	p.WeightSnapshot = map[uuid.UUID]int64{uuid.New(): 1}
	mock.ExpectExec("INSERT INTO governance_proposals").WillReturnError(errPGBoom)

	err := repo.CreateProposal(context.Background(), &p)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "inserting governance proposal")
	assert.NoError(t, mock.ExpectationsWereMet(), "no weight insert may be attempted after the proposal insert fails")
}

func TestPGRepository_CreateProposal_WrapsAWeightSnapshotInsertFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	p.WeightSnapshot = map[uuid.UUID]int64{uuid.New(): 1}
	mock.ExpectExec("INSERT INTO governance_proposals").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO governance_weight_snapshots").WillReturnError(errPGBoom)

	err := repo.CreateProposal(context.Background(), &p)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "inserting governance weight snapshot")
}

// ── GetProposal / loadWeightSnapshot / loadCancellationVotes ──

func TestPGRepository_GetProposal_LoadsTheRowItsWeightSnapshotAndCancellationVotes(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	executable := baseTime.Add(48 * time.Hour)
	p.Status = ProposalStatusPassed
	p.ExecutableAt = &executable
	voterA, voterB := uuid.New(), uuid.New()
	mock.ExpectQuery("FROM governance_proposals").WithArgs(p.ID).WillReturnRows(pgProposalRows(p))
	mock.ExpectQuery(pgSelectWeights).WithArgs(p.ID).
		WillReturnRows(pgWeightRows(map[uuid.UUID]int64{voterA: 3, voterB: 4}))
	mock.ExpectQuery(pgSelectCancelCount).WithArgs(p.ID).WillReturnRows(pgCountRows(2))

	got, err := repo.GetProposal(context.Background(), p.ID)

	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.Title, got.Title)
	assert.Equal(t, ProposalStatusPassed, got.Status)
	assert.Equal(t, p.ForWeight, got.ForWeight)
	require.NotNil(t, got.ExecutableAt)
	assert.True(t, executable.Equal(*got.ExecutableAt))
	assert.Equal(t, map[uuid.UUID]int64{voterA: 3, voterB: 4}, got.WeightSnapshot)
	assert.Equal(t, 2, got.CancellationVotes)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_GetProposal_WithoutSnapshotRowsLeavesTheSnapshotNil(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	mock.ExpectQuery("FROM governance_proposals").WillReturnRows(pgProposalRows(p))
	mock.ExpectQuery(pgSelectWeights).WillReturnRows(pgWeightRows(nil))
	mock.ExpectQuery(pgSelectCancelCount).WillReturnRows(pgCountRows(0))

	got, err := repo.GetProposal(context.Background(), p.ID)

	require.NoError(t, err)
	assert.Nil(t, got.WeightSnapshot, "no snapshot rows means weighting was off; the service falls back to head counts")
	assert.Zero(t, got.CancellationVotes)
}

func TestPGRepository_GetProposal_UnknownIDIsNotFound(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_proposals").WillReturnRows(sqlmock.NewRows(pgProposalColumns))

	got, err := repo.GetProposal(context.Background(), uuid.New())

	assert.Nil(t, got)
	assert.ErrorIs(t, err, ErrProposalNotFound)
}

func TestPGRepository_GetProposal_WrapsAQueryFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_proposals").WillReturnError(errPGBoom)

	got, err := repo.GetProposal(context.Background(), uuid.New())

	assert.Nil(t, got)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "getting governance proposal")
}

func TestPGRepository_GetProposal_FailsWhenTheWeightSnapshotCannotBeLoaded(t *testing.T) {
	tests := []struct {
		name    string
		rows    func() *sqlmock.Rows
		wantErr string
	}{
		{
			name:    "query error",
			rows:    nil,
			wantErr: "loading governance weight snapshot",
		},
		{
			name: "unscannable row",
			rows: func() *sqlmock.Rows {
				return sqlmock.NewRows([]string{"voter_id", "weight"}).AddRow(uuid.New(), "not-a-number")
			},
			wantErr: "scanning governance weight snapshot",
		},
		{
			name: "iteration error",
			rows: func() *sqlmock.Rows {
				return sqlmock.NewRows([]string{"voter_id", "weight"}).
					AddRow(uuid.New(), int64(1)).
					AddRow(uuid.New(), int64(2)).
					RowError(1, errPGBoom)
			},
			wantErr: "iterating governance weight snapshot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			p := samplePGProposal()
			mock.ExpectQuery("FROM governance_proposals").WillReturnRows(pgProposalRows(p))
			expected := mock.ExpectQuery(pgSelectWeights)
			if tt.rows == nil {
				expected.WillReturnError(errPGBoom)
			} else {
				expected.WillReturnRows(tt.rows())
			}

			got, err := repo.GetProposal(context.Background(), p.ID)

			assert.Nil(t, got)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPGRepository_GetProposal_StillReturnsTheProposalWhenCountingCancellationVotesFails(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	mock.ExpectQuery("FROM governance_proposals").WillReturnRows(pgProposalRows(p))
	mock.ExpectQuery(pgSelectWeights).WillReturnRows(pgWeightRows(nil))
	mock.ExpectQuery(pgSelectCancelCount).WillReturnError(errPGBoom)

	got, err := repo.GetProposal(context.Background(), p.ID)

	require.NoError(t, err, "the count is informational; the proposal is still readable without it")
	assert.Equal(t, p.ID, got.ID)
	assert.Zero(t, got.CancellationVotes)
}

// ── ListProposals ──

func TestPGRepository_ListProposals_ReturnsTheTotalAndEachProposalWithItsExtras(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	first, second := samplePGProposal(), samplePGProposal()
	voter := uuid.New()
	mock.ExpectQuery(pgSelectProposalCount).WillReturnRows(pgCountRows(12))
	mock.ExpectQuery("ORDER BY created_at DESC").WithArgs(10, 0).WillReturnRows(pgProposalRows(first, second))
	mock.ExpectQuery(pgSelectWeights).WithArgs(first.ID).WillReturnRows(pgWeightRows(map[uuid.UUID]int64{voter: 9}))
	mock.ExpectQuery(pgSelectCancelCount).WithArgs(first.ID).WillReturnRows(pgCountRows(1))
	mock.ExpectQuery(pgSelectWeights).WithArgs(second.ID).WillReturnRows(pgWeightRows(nil))
	mock.ExpectQuery(pgSelectCancelCount).WithArgs(second.ID).WillReturnRows(pgCountRows(0))

	got, total, err := repo.ListProposals(context.Background(), 0, 0)

	require.NoError(t, err)
	assert.Equal(t, 12, total)
	require.Len(t, got, 2)
	assert.Equal(t, first.ID, got[0].ID)
	assert.Equal(t, map[uuid.UUID]int64{voter: 9}, got[0].WeightSnapshot)
	assert.Equal(t, 1, got[0].CancellationVotes)
	assert.Equal(t, second.ID, got[1].ID)
	assert.Nil(t, got[1].WeightSnapshot)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_ListProposals_NormalizesPagingBeforeQuerying(t *testing.T) {
	tests := []struct {
		name       string
		page       int
		limit      int
		wantLimit  int
		wantOffset int
	}{
		{"zero page and limit use the defaults", 0, 0, 10, 0},
		{"negative values use the defaults", -4, -1, 10, 0},
		{"an oversized limit is capped at 100", 1, 1000, 100, 0},
		{"later pages offset by whole pages", 3, 20, 20, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			mock.ExpectQuery(pgSelectProposalCount).WillReturnRows(pgCountRows(0))
			mock.ExpectQuery("ORDER BY created_at DESC").
				WithArgs(tt.wantLimit, tt.wantOffset).
				WillReturnRows(sqlmock.NewRows(pgProposalColumns))

			got, total, err := repo.ListProposals(context.Background(), tt.page, tt.limit)

			require.NoError(t, err)
			assert.Zero(t, total)
			assert.NotNil(t, got, "an empty page is an empty list, never nil")
			assert.Empty(t, got)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPGRepository_ListProposals_WrapsACountFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery(pgSelectProposalCount).WillReturnError(errPGBoom)

	got, total, err := repo.ListProposals(context.Background(), 1, 10)

	assert.Nil(t, got)
	assert.Zero(t, total)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "counting governance proposals")
}

func TestPGRepository_ListProposals_WrapsAListFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery(pgSelectProposalCount).WillReturnRows(pgCountRows(3))
	mock.ExpectQuery("ORDER BY created_at DESC").WillReturnError(errPGBoom)

	got, total, err := repo.ListProposals(context.Background(), 1, 10)

	assert.Nil(t, got)
	assert.Zero(t, total)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "listing governance proposals")
}

func TestPGRepository_ListProposals_StillListsWhenPerProposalExtrasFail(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	p := samplePGProposal()
	mock.ExpectQuery(pgSelectProposalCount).WillReturnRows(pgCountRows(1))
	mock.ExpectQuery("ORDER BY created_at DESC").WillReturnRows(pgProposalRows(p))
	mock.ExpectQuery(pgSelectWeights).WillReturnError(errPGBoom)
	mock.ExpectQuery(pgSelectCancelCount).WillReturnError(errPGBoom)

	got, total, err := repo.ListProposals(context.Background(), 1, 10)

	require.NoError(t, err, "the weight and cancellation breakdowns are best-effort in a list")
	assert.Equal(t, 1, total)
	require.Len(t, got, 1)
	assert.Equal(t, p.ID, got[0].ID)
	assert.Nil(t, got[0].WeightSnapshot)
	assert.Zero(t, got[0].CancellationVotes)
}

// ── MarkPassed / MarkCancelled ──

func TestPGRepository_MarkPassed_MovesAPendingProposalToPassed(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	id := uuid.New()
	executableAt := baseTime.Add(24 * time.Hour)
	mock.ExpectExec("UPDATE governance_proposals").
		WithArgs(ProposalStatusPassed, executableAt, id, ProposalStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.MarkPassed(context.Background(), id, executableAt))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_MarkPassed_ReportsAConflictWhenNoLongerPending(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.MarkPassed(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, apperrors.ErrConflict)
}

func TestPGRepository_MarkPassed_WrapsAnExecFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnError(errPGBoom)

	err := repo.MarkPassed(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "marking governance proposal passed")
}

func TestPGRepository_MarkPassed_ReturnsARowsAffectedFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnResult(sqlmock.NewErrorResult(errPGBoom))

	err := repo.MarkPassed(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, errPGBoom)
}

func TestPGRepository_MarkCancelled_MovesAPassedProposalToCancelled(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	id := uuid.New()
	cancelledAt := baseTime.Add(time.Hour)
	mock.ExpectExec("UPDATE governance_proposals").
		WithArgs(ProposalStatusCancelled, cancelledAt, id, ProposalStatusPassed).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.MarkCancelled(context.Background(), id, cancelledAt))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_MarkCancelled_ReportsAConflictWhenNoLongerPassed(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.MarkCancelled(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, apperrors.ErrConflict)
}

func TestPGRepository_MarkCancelled_WrapsAnExecFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnError(errPGBoom)

	err := repo.MarkCancelled(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "cancelling governance proposal")
}

func TestPGRepository_MarkCancelled_ReturnsARowsAffectedFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("UPDATE governance_proposals").WillReturnResult(sqlmock.NewErrorResult(errPGBoom))

	err := repo.MarkCancelled(context.Background(), uuid.New(), baseTime)

	assert.ErrorIs(t, err, errPGBoom)
}

// ── Cancellation votes ──

func TestPGRepository_RecordCancellationVote_ReportsWhetherTheVoteWasNew(t *testing.T) {
	tests := []struct {
		name         string
		rowsAffected int64
		want         bool
	}{
		{"first vote is recorded", 1, true},
		{"repeat vote hits ON CONFLICT DO NOTHING", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			proposalID, voterID := uuid.New(), uuid.New()
			mock.ExpectExec("INSERT INTO governance_cancellation_votes").
				WithArgs(proposalID, voterID).
				WillReturnResult(sqlmock.NewResult(0, tt.rowsAffected))

			got, err := repo.RecordCancellationVote(context.Background(), proposalID, voterID)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPGRepository_RecordCancellationVote_WrapsAnExecFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("INSERT INTO governance_cancellation_votes").WillReturnError(errPGBoom)

	got, err := repo.RecordCancellationVote(context.Background(), uuid.New(), uuid.New())

	assert.False(t, got)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "recording governance cancellation vote")
}

func TestPGRepository_RecordCancellationVote_ReturnsARowsAffectedFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectExec("INSERT INTO governance_cancellation_votes").WillReturnResult(sqlmock.NewErrorResult(errPGBoom))

	got, err := repo.RecordCancellationVote(context.Background(), uuid.New(), uuid.New())

	assert.False(t, got)
	assert.ErrorIs(t, err, errPGBoom)
}

func TestPGRepository_CountCancellationVotes_ReturnsTheCount(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	proposalID := uuid.New()
	mock.ExpectQuery(pgSelectCancelCount).WithArgs(proposalID).WillReturnRows(pgCountRows(5))

	got, err := repo.CountCancellationVotes(context.Background(), proposalID)

	require.NoError(t, err)
	assert.Equal(t, 5, got)
}

func TestPGRepository_CountCancellationVotes_WrapsAQueryFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery(pgSelectCancelCount).WillReturnError(errPGBoom)

	got, err := repo.CountCancellationVotes(context.Background(), uuid.New())

	assert.Zero(t, got)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "counting governance cancellation votes")
}

// ── HasVoted / RecordVoteWeighted ──

func TestPGRepository_HasVoted_ReportsWhetherAVoteExists(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "has voted", false: "has not voted"}[exists], func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			proposalID, voterID := uuid.New(), uuid.New()
			mock.ExpectQuery("FROM governance_votes").
				WithArgs(proposalID, voterID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))

			got, err := repo.HasVoted(context.Background(), proposalID, voterID)

			require.NoError(t, err)
			assert.Equal(t, exists, got)
		})
	}
}

func TestPGRepository_HasVoted_WrapsAQueryFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").WillReturnError(errPGBoom)

	got, err := repo.HasVoted(context.Background(), uuid.New(), uuid.New())

	assert.False(t, got)
	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "checking governance vote existence")
}

func TestPGRepository_RecordVoteWeighted_RecordsAForVoteAndMovesBothTallies(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	proposalID, voterID := uuid.New(), uuid.New()
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO governance_votes").
		WithArgs(proposalID, voterID, true, int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SET for_votes").
		WithArgs(int64(7), sqlmock.AnyArg(), proposalID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.RecordVoteWeighted(context.Background(), proposalID, voterID, true, 7)

	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_RecordVoteWeighted_RecordsAnAgainstVoteAndMovesBothTallies(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	proposalID, voterID := uuid.New(), uuid.New()
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO governance_votes").
		WithArgs(proposalID, voterID, false, int64(3), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SET against_votes").
		WithArgs(int64(3), sqlmock.AnyArg(), proposalID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.RecordVoteWeighted(context.Background(), proposalID, voterID, false, 3)

	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_RecordVoteWeighted_RejectsASecondVoteWithoutOpeningATransaction(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), true, 1)

	assert.ErrorIs(t, err, ErrAlreadyVoted)
	assert.NoError(t, mock.ExpectationsWereMet(), "no transaction may be started for a duplicate vote")
}

func TestPGRepository_RecordVoteWeighted_PropagatesAnExistenceCheckFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").WillReturnError(errPGBoom)

	err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), true, 1)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "checking governance vote existence")
}

func TestPGRepository_RecordVoteWeighted_WrapsABeginFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin().WillReturnError(errPGBoom)

	err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), true, 1)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "beginning vote transaction")
}

func TestPGRepository_RecordVoteWeighted_RollsBackWhenTheVoteInsertFails(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO governance_votes").WillReturnError(errPGBoom)
	mock.ExpectRollback()

	err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), true, 1)

	assert.ErrorIs(t, err, errPGBoom)
	assert.ErrorContains(t, err, "recording governance vote")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_RecordVoteWeighted_RollsBackWhenTheTallyUpdateFails(t *testing.T) {
	for _, vote := range []bool{true, false} {
		t.Run(map[bool]string{true: "for", false: "against"}[vote], func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			mock.ExpectQuery("FROM governance_votes").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO governance_votes").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE governance_proposals").WillReturnError(errPGBoom)
			mock.ExpectRollback()

			err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), vote, 1)

			assert.ErrorIs(t, err, errPGBoom)
			assert.ErrorContains(t, err, "updating proposal vote count")
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPGRepository_RecordVoteWeighted_ReturnsACommitFailure(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	mock.ExpectQuery("FROM governance_votes").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO governance_votes").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE governance_proposals").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errPGBoom)

	err := repo.RecordVoteWeighted(context.Background(), uuid.New(), uuid.New(), true, 1)

	assert.ErrorIs(t, err, errPGBoom)
}

// ── UpdateStatus ──

func TestPGRepository_UpdateStatus_StampsExecutedAtWhenGiven(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	id := uuid.New()
	executedAt := baseTime.Add(72 * time.Hour)
	mock.ExpectExec("executed_at").
		WithArgs(string(ProposalStatusExecuted), executedAt, sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.UpdateStatus(context.Background(), id, ProposalStatusExecuted, &executedAt))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_UpdateStatus_OmitsExecutedAtWhenNil(t *testing.T) {
	repo, mock := newPGRepoForTest(t)
	id := uuid.New()
	mock.ExpectExec(`SET status = \$1, updated_at`).
		WithArgs(string(ProposalStatusRejected), sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.UpdateStatus(context.Background(), id, ProposalStatusRejected, nil))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPGRepository_UpdateStatus_ReturnsAnExecFailure(t *testing.T) {
	executedAt := baseTime
	for name, at := range map[string]*time.Time{"with executedAt": &executedAt, "without executedAt": nil} {
		t.Run(name, func(t *testing.T) {
			repo, mock := newPGRepoForTest(t)
			mock.ExpectExec("UPDATE governance_proposals").WillReturnError(errPGBoom)

			err := repo.UpdateStatus(context.Background(), uuid.New(), ProposalStatusExecuted, at)

			assert.ErrorIs(t, err, errPGBoom)
		})
	}
}
