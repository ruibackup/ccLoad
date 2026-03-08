package sql_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"ccLoad/internal/model"
	sqlstore "ccLoad/internal/storage/sql"

	_ "modernc.org/sqlite"
)

func newJSONTime(t time.Time) model.JSONTime {
	return model.JSONTime{Time: t}
}

func TestLog_AddAndList(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "logs.db")

	ctx := context.Background()
	channelID := createTestChannel(t, ctx, store, "log-test-channel")

	now := time.Now()
	log := &model.LogEntry{
		Time:        newJSONTime(now),
		Model:       "gpt-4",
		ChannelID:   channelID,
		StatusCode:  200,
		Message:     "success",
		Duration:    1.5,
		IsStreaming: false,
		APIKeyUsed:  "abcd...efgh",
	}
	if err := store.AddLog(ctx, log); err != nil {
		t.Fatalf("add log: %v", err)
	}
	// AddLog 方法不返回 ID，不需要检查

	since := now.Add(-1 * time.Hour)
	logs, err := store.ListLogs(ctx, since, 10, 0, nil)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}
	if len(logs) > 0 && logs[0].Model != "gpt-4" {
		t.Errorf("model: got %q, want %q", logs[0].Model, "gpt-4")
	}
}

func TestLog_BatchAdd(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "batch_logs.db")

	ctx := context.Background()
	channelID := createTestChannel(t, ctx, store, "batch-log-channel")

	now := time.Now()
	logs := []*model.LogEntry{
		{
			Time:       newJSONTime(now),
			Model:      "gpt-4",
			ChannelID:  channelID,
			StatusCode: 200,
			Message:    "success 1",
			Duration:   1.0,
			APIKeyUsed: "key1...1key",
		},
		{
			Time:       newJSONTime(now),
			Model:      "claude-3",
			ChannelID:  channelID,
			StatusCode: 200,
			Message:    "success 2",
			Duration:   2.0,
			APIKeyUsed: "key2...2key",
		},
		{
			Time:       newJSONTime(now),
			Model:      "gpt-4",
			ChannelID:  channelID,
			StatusCode: 500,
			Message:    "error",
			Duration:   0.5,
			APIKeyUsed: "key3...3key",
		},
	}

	if err := store.BatchAddLogs(ctx, logs); err != nil {
		t.Fatalf("batch add logs: %v", err)
	}
	// BatchAddLogs 方法不返回 ID，不需要检查

	since := now.Add(-1 * time.Hour)
	count, err := store.CountLogs(ctx, since, nil)
	if err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 logs, got %d", count)
	}
}

func TestLog_ListRange(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "range_logs.db")

	ctx := context.Background()
	channelID := createTestChannel(t, ctx, store, "range-log-channel")

	now := time.Now()
	logs := []*model.LogEntry{
		{
			Time:       newJSONTime(now.Add(-2 * time.Hour)),
			Model:      "old-model",
			ChannelID:  channelID,
			StatusCode: 200,
			Message:    "old log",
			Duration:   1.0,
			APIKeyUsed: "key1...1key",
		},
		{
			Time:       newJSONTime(now.Add(-30 * time.Minute)),
			Model:      "recent-model",
			ChannelID:  channelID,
			StatusCode: 200,
			Message:    "recent log",
			Duration:   1.0,
			APIKeyUsed: "key2...2key",
		},
	}
	if err := store.BatchAddLogs(ctx, logs); err != nil {
		t.Fatalf("batch add logs: %v", err)
	}

	startTime := now.Add(-1 * time.Hour)
	endTime := now

	rangeLogs, err := store.ListLogsRange(ctx, startTime, endTime, 100, 0, nil)
	if err != nil {
		t.Fatalf("list logs range: %v", err)
	}
	if len(rangeLogs) != 1 {
		t.Errorf("expected 1 log in range, got %d", len(rangeLogs))
	}
	if len(rangeLogs) > 0 && rangeLogs[0].Model != "recent-model" {
		t.Errorf("model: got %q, want %q", rangeLogs[0].Model, "recent-model")
	}

	rangeCount, err := store.CountLogsRange(ctx, startTime, endTime, nil)
	if err != nil {
		t.Fatalf("count logs range: %v", err)
	}
	if rangeCount != 1 {
		t.Errorf("expected 1 log in range count, got %d", rangeCount)
	}
}

func TestLog_Pagination(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "pagination_logs.db")

	ctx := context.Background()
	channelID := createTestChannel(t, ctx, store, "pagination-channel")

	now := time.Now()
	logs := make([]*model.LogEntry, 10)
	for i := 0; i < 10; i++ {
		logs[i] = &model.LogEntry{
			Time:       newJSONTime(now),
			Model:      "gpt-4",
			ChannelID:  channelID,
			StatusCode: 200,
			Message:    "log " + string(rune('0'+i)),
			Duration:   float64(i),
			APIKeyUsed: "key...key",
		}
	}
	if err := store.BatchAddLogs(ctx, logs); err != nil {
		t.Fatalf("batch add logs: %v", err)
	}

	since := now.Add(-1 * time.Hour)

	page1, err := store.ListLogs(ctx, since, 5, 0, nil)
	if err != nil {
		t.Fatalf("list logs page 1: %v", err)
	}
	if len(page1) != 5 {
		t.Errorf("page 1: expected 5 logs, got %d", len(page1))
	}

	page2, err := store.ListLogs(ctx, since, 5, 5, nil)
	if err != nil {
		t.Fatalf("list logs page 2: %v", err)
	}
	if len(page2) != 5 {
		t.Errorf("page 2: expected 5 logs, got %d", len(page2))
	}

	seen := make(map[int64]struct{}, len(page1))
	for _, entry := range page1 {
		seen[entry.ID] = struct{}{}
	}
	for _, entry := range page2 {
		if _, ok := seen[entry.ID]; ok {
			t.Fatalf("pages should not overlap, overlapping id=%d", entry.ID)
		}
	}
}

func TestLog_ListRange_CompatibleWithLegacySchema(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy_logs.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		CREATE TABLE logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time INTEGER NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			channel_id INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT '',
			duration REAL NOT NULL DEFAULT 0,
			is_streaming INTEGER NOT NULL DEFAULT 0,
			first_byte_time REAL NOT NULL DEFAULT 0,
			api_key_used TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		t.Fatalf("create legacy logs table: %v", err)
	}

	now := time.Now().UnixMilli()
	_, err = db.ExecContext(ctx, `
		INSERT INTO logs(time, model, channel_id, status_code, message, duration, is_streaming, first_byte_time, api_key_used)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, now, "gpt-4", 0, 200, "legacy row", 1.25, 0, 0.33, "sk-legacy")
	if err != nil {
		t.Fatalf("insert legacy log: %v", err)
	}

	store := sqlstore.NewSQLStore(db, "sqlite")
	logs, err := store.ListLogsRange(ctx, time.UnixMilli(now-60_000), time.UnixMilli(now+60_000), 10, 0, nil)
	if err != nil {
		t.Fatalf("list logs range with legacy schema: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 legacy log, got %d", len(logs))
	}
	if logs[0].Model != "gpt-4" {
		t.Fatalf("expected model gpt-4, got %q", logs[0].Model)
	}
	if logs[0].BaseURL != "" || logs[0].RequestBody != "" || logs[0].ResponseBody != "" {
		t.Fatalf("expected missing legacy fields to fallback to zero values, got base_url=%q request=%q response=%q", logs[0].BaseURL, logs[0].RequestBody, logs[0].ResponseBody)
	}
}
