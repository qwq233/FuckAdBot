package store

import "time"

type PendingVerification struct {
	ChatID   int64
	UserID   int64
	ExpireAt time.Time
}

type Store interface {
	Close() error

	// User verification status: verified / rejected / none
	IsVerified(chatID, userID int64) (bool, error)
	SetVerified(chatID, userID int64) error
	RemoveVerified(chatID, userID int64) error

	IsRejected(chatID, userID int64) (bool, error)
	SetRejected(chatID, userID int64) error
	RemoveRejected(chatID, userID int64) error

	// Pending verification window
	HasActivePending(chatID, userID int64) (bool, error)
	SetPending(chatID, userID int64, expireAt time.Time) error
	ClearPending(chatID, userID int64) error

	// Warning count
	GetWarningCount(chatID, userID int64) (int, error)
	IncrWarningCount(chatID, userID int64) (int, error) // returns new count
	ResetWarningCount(chatID, userID int64) error

	// Blacklist words
	GetBlacklistWords() ([]string, error)
	AddBlacklistWord(word, addedBy string) error
	RemoveBlacklistWord(word string) error
}
