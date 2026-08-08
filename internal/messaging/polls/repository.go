package polls

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Poll is a stored poll. RoomID and ChannelID are mutually exclusive (one names
// the surface it lives on); TotalVoters and each option's count are denormalized
// and recomputed by RecalcCountsTx. IsClosed stops further voting.
type Poll struct {
	ID             uuid.UUID
	MessageID      int64
	RoomID         *uuid.UUID
	ChannelID      *uuid.UUID
	CreatorID      uuid.UUID
	Question       string
	PollType       int
	IsAnonymous    bool
	AllowsMultiple bool
	CorrectOption  *int
	Explanation    *string
	CloseDate      *time.Time
	IsClosed       bool
	TotalVoters    int
}

// Option is one poll choice, identified by its per-poll OptionID, with a
// denormalized VoteCount maintained by RecalcCountsTx.
type Option struct {
	OptionID  int
	Text      string
	VoteCount int
}

// Repository is the polls data-access layer over pool. Multi-step mutations take
// a pgx.Tx (see BeginTx) so vote changes and count recalculation commit together.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// BeginTx starts a transaction for the multi-step insert/vote flows whose Tx
// methods must share one transaction.
func (r *Repository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// InsertTx inserts the poll row on tx. Options are inserted separately via
// InsertOptionTx, and the poll's backing message must be inserted in the same tx.
func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, p *Poll) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO polls (id, message_id, room_id, channel_id, creator_id, question, poll_type,
		 is_anonymous, allows_multiple, correct_option, explanation, close_date)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		p.ID, p.MessageID, p.RoomID, p.ChannelID, p.CreatorID, p.Question, p.PollType,
		p.IsAnonymous, p.AllowsMultiple, p.CorrectOption, p.Explanation, p.CloseDate)
	return err
}

// InsertOptionTx inserts one poll option on tx. optionID is the caller-assigned
// index (0-based) used to reference the choice when voting.
func (r *Repository) InsertOptionTx(ctx context.Context, tx pgx.Tx, pollID uuid.UUID, optionID int, text string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO poll_options (poll_id, option_id, text) VALUES ($1,$2,$3)`,
		pollID, optionID, text)
	return err
}

// GetFlags reads just the is_closed and allows_multiple flags for a poll, used to
// gate voting before opening a transaction. err is non-nil (e.g. pgx.ErrNoRows)
// when the poll does not exist.
func (r *Repository) GetFlags(ctx context.Context, pollID uuid.UUID) (isClosed, allowsMultiple bool, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT is_closed, allows_multiple FROM polls WHERE id = $1`, pollID,
	).Scan(&isClosed, &allowsMultiple)
	return
}

// DeleteUserVotesTx removes all of a user's existing votes on a poll within tx,
// used to enforce single-choice semantics before inserting the new vote(s).
func (r *Repository) DeleteUserVotesTx(ctx context.Context, tx pgx.Tx, pollID, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	return err
}

// InsertVoteTx records one (user, option) vote on tx, ignoring duplicates via ON
// CONFLICT DO NOTHING so re-voting the same option is a no-op.
func (r *Repository) InsertVoteTx(ctx context.Context, tx pgx.Tx, pollID, userID uuid.UUID, optionID int32) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO poll_votes (poll_id, user_id, option_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		pollID, userID, optionID)
	return err
}

// RecalcCountsTx recomputes the denormalized per-option vote_count and the poll's
// total_voters (distinct voters) from poll_votes within tx. Call it after any vote
// mutation so counts reflect the change atomically.
func (r *Repository) RecalcCountsTx(ctx context.Context, tx pgx.Tx, pollID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE poll_options SET vote_count = (
			SELECT COUNT(*) FROM poll_votes
			WHERE poll_id = poll_options.poll_id AND option_id = poll_options.option_id
		) WHERE poll_id = $1`, pollID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE polls SET total_voters = (
			SELECT COUNT(DISTINCT user_id) FROM poll_votes WHERE poll_id = $1
		) WHERE id = $1`, pollID)
	return err
}

// Load fetches a poll with its options (ordered by option_id) and the given
// user's own selected option IDs (myVotes). A missing poll returns an error;
// option and vote rows that fail to scan are skipped, and a vote-query error
// yields nil myVotes without failing the call.
func (r *Repository) Load(ctx context.Context, pollID, userID uuid.UUID) (*Poll, []Option, []int, error) {
	var p Poll
	err := r.pool.QueryRow(ctx,
		`SELECT id, message_id, room_id, channel_id, creator_id, question, poll_type,
		 is_anonymous, allows_multiple, correct_option, explanation, close_date, is_closed, total_voters
		 FROM polls WHERE id = $1`, pollID,
	).Scan(&p.ID, &p.MessageID, &p.RoomID, &p.ChannelID, &p.CreatorID, &p.Question, &p.PollType,
		&p.IsAnonymous, &p.AllowsMultiple, &p.CorrectOption, &p.Explanation, &p.CloseDate, &p.IsClosed, &p.TotalVoters)
	if err != nil {
		return nil, nil, nil, err
	}

	optRows, err := r.pool.Query(ctx,
		`SELECT option_id, text, vote_count FROM poll_options WHERE poll_id = $1 ORDER BY option_id`, pollID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer optRows.Close()

	var options []Option
	for optRows.Next() {
		var o Option
		if err := optRows.Scan(&o.OptionID, &o.Text, &o.VoteCount); err != nil {
			continue
		}
		options = append(options, o)
	}

	var myVotes []int
	voteRows, err := r.pool.Query(ctx,
		`SELECT option_id FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	if err == nil {
		for voteRows.Next() {
			var optID int
			if voteRows.Scan(&optID) == nil {
				myVotes = append(myVotes, optID)
			}
		}
		voteRows.Close()
	}

	return &p, options, myVotes, nil
}

// Close marks a poll closed only if creatorID owns it; a non-owner or missing
// poll affects no rows and returns nil (no error).
func (r *Repository) Close(ctx context.Context, pollID uuid.UUID, creatorID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE polls SET is_closed = true WHERE id = $1 AND creator_id = $2`, pollID, creatorID)
	return err
}

// CloseExpired closes every open poll whose close_date has passed and returns the
// number of polls closed. Used by the background RunCloser loop.
func (r *Repository) CloseExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE polls SET is_closed = true
		 WHERE is_closed = false AND close_date IS NOT NULL AND close_date <= NOW()
		 RETURNING id`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
