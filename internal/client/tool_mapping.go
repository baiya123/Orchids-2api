// Package client provides tool name mapping between Claude Code and Orchids.
package client

import (
	"strings"
	"sync"
	"sync/atomic"
)

// ToolMapper handles bidirectional tool name mapping and statistics.
type ToolMapper struct {
	// Claude Code → Orchids 标准名
	toOrchids map[string]string
	// Orchids → Claude Code 标准名
	fromOrchids map[string]string
	// 工具调用统计
	stats map[string]*uint64
	mu    sync.RWMutex
}

// DefaultToolMapper is the global tool mapper instance.
var DefaultToolMapper = NewToolMapper()

// NewToolMapper creates a new ToolMapper with default mappings.
func NewToolMapper() *ToolMapper {
	tm := &ToolMapper{
		toOrchids:   make(map[string]string),
		fromOrchids: make(map[string]string),
		stats:       make(map[string]*uint64),
	}

	// Claude Code → Orchids 映射
	tm.addMapping("Str_Replace_Editor", "Edit")
	tm.addMapping("str_replace_editor", "Edit")
	tm.addMapping("View", "Read")
	tm.addMapping("view", "Read")
	tm.addMapping("ReadFile", "Read")
	tm.addMapping("read_file", "Read")
	tm.addMapping("ListDir", "LS")
	tm.addMapping("list_dir", "LS")
	tm.addMapping("list_directory", "LS")
	tm.addMapping("RipGrepTool", "Grep")
	tm.addMapping("ripgrep", "Grep")
	tm.addMapping("search_code", "Grep")
	tm.addMapping("SearchCode", "Grep")
	tm.addMapping("GlobTool", "Glob")
	tm.addMapping("glob", "Glob")
	tm.addMapping("find_files", "Glob")
	tm.addMapping("Execute", "Bash")
	tm.addMapping("execute", "Bash")
	tm.addMapping("run_command", "Bash")
	tm.addMapping("RunCommand", "Bash")
	tm.addMapping("WriteFile", "Write")
	tm.addMapping("write_file", "Write")
	tm.addMapping("CreateFile", "Write")
	tm.addMapping("create_file", "Write")

	// Orchids 事件 → Claude Code 工具名映射
	tm.fromOrchids["edit_file"] = "Edit"
	tm.fromOrchids["todo_write"] = "TodoWrite"
	tm.fromOrchids["Edit"] = "Edit"
	tm.fromOrchids["Read"] = "Read"
	tm.fromOrchids["Write"] = "Write"
	tm.fromOrchids["Bash"] = "Bash"
	tm.fromOrchids["LS"] = "LS"
	tm.fromOrchids["Glob"] = "Glob"
	tm.fromOrchids["Grep"] = "Grep"
	tm.fromOrchids["TodoWrite"] = "TodoWrite"

	return tm
}

func (tm *ToolMapper) addMapping(from, to string) {
	tm.toOrchids[from] = to
	tm.toOrchids[strings.ToLower(from)] = to
}

// ToOrchids maps a Claude Code tool name to Orchids standard name.
func (tm *ToolMapper) ToOrchids(name string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if mapped, ok := tm.toOrchids[name]; ok {
		return mapped
	}
	if mapped, ok := tm.toOrchids[strings.ToLower(name)]; ok {
		return mapped
	}
	return name // 未知工具保持原名
}

// FromOrchids maps an Orchids tool name to Claude Code standard name.
func (tm *ToolMapper) FromOrchids(name string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if mapped, ok := tm.fromOrchids[name]; ok {
		return mapped
	}
	return name
}

// RecordCall records a tool call for statistics.
func (tm *ToolMapper) RecordCall(name string) {
	tm.mu.Lock()
	if tm.stats[name] == nil {
		var count uint64
		tm.stats[name] = &count
	}
	tm.mu.Unlock()

	atomic.AddUint64(tm.stats[name], 1)
}

// GetStats returns a copy of tool call statistics.
func (tm *ToolMapper) GetStats() map[string]uint64 {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make(map[string]uint64, len(tm.stats))
	for name, count := range tm.stats {
		result[name] = atomic.LoadUint64(count)
	}
	return result
}

// ResetStats clears all statistics.
func (tm *ToolMapper) ResetStats() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for name := range tm.stats {
		atomic.StoreUint64(tm.stats[name], 0)
	}
}

// IsBlocked checks if a tool should be blocked.
// 可以在这里添加工具过滤逻辑
var blockedTools = map[string]bool{
	"web_search": true,
	"WebSearch":  true,
	"SQL":        true,
	// 添加其他需要屏蔽的云端工具
}

// IsBlocked returns true if the tool should be blocked.
func (tm *ToolMapper) IsBlocked(name string) bool {
	lower := strings.ToLower(name)
	return blockedTools[name] || blockedTools[lower]
}

// NormalizeToolName standardizes tool name for consistent handling.
func NormalizeToolName(name string) string {
	return DefaultToolMapper.ToOrchids(name)
}

// MapToolCall maps a tool call from Claude Code format to Orchids format.
// Returns the mapped name and records the call.
func MapToolCall(name string) string {
	mapped := DefaultToolMapper.ToOrchids(name)
	DefaultToolMapper.RecordCall(mapped)
	return mapped
}

// MapToolResponse maps a tool response from Orchids format to Claude Code format.
func MapToolResponse(name string) string {
	return DefaultToolMapper.FromOrchids(name)
}
