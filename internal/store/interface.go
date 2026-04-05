package store

import "time"

type PendingVerification struct {
	ChatID            int64
	UserID            int64
	Timestamp         int64
	RandomToken       string
	ExpireAt          time.Time
	ReminderMessageID int64
	MessageThreadID   int64
	ReplyToMessageID  int64
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
	GetPending(chatID, userID int64) (*PendingVerification, error)
	SetPending(pending PendingVerification) error
	ClearPending(chatID, userID int64) error

	// Warning count
	GetWarningCount(chatID, userID int64) (int, error)
	IncrWarningCount(chatID, userID int64) (int, error) // returns new count
	ResetWarningCount(chatID, userID int64) error

	// Blacklist words (chatID=0 for global, specific chatID for group-scoped)
	GetBlacklistWords(chatID int64) ([]string, error)
	AddBlacklistWord(chatID int64, word, addedBy string) error
	RemoveBlacklistWord(chatID int64, word string) error
	GetAllBlacklistWords() (map[int64][]string, error)
}
