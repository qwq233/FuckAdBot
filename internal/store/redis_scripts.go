package store

import "github.com/redis/go-redis/v9"

var reserveVerificationWindowScript = redis.NewScript(`
local pendingKey = KEYS[1]
local statusKey = KEYS[2]
local warningKey = KEYS[3]
local pendingIndexKey = KEYS[4]
local userChatsKey = KEYS[5]

local maxWarnings = tonumber(ARGV[13]) or 0
local warningCount = tonumber(redis.call("GET", warningKey) or "0") or 0
if warningCount >= maxWarnings then
	return {3, warningCount}
end

if redis.call("EXISTS", pendingKey) == 1 then
	local userLanguage = redis.call("HGET", pendingKey, "user_language") or ""
	local timestamp = redis.call("HGET", pendingKey, "timestamp") or "0"
	local randomToken = redis.call("HGET", pendingKey, "random_token") or ""
	local expireAtUnix = redis.call("HGET", pendingKey, "expire_at_unix") or "0"
	local reminderMessageID = redis.call("HGET", pendingKey, "reminder_message_id") or "0"
	local privateMessageID = redis.call("HGET", pendingKey, "private_message_id") or "0"
	local originalMessageID = redis.call("HGET", pendingKey, "original_message_id") or "0"
	local messageThreadID = redis.call("HGET", pendingKey, "message_thread_id") or "0"
	local replyToMessageID = redis.call("HGET", pendingKey, "reply_to_message_id") or "0"
	return {
		2,
		warningCount,
		userLanguage,
		timestamp,
		randomToken,
		expireAtUnix,
		reminderMessageID,
		privateMessageID,
		originalMessageID,
		messageThreadID,
		replyToMessageID,
	}
end

local status = redis.call("GET", statusKey)
if status == "verified" or status == "rejected" then
	return {0, warningCount}
end

redis.call("HSET", pendingKey,
	"chat_id", ARGV[2],
	"user_id", ARGV[3],
	"user_language", ARGV[4],
	"timestamp", ARGV[5],
	"random_token", ARGV[6],
	"expire_at_unix", ARGV[7],
	"reminder_message_id", ARGV[8],
	"private_message_id", ARGV[9],
	"original_message_id", ARGV[10],
	"message_thread_id", ARGV[11],
	"reply_to_message_id", ARGV[12]
)
redis.call("ZADD", pendingIndexKey, ARGV[7], ARGV[1])
redis.call("SADD", userChatsKey, ARGV[2])

return {1, warningCount}
`)

var createPendingIfAbsentScript = redis.NewScript(`
local pendingKey = KEYS[1]
local statusKey = KEYS[2]
local pendingIndexKey = KEYS[3]
local userChatsKey = KEYS[4]

if redis.call("EXISTS", pendingKey) == 1 then
	return {2}
end

local status = redis.call("GET", statusKey)
if status == "verified" or status == "rejected" then
	return {0}
end

redis.call("HSET", pendingKey,
	"chat_id", ARGV[2],
	"user_id", ARGV[3],
	"user_language", ARGV[4],
	"timestamp", ARGV[5],
	"random_token", ARGV[6],
	"expire_at_unix", ARGV[7],
	"reminder_message_id", ARGV[8],
	"private_message_id", ARGV[9],
	"original_message_id", ARGV[10],
	"message_thread_id", ARGV[11],
	"reply_to_message_id", ARGV[12]
)
redis.call("ZADD", pendingIndexKey, ARGV[7], ARGV[1])
redis.call("SADD", userChatsKey, ARGV[2])

return {1}
`)

var updatePendingMetadataByTokenScript = redis.NewScript(`
local pendingKey = KEYS[1]
local pendingIndexKey = KEYS[2]

if redis.call("EXISTS", pendingKey) == 0 then
	return 0
end

if redis.call("HGET", pendingKey, "timestamp") ~= ARGV[2] then
	return 0
end
if redis.call("HGET", pendingKey, "random_token") ~= ARGV[3] then
	return 0
end

redis.call("HSET", pendingKey,
	"user_language", ARGV[4],
	"expire_at_unix", ARGV[5],
	"reminder_message_id", ARGV[6],
	"private_message_id", ARGV[7],
	"original_message_id", ARGV[8],
	"message_thread_id", ARGV[9],
	"reply_to_message_id", ARGV[10]
)
redis.call("ZADD", pendingIndexKey, ARGV[5], ARGV[1])

return 1
`)

var incrWarningCountScript = redis.NewScript(`
local warningKey = KEYS[1]
local userChatsKey = KEYS[2]

local count = redis.call("INCR", warningKey)
redis.call("SADD", userChatsKey, ARGV[1])

return count
`)

var resolvePendingByTokenScript = redis.NewScript(`
local pendingKey = KEYS[1]
local statusKey = KEYS[2]
local warningKey = KEYS[3]
local pendingIndexKey = KEYS[4]
local userChatsKey = KEYS[5]

if redis.call("EXISTS", pendingKey) == 0 then
	return {0}
end

local currentTimestamp = redis.call("HGET", pendingKey, "timestamp")
local currentRandomToken = redis.call("HGET", pendingKey, "random_token")
if currentTimestamp ~= ARGV[3] or currentRandomToken ~= ARGV[4] then
	return {0}
end

local action = ARGV[5]
if action ~= "approve" and action ~= "reject" and action ~= "expire" and action ~= "cancel" then
	return {-1}
end

local userLanguage = redis.call("HGET", pendingKey, "user_language") or ""
local expireAtUnix = redis.call("HGET", pendingKey, "expire_at_unix") or "0"
local reminderMessageID = redis.call("HGET", pendingKey, "reminder_message_id") or "0"
local privateMessageID = redis.call("HGET", pendingKey, "private_message_id") or "0"
local originalMessageID = redis.call("HGET", pendingKey, "original_message_id") or "0"
local messageThreadID = redis.call("HGET", pendingKey, "message_thread_id") or "0"
local replyToMessageID = redis.call("HGET", pendingKey, "reply_to_message_id") or "0"

redis.call("DEL", pendingKey)
redis.call("ZREM", pendingIndexKey, ARGV[1])

local verified = 0
local rejected = 0
local warningCount = 0
local shouldBan = 0
local maxWarnings = tonumber(ARGV[6])

if action == "approve" then
	redis.call("SET", statusKey, "verified")
	redis.call("DEL", warningKey)
	redis.call("SADD", userChatsKey, ARGV[2])
	verified = 1
elseif action == "reject" then
	redis.call("SET", statusKey, "rejected")
	redis.call("DEL", warningKey)
	redis.call("SADD", userChatsKey, ARGV[2])
	rejected = 1
elseif action == "expire" then
	local status = redis.call("GET", statusKey)
	if status == "verified" then
		verified = 1
	elseif status == "rejected" then
		rejected = 1
	else
		warningCount = tonumber(redis.call("INCR", warningKey))
		redis.call("SADD", userChatsKey, ARGV[2])
		if maxWarnings > 0 and warningCount >= maxWarnings then
			shouldBan = 1
		end
	end
elseif action == "cancel" then
	-- no-op
end

return {
	1,
	userLanguage,
	currentTimestamp,
	currentRandomToken,
	expireAtUnix,
	reminderMessageID,
	privateMessageID,
	originalMessageID,
	messageThreadID,
	replyToMessageID,
	verified,
	rejected,
	warningCount,
	shouldBan
}
`)
