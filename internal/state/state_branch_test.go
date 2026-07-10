package state

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// state_branch_test.go 覆盖 state.go:547/550/555 三个复合条件
// `ConfigSnapshot.X != "" && ConfigSnapshot.X != cfg.X` 的全组合。
// 每字段覆盖三种情况：空快照（X=="" → 短路通过）、相同值（X==cfg.X → 通过）、不同值（X!=cfg.X → err）。
func TestValidateConfig_BranchCombos(t *testing.T) {
	tmpDir := t.TempDir()

	// RepoURL 组合
	t.Run("RepoURL_empty_snapshot_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s1", "https://a.com", "", filepath.Join(tmpDir, "s1.json"), 0,
			ConfigSnapshot{}) // 空快照 → RepoURL=="" 短路通过
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://b.com"}))
	})
	t.Run("RepoURL_same_value_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s2", "https://a.com", "", filepath.Join(tmpDir, "s2.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"})
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com"}))
	})
	t.Run("RepoURL_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s3", "https://a.com", "", filepath.Join(tmpDir, "s3.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://b.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "RepoURL")
	})

	// GroupFilter 组合
	t.Run("GroupFilter_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s4", "https://a.com", "com.a", filepath.Join(tmpDir, "s4.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com", GroupFilter: "com.a"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", GroupFilter: "com.b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GroupFilter")
	})

	// RulesLevel 组合
	t.Run("RulesLevel_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s5", "https://a.com", "", filepath.Join(tmpDir, "s5.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "core"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "extended"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "RulesLevel")
	})
	t.Run("RulesLevel_empty_snapshot_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s6", "https://a.com", "", filepath.Join(tmpDir, "s6.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"}) // RulesLevel=="" 短路通过
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "extended"}))
	})
}
