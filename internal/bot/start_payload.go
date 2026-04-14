package bot

import (
	"fmt"
	"strconv"
	"strings"
)

const verificationStartPayloadPrefix = "verify"

func BuildVerificationStartPayload(chatID, userID, verificationInfoID int64) string {
	buf := make([]byte, 0, len(verificationStartPayloadPrefix)+3+(3*20))
	return string(appendVerificationStartPayload(buf, chatID, userID, verificationInfoID))
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
	buf := make([]byte, 0, len("https://t.me/")+len(botUsername)+len("?start=")+len(verificationStartPayloadPrefix)+3+(3*20))
	buf = append(buf, "https://t.me/"...)
	buf = append(buf, botUsername...)
	buf = append(buf, "?start="...)
	return string(appendVerificationStartPayload(buf, chatID, userID, verificationInfoID))
}

func appendVerificationStartPayload(dst []byte, chatID, userID, verificationInfoID int64) []byte {
	dst = append(dst, verificationStartPayloadPrefix...)
	dst = append(dst, '_')
	dst = strconv.AppendInt(dst, chatID, 10)
	dst = append(dst, '_')
	dst = strconv.AppendInt(dst, userID, 10)
	dst = append(dst, '_')
	dst = strconv.AppendInt(dst, verificationInfoID, 10)
	return dst
}
