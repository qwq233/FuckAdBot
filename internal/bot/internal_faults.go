package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	internalFaultWindow              = 10 * time.Minute
	internalFaultThreshold           = 20
	internalFaultRecentLimit         = 5
	internalFaultAlertRequestTimeout = 5 * time.Second
)

type internalFaultEntry struct {
	at      time.Time
	summary string
}

type internalFaultSnapshot struct {
	TriggeredAt time.Time
	Window      time.Duration
	Threshold   int
	Count       int
	Recent      []string
}

type internalFaultMonitor struct {
	mu      sync.Mutex
	entries []internalFaultEntry
	tripped bool
}

func (m *internalFaultMonitor) record(component string, err error) (internalFaultSnapshot, bool) {
	return m.recordAt(time.Now().UTC(), component, err)
}

func (m *internalFaultMonitor) recordAt(now time.Time, component string, err error) (internalFaultSnapshot, bool) {
	if err == nil {
		return internalFaultSnapshot{}, false
	}

	cutoff := now.Add(-internalFaultWindow)
	summary := formatInternalFaultSummary(component, err)

	m.mu.Lock()
	defer m.mu.Unlock()

	kept := m.entries[:0]
	for _, entry := range m.entries {
		if !entry.at.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	m.entries = append(kept, internalFaultEntry{at: now, summary: summary})

	if m.tripped || len(m.entries) < internalFaultThreshold {
		return internalFaultSnapshot{}, false
	}
	m.tripped = true

	recentCount := internalFaultRecentLimit
	if len(m.entries) < recentCount {
		recentCount = len(m.entries)
	}
	recent := make([]string, 0, recentCount)
	for index := len(m.entries) - 1; index >= 0 && len(recent) < recentCount; index-- {
		recent = append(recent, m.entries[index].summary)
	}

	return internalFaultSnapshot{
		TriggeredAt: now,
		Window:      internalFaultWindow,
		Threshold:   internalFaultThreshold,
		Count:       len(m.entries),
		Recent:      recent,
	}, true
}

func formatInternalFaultSummary(component string, err error) string {
	if err == nil {
		return strings.TrimSpace(component)
	}

	cleanComponent := strings.TrimSpace(component)
	cleanMessage := strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")

	if cleanComponent == "" {
		return cleanMessage
	}
	if cleanMessage == "" {
		return cleanComponent
	}

	return cleanComponent + ": " + cleanMessage
}

func (b *Bot) Errors() <-chan error {
	if b == nil {
		return nil
	}

	b.ensureRuntimeState()
	return b.fatalErrCh
}

func (b *Bot) RecordInternalFault(component string, err error) {
	b.recordInternalFault(component, err)
}

func (b *Bot) recordInternalFault(component string, err error) {
	if b == nil || err == nil {
		return
	}

	b.ensureRuntimeState()

	summary := formatInternalFaultSummary(component, err)
	log.Printf("[bot] internal fault: %s", summary)
	b.runtimeStats.recordError("internal fault: " + summary)

	snapshot, triggered := b.internalFaults.record(component, err)
	if !triggered {
		return
	}

	log.Printf("[bot] internal fault fuse triggered: count=%d window=%s", snapshot.Count, snapshot.Window)
	b.notifyInternalFaultTrip(snapshot)

	fatalErr := fmt.Errorf("internal fault fuse triggered after %d faults in %s", snapshot.Count, snapshot.Window)
	select {
	case b.fatalErrCh <- fatalErr:
	default:
	}
}

func (b *Bot) notifyInternalFaultTrip(snapshot internalFaultSnapshot) {
	if b == nil || b.Bot == nil || b.Config == nil || len(b.Config.Bot.Admins) == 0 {
		return
	}

	alertText := buildInternalFaultAlertText(snapshot)
	for _, adminID := range b.Config.Bot.Admins {
		ctx, cancel := context.WithTimeout(context.Background(), internalFaultAlertRequestTimeout)
		_, err := b.Bot.SendMessageWithContext(ctx, adminID, alertText, nil)
		cancel()
		if err != nil {
			log.Printf("[bot] internal fault alert send error: admin=%d err=%v", adminID, err)
		}
	}
}

func buildInternalFaultAlertText(snapshot internalFaultSnapshot) string {
	var builder strings.Builder
	builder.Grow(256)
	builder.WriteString("\u3010\u7a0b\u5e8f\u5185\u90e8\u9519\u8bef\u7194\u65ad\u3011\n")
	fmt.Fprintf(&builder, "\u89e6\u53d1\u6761\u4ef6\uff1a%s\u5185\u8fbe\u5230%d\u6b21\u7a0b\u5e8f\u5185\u90e8\u9519\u8bef\n", formatInternalFaultWindowLabel(snapshot.Window), snapshot.Threshold)
	fmt.Fprintf(&builder, "\u89e6\u53d1\u65f6\u95f4\uff1a%s\n", snapshot.TriggeredAt.Format(time.RFC3339))
	fmt.Fprintf(&builder, "\u7a97\u53e3\u5185\u7edf\u8ba1\u9519\u8bef\u6570\uff1a%d\n", snapshot.Count)
	builder.WriteString("\u6700\u8fd1\u8ba1\u6570\u9519\u8bef\u6458\u8981\uff1a\n")
	if len(snapshot.Recent) == 0 {
		builder.WriteString("1. \u65e0\n")
	} else {
		for index, summary := range snapshot.Recent {
			fmt.Fprintf(&builder, "%d. %s\n", index+1, summary)
		}
	}
	builder.WriteString("\u7a0b\u5e8f\u5c06\u7acb\u5373\u9000\u51fa\u3002")
	return builder.String()
}

func formatInternalFaultWindowLabel(window time.Duration) string {
	if window > 0 && window%time.Minute == 0 {
		return fmt.Sprintf("%d\u5206\u949f", int(window/time.Minute))
	}
	return window.String()
}
