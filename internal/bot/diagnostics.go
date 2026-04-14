package bot

import (
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/store"
)

const diagnosticsCacheTTL = time.Second

var byteSuffixes = [...]string{"KiB", "MiB", "GiB", "TiB"}

type botDiagnosticsSnapshot struct {
	expiresAt       time.Time
	runtimeSnapshot botRuntimeSnapshot
	memStats        runtime.MemStats
	pendingBacklog  int
	blacklistScopes int
	blacklistWords  int
	storeStats      store.RuntimeStats
	captchaStats    captcha.RuntimeStats
}

func (b *Bot) cmdHealth(bot *gotgbot.Bot, ctx *ext.Context) error {
	return b.handleRuntimeStatsCommand(bot, ctx, true)
}

func (b *Bot) handleRuntimeStatsCommand(bot *gotgbot.Bot, ctx *ext.Context, compact bool) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.From == nil || !b.isBotAdmin(msg.From.Id) {
		return nil
	}

	requestLanguage := b.requestLanguageForUser(msg.From)
	var reply string
	if compact {
		reply = b.renderHealthReply(requestLanguage)
	} else {
		reply = b.renderStatsReply(requestLanguage)
	}

	_, err := bot.SendMessage(msg.Chat.Id, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})
	return err
}

func (b *Bot) renderHealthReply(locale string) string {
	diag := b.collectDiagnostics()
	storeMode := b.storeMode()

	var builder strings.Builder
	builder.Grow(320 + len(diag.runtimeSnapshot.RecentErrors)*64)
	builder.WriteString("✅ <b>")
	builder.WriteString(escapeHTML(tr(locale, "health_title")))
	builder.WriteString("</b>\n")
	appendCodeLine(&builder, tr(locale, "diag_store_mode"), storeMode)
	appendCodeLine(&builder, tr(locale, "diag_uptime"), time.Since(diag.runtimeSnapshot.StartedAt).Round(time.Second).String())
	appendCodeLine(&builder, tr(locale, "diag_pending_backlog"), formatCountOrUnknown(diag.pendingBacklog, locale))
	appendCodeLine(&builder, tr(locale, "diag_queue_depth"), formatQueueDepth(diag.storeStats, locale))
	appendIntCodeLine(&builder, tr(locale, "diag_recent_errors"), len(diag.runtimeSnapshot.RecentErrors))
	appendTripleUint64CodeLine(&builder, tr(locale, "diag_captcha_summary"), diag.captchaStats.Successes, diag.captchaStats.Failures, diag.captchaStats.Timeouts)
	appendIntCodeLine(&builder, tr(locale, "diag_blacklist_words"), diag.blacklistWords)
	appendIntCodeLine(&builder, tr(locale, "diag_blacklist_scopes"), diag.blacklistScopes)
	appendCodeLine(&builder, tr(locale, "diag_heap"), formatBytes(diag.memStats.HeapAlloc))

	return trimTrailingNewline(builder.String())
}

func (b *Bot) renderStatsReply(locale string) string {
	diag := b.collectDiagnostics()
	storeMode := b.storeMode()

	var builder strings.Builder
	builder.Grow(640 + len(diag.runtimeSnapshot.RecentErrors)*80)
	builder.WriteString("📊 <b>")
	builder.WriteString(escapeHTML(tr(locale, "stats_title")))
	builder.WriteString("</b>\n")
	appendCodeLine(&builder, tr(locale, "diag_store_mode"), storeMode)
	appendCodeLine(&builder, tr(locale, "diag_uptime"), time.Since(diag.runtimeSnapshot.StartedAt).Round(time.Second).String())
	appendIntCodeLine(&builder, tr(locale, "diag_goroutines"), runtime.NumGoroutine())
	appendCodeLine(&builder, tr(locale, "diag_heap"), formatBytes(diag.memStats.HeapAlloc))
	appendUint32CodeLine(&builder, tr(locale, "diag_gc_cycles"), diag.memStats.NumGC)
	appendCodeLine(&builder, tr(locale, "diag_pending_backlog"), formatCountOrUnknown(diag.pendingBacklog, locale))
	appendCodeLine(&builder, tr(locale, "diag_last_sweeper"), formatSweeperStatus(diag.runtimeSnapshot, locale))
	appendCodeLine(&builder, tr(locale, "diag_queue_depth"), formatQueueDepth(diag.storeStats, locale))
	appendCodeLine(&builder, tr(locale, "diag_last_flush"), formatStoreOperationStatus(diag.storeStats.LastFlushAt, diag.storeStats.LastFlushError, locale))
	appendCodeLine(&builder, tr(locale, "diag_last_replay"), formatStoreOperationStatus(diag.storeStats.LastReplayAt, diag.storeStats.LastReplayError, locale))
	appendCodeLine(&builder, tr(locale, "diag_last_rebuild"), formatStoreOperationStatus(diag.storeStats.LastRebuildAt, diag.storeStats.LastRebuildError, locale))
	appendIntCodeLine(&builder, tr(locale, "diag_blacklist_scopes"), diag.blacklistScopes)
	appendIntCodeLine(&builder, tr(locale, "diag_blacklist_words"), diag.blacklistWords)
	appendTripleUint64CodeLine(&builder, tr(locale, "diag_admin_cache_summary"), diag.runtimeSnapshot.AdminCacheHits, diag.runtimeSnapshot.AdminCacheMisses, diag.runtimeSnapshot.AdminCacheErrors)
	appendTripleUint64CodeLine(&builder, tr(locale, "diag_captcha_summary"), diag.captchaStats.Successes, diag.captchaStats.Failures, diag.captchaStats.Timeouts)
	builder.WriteString(tr(locale, "diag_recent_errors"))
	builder.WriteString(":\n")
	builder.WriteString(formatRecentErrors(diag.runtimeSnapshot.RecentErrors, locale))

	return trimTrailingNewline(builder.String())
}

func (b *Bot) collectDiagnostics() botDiagnosticsSnapshot {
	now := time.Now().UTC()

	b.diagnosticsMu.RLock()
	cached := b.diagnostics
	b.diagnosticsMu.RUnlock()
	if !cached.expiresAt.IsZero() && cached.expiresAt.After(now) {
		return cached
	}

	refreshed := b.collectDiagnosticsFresh(now)

	b.diagnosticsMu.Lock()
	if b.diagnostics.expiresAt.After(now) {
		refreshed = b.diagnostics
	} else {
		b.diagnostics = refreshed
	}
	b.diagnosticsMu.Unlock()

	return refreshed
}

func (b *Bot) collectDiagnosticsFresh(now time.Time) botDiagnosticsSnapshot {
	b.ensureRuntimeState()
	runtimeSnapshot := b.runtimeStats.snapshot()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	pendingBacklog := -1
	if b != nil && b.Store != nil {
		pendingVerifications, err := b.Store.ListPendingVerifications()
		if err != nil {
			b.runtimeStats.recordErrorf("diagnostics pending backlog: %v", err)
		} else {
			pendingBacklog = len(pendingVerifications)
		}
	}

	blacklistScopes := 0
	blacklistWords := 0
	if b != nil && b.Store != nil {
		allWords, err := b.Store.GetAllBlacklistWords()
		if err != nil {
			b.runtimeStats.recordErrorf("diagnostics blacklist counts: %v", err)
		} else {
			for chatID, words := range allWords {
				if chatID != 0 {
					blacklistScopes++
				}
				blacklistWords += len(words)
			}
		}
	}

	var storeStats store.RuntimeStats
	if reporter, ok := b.Store.(store.RuntimeStatsReporter); ok {
		storeStats = reporter.RuntimeStats()
	} else {
		storeStats.Mode = b.storeMode()
		storeStats.QueueDepth = -1
	}

	var captchaStats captcha.RuntimeStats
	if reporter, ok := b.Captcha.(captcha.RuntimeStatsReporter); ok {
		captchaStats = reporter.RuntimeStats()
	}

	return botDiagnosticsSnapshot{
		expiresAt:       now.Add(diagnosticsCacheTTL),
		runtimeSnapshot: runtimeSnapshot,
		memStats:        memStats,
		pendingBacklog:  pendingBacklog,
		blacklistScopes: blacklistScopes,
		blacklistWords:  blacklistWords,
		storeStats:      storeStats,
		captchaStats:    captchaStats,
	}
}

func (b *Bot) storeMode() string {
	if b == nil || b.Config == nil {
		return "unknown"
	}

	if b.Config.Store.Type == "sqlite" && b.Config.Store.DualWriteEnabled {
		return "sqlite+redis-cache"
	}
	return b.Config.Store.Type
}

func formatSweeperStatus(snapshot botRuntimeSnapshot, locale string) string {
	if snapshot.LastSweeperRunAt.IsZero() {
		return tr(locale, "diag_unknown")
	}

	var builder strings.Builder
	builder.Grow(len(time.RFC3339) + 40)
	builder.WriteString(snapshot.LastSweeperRunAt.Format(time.RFC3339))
	builder.WriteString(" dur=")
	builder.WriteString(snapshot.LastSweeperDuration.Round(time.Millisecond).String())
	builder.WriteString(" scanned=")
	builder.WriteString(strconv.Itoa(snapshot.LastSweeperScanned))
	builder.WriteString(" expired=")
	builder.WriteString(strconv.Itoa(snapshot.LastSweeperExpired))
	return builder.String()
}

func formatStoreOperationStatus(at time.Time, errText, locale string) string {
	if at.IsZero() {
		return tr(locale, "diag_unknown")
	}
	formattedAt := at.Format(time.RFC3339)
	if errText != "" {
		var builder strings.Builder
		builder.Grow(len(formattedAt) + len(errText) + len(" error="))
		builder.WriteString(formattedAt)
		builder.WriteString(" error=")
		builder.WriteString(errText)
		return builder.String()
	}
	return formattedAt + " ok"
}

func formatQueueDepth(stats store.RuntimeStats, locale string) string {
	if stats.QueueDepthError != "" {
		return tr(locale, "diag_unknown") + " (" + stats.QueueDepthError + ")"
	}
	if stats.QueueDepth < 0 {
		return tr(locale, "diag_unknown")
	}
	return strconv.Itoa(stats.QueueDepth)
}

func formatCountOrUnknown(value int, locale string) string {
	if value < 0 {
		return tr(locale, "diag_unknown")
	}
	return strconv.Itoa(value)
}

func formatRecentErrors(recentErrors []string, locale string) string {
	if len(recentErrors) == 0 {
		return escapeHTML(tr(locale, "diag_none"))
	}

	var builder strings.Builder
	size := 0
	for _, entry := range recentErrors {
		size += len(entry) + 16
	}
	builder.Grow(size)
	for index, entry := range recentErrors {
		builder.WriteString(strconv.Itoa(index + 1))
		builder.WriteString(". <code>")
		builder.WriteString(escapeHTML(entry))
		builder.WriteString("</code>\n")
	}
	return trimTrailingNewline(builder.String())
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatUint(bytes, 10) + " B"
	}

	value := float64(bytes)
	value /= unit
	suffixIndex := 0
	for value >= unit && suffixIndex < len(byteSuffixes)-1 {
		value /= unit
		suffixIndex++
	}

	return strconv.FormatFloat(value, 'f', 1, 64) + " " + byteSuffixes[suffixIndex]
}

func appendCodeLine(builder *strings.Builder, label, value string) {
	builder.WriteString(label)
	builder.WriteString(": <code>")
	builder.WriteString(escapeHTML(value))
	builder.WriteString("</code>\n")
}

func appendIntCodeLine(builder *strings.Builder, label string, value int) {
	builder.WriteString(label)
	builder.WriteString(": <code>")
	builder.WriteString(strconv.Itoa(value))
	builder.WriteString("</code>\n")
}

func appendUint32CodeLine(builder *strings.Builder, label string, value uint32) {
	builder.WriteString(label)
	builder.WriteString(": <code>")
	builder.WriteString(strconv.FormatUint(uint64(value), 10))
	builder.WriteString("</code>\n")
}

func appendTripleIntCodeLine(builder *strings.Builder, label string, first, second, third int) {
	builder.WriteString(label)
	builder.WriteString(": <code>")
	builder.WriteString(strconv.Itoa(first))
	builder.WriteString(" / ")
	builder.WriteString(strconv.Itoa(second))
	builder.WriteString(" / ")
	builder.WriteString(strconv.Itoa(third))
	builder.WriteString("</code>\n")
}

func appendTripleUint64CodeLine(builder *strings.Builder, label string, first, second, third uint64) {
	builder.WriteString(label)
	builder.WriteString(": <code>")
	builder.WriteString(strconv.FormatUint(first, 10))
	builder.WriteString(" / ")
	builder.WriteString(strconv.FormatUint(second, 10))
	builder.WriteString(" / ")
	builder.WriteString(strconv.FormatUint(third, 10))
	builder.WriteString("</code>\n")
}

func trimTrailingNewline(value string) string {
	return strings.TrimSuffix(value, "\n")
}
