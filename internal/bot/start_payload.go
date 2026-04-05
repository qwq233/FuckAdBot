package bot

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const verificationStartPayloadPrefix = "verify"

func BuildVerificationStartPayload(chatID, userID, verificationInfoID int64) string {
	return fmt.Sprintf("%s_%d_%d_%d", verificationStartPayloadPrefix, chatID, userID, verificationInfoID)
}

func ParseVerificationStartPayload(payload string) (int64, int64, int64, error) {
	parts := strings.Split(payload, "_")
	if len(parts) != 4 || parts[0] != verificationStartPayloadPrefix {
		return 0, 0, 0, fmt.Errorf("invalid verification start payload")
	}

	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid chat id: %w", err)
	}

	userID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid user id: %w", err)
	}

	verificationInfoID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid verification info id: %w", err)
	}

	return chatID, userID, verificationInfoID, nil
}

func BuildVerificationStartURL(botUsername string, chatID, userID, verificationInfoID int64) string {
	payload := BuildVerificationStartPayload(chatID, userID, verificationInfoID)
	return fmt.Sprintf("https://t.me/%s?start=%s", botUsername, url.QueryEscape(payload))
}
