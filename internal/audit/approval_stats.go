package audit

import (
	"fmt"
	"strings"
	"time"
)

const approvalStatsDefaultDays = 90

type AgentApprovalStats struct {
	Provider string
	Approved int
	Rejected int
}

// NormalizeDecisionAgent canonicalizes an agent/provider id for comparison:
// lowercased and trimmed. It no longer strips a "capture:" prefix — capture
// and organize-* pending rows are excluded before they ever reach trust
// evaluation (see ApprovalStats below), so any provider id this returns is
// already a direct-write agent id.
func NormalizeDecisionAgent(agent string) string {
	return strings.ToLower(strings.TrimSpace(agent))
}

func (l *Logger) ApprovalStats(days int) ([]AgentApprovalStats, error) {
	if l == nil || l.db == nil {
		return []AgentApprovalStats{}, nil
	}
	if days <= 0 {
		days = approvalStatsDefaultDays
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	// capture:* and organize-* pendings queue unconditionally regardless of
	// trust level (they're never gated by the provider's trust setting), so a
	// human approving them says nothing about that provider's direct-write
	// judgment. Counting them would let rubber-stamping trivial capture facts
	// build an approval record that promotes the agent's DIRECT writes to
	// auto — excluded here so trust evidence only reflects direct writes.
	rows, err := l.db.Query(`
		SELECT provider,
		       SUM(CASE WHEN action = 'pending_approve' THEN 1 ELSE 0 END) AS approved,
		       SUM(CASE WHEN action = 'pending_reject' THEN 1 ELSE 0 END) AS rejected
		FROM audit_entries
		WHERE action IN ('pending_approve', 'pending_reject') AND timestamp >= ?
		  AND provider NOT LIKE '%:%' AND provider NOT LIKE 'organize-%'
		GROUP BY provider
		ORDER BY COUNT(*) DESC, provider ASC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query approval stats: %w", err)
	}
	defer rows.Close()

	stats := []AgentApprovalStats{}
	for rows.Next() {
		var stat AgentApprovalStats
		if err := rows.Scan(&stat.Provider, &stat.Approved, &stat.Rejected); err != nil {
			return nil, fmt.Errorf("failed to scan approval stats: %w", err)
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate approval stats: %w", err)
	}
	return stats, nil
}
