package community

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
)

const (
	ContestDraft      = "draft"
	ContestPublishing = "publishing"
	ContestActive     = "active"
	ContestClosing    = "closing"
	ContestFinalized  = "finalized"
)

// Contest is the durable lifecycle record for one Telegram forum-topic contest.
type Contest struct {
	ContestID             string     `db:"contest_id" json:"contest_id"`
	ChatID                int64      `db:"chat_id" json:"chat_id"`
	ThreadID              int        `db:"thread_id" json:"thread_id"`
	Title                 string     `db:"title" json:"title"`
	Bases                 string     `db:"bases" json:"bases"`
	AnnouncementText      string     `db:"announcement_text" json:"announcement_text"`
	Status                string     `db:"status" json:"status"`
	AnnouncementMessageID int        `db:"announcement_message_id" json:"announcement_message_id"`
	DeadlineAt            *time.Time `db:"deadline_at" json:"deadline_at,omitempty"`
	PublishedAt           *time.Time `db:"published_at" json:"published_at,omitempty"`
	ReactionCutoffAt      *time.Time `db:"reaction_cutoff_at" json:"reaction_cutoff_at,omitempty"`
	ConsolidateAfter      *time.Time `db:"consolidate_after" json:"consolidate_after,omitempty"`
	FinalizedAt           *time.Time `db:"finalized_at" json:"finalized_at,omitempty"`
	WinnerUserID          int64      `db:"winner_user_id" json:"winner_user_id"`
	WinnerMessageID       int        `db:"winner_message_id" json:"winner_message_id"`
	WinnerReactions       int        `db:"winner_reactions" json:"winner_reactions"`
	FinalSnapshot         string     `db:"final_snapshot" json:"-"`
	CreatedBy             string     `db:"created_by" json:"created_by"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

type ContestDraftInput struct {
	Title      string
	Bases      string
	DeadlineAt *time.Time
	CreatedBy  string
}

type ContestLeaderboardEntry struct {
	Position      int            `json:"position"`
	UserID        int64          `db:"user_id" json:"user_id"`
	UserName      string         `db:"username" json:"username"`
	DisplayName   string         `db:"display_name" json:"display_name"`
	MessageID     int            `db:"first_message_id" json:"message_id"`
	ReactionTotal int            `json:"reaction_total"`
	Breakdown     map[string]int `json:"breakdown"`
}

type ContestAppeal struct {
	AppealToken        string    `db:"appeal_token" json:"appeal_token"`
	ContestID          string    `db:"contest_id" json:"contest_id"`
	ChatID             int64     `db:"chat_id" json:"chat_id"`
	UserID             int64     `db:"user_id" json:"user_id"`
	UserName           string    `db:"username" json:"username"`
	DisplayName        string    `db:"display_name" json:"display_name"`
	OriginalMessageID  int       `db:"original_message_id" json:"original_message_id"`
	DuplicateMessageID int       `db:"duplicate_message_id" json:"duplicate_message_id"`
	PhotoFileID        string    `db:"photo_file_id" json:"-"`
	Caption            string    `db:"caption" json:"caption"`
	Status             string    `db:"status" json:"status"`
	AppealText         string    `db:"appeal_text" json:"appeal_text"`
	ResolutionText     string    `db:"resolution_text" json:"resolution_text"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time `db:"updated_at" json:"updated_at"`
}

func BuildContestAnnouncement(title, bases string, deadline *time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📸 %s\n\n%s\n\n", strings.TrimSpace(title), strings.TrimSpace(bases))
	b.WriteString("Cómo participar:\n• Publica una sola foto en este topic.\n• Se admite una participación por usuario.\n")
	b.WriteString("• Si envías otra foto, se eliminará el duplicado y podrás apelar desde el aviso.\n")
	b.WriteString("• El resultado se calcula sumando todas las reacciones recibidas por cada foto.")
	if deadline != nil {
		fmt.Fprintf(&b, "\n\nCierre previsto: %s", deadline.In(time.Local).Format("02/01/2006 15:04"))
	}
	return b.String()
}

func (s *Store) CreateContestDraft(ctx context.Context, input ContestDraftInput, chatID int64) (Contest, error) {
	title, bases := strings.TrimSpace(input.Title), strings.TrimSpace(input.Bases)
	if title == "" || bases == "" || chatID == 0 {
		return Contest{}, fmt.Errorf("title, bases, and chat id are required")
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("contest-%s-%s", now.Format("20060102-150405"), randomToken(4))
	contest := Contest{ContestID: id, ChatID: chatID, Title: title, Bases: bases,
		AnnouncementText: BuildContestAnnouncement(title, bases, input.DeadlineAt), Status: ContestDraft,
		DeadlineAt: input.DeadlineAt, CreatedBy: strings.TrimSpace(input.CreatedBy), CreatedAt: now, UpdatedAt: now}
	query := s.db.Adopt(`INSERT INTO community_contests
		(tenant_id, contest_id, chat_id, thread_id, title, bases, announcement_text, status,
		 deadline_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := s.db.ExecContext(ctx, query, s.db.TenantID(), contest.ContestID, contest.ChatID,
		contest.Title, contest.Bases, contest.AnnouncementText, contest.Status, contest.DeadlineAt,
		contest.CreatedBy, now, now); err != nil {
		return Contest{}, fmt.Errorf("create contest draft: %w", err)
	}
	return contest, nil
}

func (s *Store) UpdateContestDraft(ctx context.Context, contestID, announcement string) error {
	announcement = strings.TrimSpace(announcement)
	if announcement == "" {
		return fmt.Errorf("announcement text is required")
	}
	query := s.db.Adopt(`UPDATE community_contests SET announcement_text = ?, updated_at = ?
		WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	result, err := s.db.ExecContext(ctx, query, announcement, time.Now().UTC(), s.db.TenantID(), contestID, ContestDraft)
	if err != nil {
		return fmt.Errorf("update contest draft: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return fmt.Errorf("contest %s is not an editable draft", contestID)
	}
	return nil
}

func (s *Store) GetContest(ctx context.Context, contestID string) (Contest, error) {
	var contest Contest
	query := s.db.Adopt(`SELECT contest_id, chat_id, thread_id, title, bases, announcement_text, status,
		announcement_message_id, deadline_at, published_at, reaction_cutoff_at, consolidate_after,
		finalized_at, winner_user_id, winner_message_id, winner_reactions, final_snapshot,
		created_by, created_at, updated_at FROM community_contests WHERE tenant_id = ? AND contest_id = ?`)
	if err := s.db.GetContext(ctx, &contest, query, s.db.TenantID(), strings.TrimSpace(contestID)); err != nil {
		return Contest{}, fmt.Errorf("get contest: %w", err)
	}
	return contest, nil
}

func (s *Store) ListContests(ctx context.Context, limit int) ([]Contest, error) {
	var contests []Contest
	query := s.db.Adopt(`SELECT contest_id, chat_id, thread_id, title, bases, announcement_text, status,
		announcement_message_id, deadline_at, published_at, reaction_cutoff_at, consolidate_after,
		finalized_at, winner_user_id, winner_message_id, winner_reactions, final_snapshot,
		created_by, created_at, updated_at FROM community_contests WHERE tenant_id = ?
		ORDER BY created_at DESC LIMIT ?`)
	if err := s.db.SelectContext(ctx, &contests, query, s.db.TenantID(), boundedLimit(limit)); err != nil {
		return nil, fmt.Errorf("list contests: %w", err)
	}
	return contests, nil
}

func (s *Store) ActiveContestForThread(ctx context.Context, chatID int64, threadID int) (Contest, bool, error) {
	var contest Contest
	query := s.db.Adopt(`SELECT contest_id, chat_id, thread_id, title, bases, announcement_text, status,
		announcement_message_id, deadline_at, published_at, reaction_cutoff_at, consolidate_after,
		finalized_at, winner_user_id, winner_message_id, winner_reactions, final_snapshot,
		created_by, created_at, updated_at FROM community_contests
		WHERE tenant_id = ? AND chat_id = ? AND thread_id = ? AND status IN (?, ?)
		ORDER BY published_at DESC LIMIT 1`)
	if err := s.db.GetContext(ctx, &contest, query, s.db.TenantID(), chatID, threadID, ContestActive, ContestClosing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Contest{}, false, nil
		}
		return Contest{}, false, fmt.Errorf("find active contest topic: %w", err)
	}
	return contest, true, nil
}

func (s *Store) setContestPublishing(ctx context.Context, contestID string) (Contest, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	tx, beginErr := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if beginErr != nil {
		return Contest{}, beginErr
	}
	defer tx.Rollback()
	var active int
	q := s.db.Adopt(`SELECT COUNT(*) FROM community_contests WHERE tenant_id = ? AND status IN (?, ?, ?)`)
	if err := tx.GetContext(
		ctx, &active, q, s.db.TenantID(), ContestPublishing, ContestActive, ContestClosing,
	); err != nil {
		return Contest{}, err
	}
	if active > 0 {
		return Contest{}, fmt.Errorf("ya existe un concurso publicado o en cierre")
	}
	q = s.db.Adopt(`UPDATE community_contests SET status = ?, updated_at = ? WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	res, err := tx.ExecContext(ctx, q, ContestPublishing, time.Now().UTC(), s.db.TenantID(), contestID, ContestDraft)
	if err != nil {
		return Contest{}, err
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		return Contest{}, fmt.Errorf("el concurso no es un borrador publicable")
	}
	if err := tx.Commit(); err != nil {
		return Contest{}, err
	}
	return s.GetContest(ctx, contestID)
}

func (s *Store) markContestActive(ctx context.Context, contestID string, threadID, messageID int) error {
	now := time.Now().UTC()
	q := s.db.Adopt(`UPDATE community_contests SET status = ?, thread_id = ?, announcement_message_id = ?,
		published_at = ?, updated_at = ? WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	_, err := s.db.ExecContext(
		ctx, q, ContestActive, threadID, messageID, now, now,
		s.db.TenantID(), contestID, ContestPublishing,
	)
	return err
}

func (s *Store) resetContestDraft(ctx context.Context, contestID string) {
	q := s.db.Adopt(`UPDATE community_contests SET status = ?, updated_at = ? WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	_, _ = s.db.ExecContext(ctx, q, ContestDraft, time.Now().UTC(), s.db.TenantID(), contestID, ContestPublishing)
}

func (s *Store) markContestClosing(ctx context.Context, contestID string, cutoff, consolidateAfter time.Time) error {
	q := s.db.Adopt(`UPDATE community_contests SET status = ?, reaction_cutoff_at = ?, consolidate_after = ?, updated_at = ?
		WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	res, err := s.db.ExecContext(
		ctx, q, ContestClosing, cutoff, consolidateAfter, cutoff,
		s.db.TenantID(), contestID, ContestActive,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		return fmt.Errorf("contest is not active")
	}
	return nil
}

func (s *Store) markContestFinalized(ctx context.Context, contestID, snapshot string, winner ContestLeaderboardEntry) error {
	now := time.Now().UTC()
	q := s.db.Adopt(`UPDATE community_contests SET status = ?, final_snapshot = ?, winner_user_id = ?,
		winner_message_id = ?, winner_reactions = ?, finalized_at = ?, updated_at = ?
		WHERE tenant_id = ? AND contest_id = ? AND status = ?`)
	_, err := s.db.ExecContext(ctx, q, ContestFinalized, snapshot, winner.UserID, winner.MessageID,
		winner.ReactionTotal, now, now, s.db.TenantID(), contestID, ContestClosing)
	return err
}

func (s *Store) RecordReactionCounts(
	ctx context.Context, chatID int64, messageID int, at time.Time, counts map[string]int,
) error {
	contestID, allowed, err := s.reactionContest(ctx, chatID, messageID, at)
	if err != nil || !allowed {
		return err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.db.Adopt(`DELETE FROM community_contest_reactions
		WHERE tenant_id = ? AND contest_id = ? AND message_id = ?`)
	if _, err = tx.ExecContext(ctx, q, s.db.TenantID(), contestID, messageID); err != nil {
		return err
	}
	q = s.db.Adopt(`INSERT INTO community_contest_reactions
		(tenant_id, contest_id, message_id, reaction_key, total_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	for key, count := range counts {
		if count <= 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, q, s.db.TenantID(), contestID, messageID, key, count, at.UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RecordActorReactions(
	ctx context.Context, chatID int64, messageID int, actorKey string,
	at time.Time, reactions []string,
) error {
	contestID, allowed, err := s.reactionContest(ctx, chatID, messageID, at)
	if err != nil || !allowed {
		return err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.db.Adopt(`DELETE FROM community_contest_reaction_actors
		WHERE tenant_id = ? AND contest_id = ? AND message_id = ? AND actor_key = ?`)
	if _, err = tx.ExecContext(ctx, q, s.db.TenantID(), contestID, messageID, actorKey); err != nil {
		return err
	}
	q = s.db.Adopt(`INSERT INTO community_contest_reaction_actors
		(tenant_id, contest_id, message_id, actor_key, reaction_key, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	for _, reaction := range reactions {
		if _, err = tx.ExecContext(ctx, q, s.db.TenantID(), contestID, messageID, actorKey, reaction, at.UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) reactionContest(
	ctx context.Context, chatID int64, messageID int, at time.Time,
) (contestID string, allowed bool, err error) {
	var row struct {
		ContestID string     `db:"contest_id"`
		Cutoff    *time.Time `db:"reaction_cutoff_at"`
	}
	q := s.db.Adopt(`SELECT c.contest_id, c.reaction_cutoff_at FROM community_contests c
		JOIN community_contest_entries e ON e.tenant_id = c.tenant_id AND e.contest_id = c.contest_id
		WHERE c.tenant_id = ? AND c.chat_id = ? AND e.first_message_id = ? AND c.status IN (?, ?) LIMIT 1`)
	if err := s.db.GetContext(ctx, &row, q, s.db.TenantID(), chatID, messageID, ContestActive, ContestClosing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if row.Cutoff != nil && at.After(*row.Cutoff) {
		return row.ContestID, false, nil
	}
	return row.ContestID, true, nil
}

func (s *Store) ContestLeaderboard(ctx context.Context, contestID string) ([]ContestLeaderboardEntry, error) {
	q := s.db.Adopt(`SELECT e.user_id, COALESCE(m.username, '') AS username, COALESCE(m.display_name, '') AS display_name,
		e.first_message_id FROM community_contest_entries e LEFT JOIN community_members m
		ON m.tenant_id=e.tenant_id AND m.chat_id=e.chat_id AND m.user_id=e.user_id
		WHERE e.tenant_id = ? AND e.contest_id = ?`)
	var entries []ContestLeaderboardEntry
	if err := s.db.SelectContext(ctx, &entries, q, s.db.TenantID(), contestID); err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Breakdown = map[string]int{}
		var rows []struct {
			Key   string `db:"reaction_key"`
			Total int    `db:"total_count"`
		}
		q = s.db.Adopt(`SELECT reaction_key, total_count FROM community_contest_reactions
			WHERE tenant_id = ? AND contest_id = ? AND message_id = ?`)
		if err := s.db.SelectContext(ctx, &rows, q, s.db.TenantID(), contestID, entries[i].MessageID); err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			q = s.db.Adopt(`SELECT reaction_key, COUNT(*) AS total_count FROM community_contest_reaction_actors
				WHERE tenant_id = ? AND contest_id = ? AND message_id = ? GROUP BY reaction_key`)
			if err := s.db.SelectContext(ctx, &rows, q, s.db.TenantID(), contestID, entries[i].MessageID); err != nil {
				return nil, err
			}
		}
		for _, row := range rows {
			entries[i].Breakdown[row.Key] = row.Total
			entries[i].ReactionTotal += row.Total
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ReactionTotal == entries[j].ReactionTotal {
			return entries[i].MessageID < entries[j].MessageID
		}
		return entries[i].ReactionTotal > entries[j].ReactionTotal
	})
	for i := range entries {
		entries[i].Position = i + 1
	}
	return entries, nil
}

func (s *Store) CreateContestAppealOffer(
	ctx context.Context, contestID string, msg eventsCommunityMessageAlias, originalMessageID int,
) (string, error) {
	token := randomToken(16)
	now := timestamp(msg.ReceivedAt)
	q := s.db.Adopt(`INSERT INTO community_contest_appeals
		(tenant_id, appeal_token, contest_id, chat_id, user_id, username, display_name,
		 original_message_id, duplicate_message_id, photo_file_id, caption, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'offered', ?, ?)
		ON CONFLICT (tenant_id, appeal_token) DO NOTHING`)
	_, err := s.db.ExecContext(ctx, q, s.db.TenantID(), token, contestID, msg.ChatID, msg.UserID,
		msg.UserName, msg.DisplayName, originalMessageID, msg.MessageID, msg.PhotoFileID, msg.Text, now, now)
	return token, err
}

// SubmitContestAppeal turns a private deep-link offer into an admin-review request.
func (s *Store) SubmitContestAppeal(ctx context.Context, token string, userID int64, text string) (ContestAppeal, error) {
	q := s.db.Adopt(`UPDATE community_contest_appeals SET status = 'open', appeal_text = ?, updated_at = ?
		WHERE tenant_id = ? AND appeal_token = ? AND user_id = ? AND status IN ('offered','open')`)
	res, err := s.db.ExecContext(ctx, q, strings.TrimSpace(text), time.Now().UTC(), s.db.TenantID(), token, userID)
	if err != nil {
		return ContestAppeal{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ContestAppeal{}, fmt.Errorf("apelación no válida o perteneciente a otro usuario")
	}
	return s.GetContestAppeal(ctx, token)
}

func (s *Store) OpenContestAppeal(ctx context.Context, token string, userID int64, text string) error {
	_, err := s.SubmitContestAppeal(ctx, token, userID, text)
	return err
}

func (s *Store) GetContestAppeal(ctx context.Context, token string) (ContestAppeal, error) {
	var appeal ContestAppeal
	q := s.db.Adopt(`SELECT appeal_token, contest_id, chat_id, user_id, username, display_name,
		original_message_id, duplicate_message_id, photo_file_id, caption, status, appeal_text,
		resolution_text, created_at, updated_at FROM community_contest_appeals
		WHERE tenant_id = ? AND appeal_token = ?`)
	err := s.db.GetContext(ctx, &appeal, q, s.db.TenantID(), token)
	return appeal, err
}

func (s *Store) ListContestAppeals(ctx context.Context, contestID string, limit int) ([]ContestAppeal, error) {
	where := "tenant_id = ? AND status <> 'offered'"
	args := []any{s.db.TenantID()}
	if strings.TrimSpace(contestID) != "" {
		where += " AND contest_id = ?"
		args = append(args, contestID)
	}
	args = append(args, boundedLimit(limit))
	q := s.db.Adopt(`SELECT appeal_token, contest_id, chat_id, user_id, username, display_name,
		original_message_id, duplicate_message_id, photo_file_id, caption, status, appeal_text,
		resolution_text, created_at, updated_at FROM community_contest_appeals WHERE ` + where + `
		ORDER BY created_at DESC LIMIT ?`)
	var appeals []ContestAppeal
	err := s.db.SelectContext(ctx, &appeals, q, args...)
	return appeals, err
}

func (s *Store) ResolveContestAppeal(
	ctx context.Context, token string, accepted bool, resolution string,
) (ContestAppeal, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	tx, beginErr := s.db.BeginTxx(ctx, &sql.TxOptions{})
	if beginErr != nil {
		return ContestAppeal{}, beginErr
	}
	defer tx.Rollback()
	var appeal ContestAppeal
	q := s.db.Adopt(`SELECT appeal_token, contest_id, chat_id, user_id, username, display_name,
		original_message_id, duplicate_message_id, photo_file_id, caption, status, appeal_text,
		resolution_text, created_at, updated_at FROM community_contest_appeals
		WHERE tenant_id = ? AND appeal_token = ?`)
	if err := tx.GetContext(ctx, &appeal, q, s.db.TenantID(), token); err != nil {
		return ContestAppeal{}, err
	}
	if appeal.Status != "open" {
		return ContestAppeal{}, fmt.Errorf("appeal is %s, expected open", appeal.Status)
	}
	status := "rejected"
	if accepted {
		status = "accepted"
	}
	q = s.db.Adopt(`UPDATE community_contest_appeals SET status = ?, resolution_text = ?, updated_at = ?
		WHERE tenant_id = ? AND appeal_token = ? AND status = 'open'`)
	if _, err := tx.ExecContext(
		ctx, q, status, strings.TrimSpace(resolution), time.Now().UTC(), s.db.TenantID(), token,
	); err != nil {
		return ContestAppeal{}, err
	}
	if accepted {
		q = s.db.Adopt(`DELETE FROM community_contest_entries
			WHERE tenant_id = ? AND contest_id = ? AND user_id = ? AND first_message_id = ?`)
		if _, err := tx.ExecContext(
			ctx, q, s.db.TenantID(), appeal.ContestID, appeal.UserID, appeal.OriginalMessageID,
		); err != nil {
			return ContestAppeal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ContestAppeal{}, err
	}
	appeal.Status, appeal.ResolutionText = status, strings.TrimSpace(resolution)
	return appeal, nil
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// Alias keeps contest storage independent of the events package import cycle.
type eventsCommunityMessageAlias struct {
	ChatID, UserID                           int64
	MessageID                                int
	UserName, DisplayName, PhotoFileID, Text string
	ReceivedAt                               time.Time
}

type ContestTelegram interface {
	Send(c tbapi.Chattable) (tbapi.Message, error)
	Request(c tbapi.Chattable) (*tbapi.APIResponse, error)
}

type ContestManager struct {
	store       *Store
	api         ContestTelegram
	chatID      int64
	settleDelay time.Duration
}

func NewContestManager(store *Store, api ContestTelegram, chatID int64) *ContestManager {
	return &ContestManager{store: store, api: api, chatID: chatID, settleDelay: 3 * time.Minute}
}

func (m *ContestManager) CreateDraft(ctx context.Context, input ContestDraftInput) (Contest, error) {
	return m.store.CreateContestDraft(ctx, input, m.chatID)
}
func (m *ContestManager) UpdateDraft(ctx context.Context, id, announcement string) error {
	return m.store.UpdateContestDraft(ctx, id, announcement)
}
func (m *ContestManager) List(ctx context.Context, limit int) ([]Contest, error) {
	return m.store.ListContests(ctx, limit)
}
func (m *ContestManager) Get(ctx context.Context, id string) (Contest, error) {
	return m.store.GetContest(ctx, id)
}
func (m *ContestManager) Leaderboard(ctx context.Context, id string) ([]ContestLeaderboardEntry, error) {
	return m.store.ContestLeaderboard(ctx, id)
}
func (m *ContestManager) Appeals(ctx context.Context, id string, limit int) ([]ContestAppeal, error) {
	return m.store.ListContestAppeals(ctx, id, limit)
}

func contestForum(chatID int64, threadID int) tbapi.BaseForum {
	return tbapi.BaseForum{
		ChatConfig:      tbapi.ChatConfig{ChatID: chatID},
		MessageThreadID: threadID,
	}
}

func (m *ContestManager) Publish(ctx context.Context, id string) (Contest, error) {
	if m.api == nil {
		return Contest{}, fmt.Errorf("telegram no está disponible en este proceso")
	}
	contest, err := m.store.setContestPublishing(ctx, id)
	if err != nil {
		return Contest{}, err
	}
	topicResp, err := m.api.Request(tbapi.CreateForumTopicConfig{
		ChatConfig: tbapi.ChatConfig{ChatID: contest.ChatID},
		Name:       contest.Title,
	})
	if err != nil {
		m.store.resetContestDraft(ctx, id)
		return Contest{}, fmt.Errorf("crear topic del concurso: %w", err)
	}
	var topic tbapi.ForumTopic
	if err = json.Unmarshal(topicResp.Result, &topic); err != nil || topic.MessageThreadID == 0 {
		m.store.resetContestDraft(ctx, id)
		return Contest{}, fmt.Errorf("interpretar topic creado: %w", err)
	}
	message := tbapi.NewMessage(contest.ChatID, contest.AnnouncementText)
	message.MessageThreadID = topic.MessageThreadID
	sent, err := m.api.Send(message)
	if err != nil {
		_, _ = m.api.Request(tbapi.DeleteForumTopicConfig{
			BaseForum: contestForum(contest.ChatID, topic.MessageThreadID),
		})
		m.store.resetContestDraft(ctx, id)
		return Contest{}, fmt.Errorf("publicar bases: %w", err)
	}
	_, err = m.api.Request(tbapi.NewPinChatMessage(contest.ChatID, sent.MessageID, true))
	if err != nil {
		_, _ = m.api.Request(tbapi.DeleteForumTopicConfig{
			BaseForum: contestForum(contest.ChatID, topic.MessageThreadID),
		})
		m.store.resetContestDraft(ctx, id)
		return Contest{}, fmt.Errorf("fijar bases: %w", err)
	}
	if err := m.store.markContestActive(ctx, id, topic.MessageThreadID, sent.MessageID); err != nil {
		_, _ = m.api.Request(tbapi.DeleteForumTopicConfig{
			BaseForum: contestForum(contest.ChatID, topic.MessageThreadID),
		})
		m.store.resetContestDraft(ctx, id)
		return Contest{}, err
	}
	return m.store.GetContest(ctx, id)
}

func (m *ContestManager) BeginFinalize(ctx context.Context, id string) (Contest, error) {
	if m.api == nil {
		return Contest{}, fmt.Errorf("telegram no está disponible en este proceso")
	}
	contest, err := m.store.GetContest(ctx, id)
	if err != nil {
		return Contest{}, err
	}
	if contest.Status != ContestActive {
		return Contest{}, fmt.Errorf("el concurso no está activo")
	}
	if _, err = m.api.Request(tbapi.CloseForumTopicConfig{
		BaseForum: contestForum(contest.ChatID, contest.ThreadID),
	}); err != nil {
		return Contest{}, fmt.Errorf("cerrar topic: %w", err)
	}
	now := time.Now().UTC()
	if err := m.store.markContestClosing(ctx, id, now, now.Add(m.settleDelay)); err != nil {
		return Contest{}, err
	}
	return m.store.GetContest(ctx, id)
}

func (m *ContestManager) ConfirmFinalize(ctx context.Context, id string) (Contest, error) {
	if m.api == nil {
		return Contest{}, fmt.Errorf("telegram no está disponible en este proceso")
	}
	contest, err := m.store.GetContest(ctx, id)
	if err != nil {
		return Contest{}, err
	}
	if contest.Status != ContestClosing || contest.ConsolidateAfter == nil {
		return Contest{}, fmt.Errorf("el concurso no está pendiente de confirmación")
	}
	if time.Now().UTC().Before(*contest.ConsolidateAfter) {
		return Contest{}, fmt.Errorf(
			"espera hasta %s para consolidar las reacciones retrasadas",
			contest.ConsolidateAfter.In(time.Local).Format("15:04:05"),
		)
	}
	leaders, err := m.store.ContestLeaderboard(ctx, id)
	if err != nil {
		return Contest{}, err
	}
	snapshot, _ := json.Marshal(leaders)
	winner := ContestLeaderboardEntry{}
	if len(leaders) > 0 {
		winner = leaders[0]
	}
	if _, err = m.api.Request(tbapi.DeleteForumTopicConfig{
		BaseForum: contestForum(contest.ChatID, contest.ThreadID),
	}); err != nil {
		return Contest{}, fmt.Errorf("borrar topic y conversación: %w", err)
	}
	if err := m.store.markContestFinalized(ctx, id, string(snapshot), winner); err != nil {
		return Contest{}, err
	}
	return m.store.GetContest(ctx, id)
}

func (m *ContestManager) ResolveAppeal(
	ctx context.Context, token string, accepted bool, resolution string,
) (ContestAppeal, error) {
	appeal, err := m.store.ResolveContestAppeal(ctx, token, accepted, resolution)
	if err != nil {
		return ContestAppeal{}, err
	}
	if m.api != nil {
		text := "Tu apelación del concurso ha sido rechazada."
		if accepted {
			text = "Tu apelación del concurso ha sido aceptada. " +
				"Se ha liberado tu participación y ya puedes subir una nueva foto."
		}
		if strings.TrimSpace(resolution) != "" {
			text += "\n\n" + strings.TrimSpace(resolution)
		}
		_, _ = m.api.Send(tbapi.NewMessage(appeal.UserID, text))
	}
	return appeal, nil
}
