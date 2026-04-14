package store

import (
	"fmt"

	"github.com/qwq233/fuckadbot/internal/config"
)

// ErrorReporter is implemented by stores that can surface asynchronous fatal
// runtime errors to the application.
type ErrorReporter interface {
	Errors() <-chan error
}

// SnapshotStore exposes authoritative SQLite state for cache rebuilds.
type SnapshotStore interface {
	ListUserLanguagePreferences() ([]LanguagePreferenceRecord, error)
	ListUserStatuses() ([]UserStatusRecord, error)
	ListWarnings() ([]WarningRecord, error)
	ListPendingVerifications() ([]PendingVerification, error)
	GetAllBlacklistWords() (map[int64][]string, error)
}

type UserStatusRecord struct {
	ChatID int64
	UserID int64
	Status string
}

type WarningRecord struct {
	ChatID int64
	UserID int64
	Count  int
}

type LanguagePreferenceRecord struct {
	UserID   int64
	Language string
}

func NewFromConfig(cfg config.StoreConfig) (Store, error) {
	switch cfg.Type {
	case "sqlite":
		if cfg.DualWriteEnabled {
			return NewDualWriteStore(cfg)
		}
		return NewSQLiteStore(cfg.SQLitePath())
	case "redis":
		return NewRedisStore(cfg)
	default:
		return nil, fmt.Errorf("unsupported store type: %s", cfg.Type)
	}
}
