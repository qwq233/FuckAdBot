package store

import "time"

type PendingVerification struct {
	ChatID            int64
	UserID            int64
	UserLanguage      string
	Timestamp         int64
	RandomToken       string
	ExpireAt          time.Time
	ReminderMessageID int64
	PrivateMessageID  int64
	OriginalMessageID int64
	MessageThreadID   int64
	ReplyToMessageID  int64
}

type PendingAction string

const (
	PendingActionApprove PendingAction = "approve"
	PendingActionReject  PendingAction = "reject"
	PendingActionExpire  PendingAction = "expire"
	PendingActionCancel  PendingAction = "cancel"
)

type PendingResolutionResult struct {
	Matched      bool
	Action       PendingAction
	Pending      *PendingVerification
	Verified     bool
	Rejected     bool
	WarningCount int
	ShouldBan    bool
}

type Store interface {
	Close() error

	// User preferences
	GetUserLanguagePreference(userID int64) (string, error)
	SetUserLanguagePreference(userID int64, language string) error

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
	ListPendingVerifications() ([]PendingVerification, error)
	CreatePendingIfAbsent(pending PendingVerification) (created bool, existing *PendingVerification, err error)
	SetPending(pending PendingVerification) error
	UpdatePendingMetadataByToken(pending PendingVerification) (updated bool, err error)
	ClearPending(chatID, userID int64) error
	ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action PendingAction, maxWarnings int) (PendingResolutionResult, error)
	ClearUserVerificationStateEverywhere(userID int64) error

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
