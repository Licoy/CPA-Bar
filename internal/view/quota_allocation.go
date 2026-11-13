package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/quota"
)

// Compute against every account before sorting or filtering either list.
func claudeAllocations(c *ui.Context, s core.InstanceSnapshot, now time.Time) map[string][]quota.ClaudeTargetAllocation {
	rows := quota.ClaudeTargetAllocations(s.Credentials, s.LastSuccess, now)
	allocations := make(map[string][]quota.ClaudeTargetAllocation)
	for i, row := range rows {
		if row != nil {
			allocations[credKeyOf(s.Credentials[i])] = row
		}
	}
	if len(allocations) > 0 {
		c.After(5 * time.Second)
	}
	return allocations
}

func allocationShare(share *float64) string {
	if share == nil {
		return T("未知", "Unknown")
	}
	return fmt.Sprintf("%.2f%%", *share)
}

func allocationView(c *ui.Context, allocation []quota.ClaudeTargetAllocation) {
	if len(allocation) == 0 {
		return
	}
	primary := allocation[0]
	var lines []string
	lines = append(lines, T("实时目标分配占比", "Live target allocation share"))
	for _, item := range allocation {
		if item.Scope == "" {
			primary = item
		}
	}
	for _, item := range allocation {
		scope := item.Scope
		if scope == "" {
			scope = T("通用", "General")
		}
		line := scope + " · " + allocationShare(item.Share)
		if item.Err != nil {
			line += " · " + item.Err.Error()
		}
		lines = append(lines, line)
	}
	ui.Text(c, T("分配 ", "Allocation ")+allocationShare(primary.Share)).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine().Shrink(0).Tooltip(strings.Join(lines, "\n"))
}
