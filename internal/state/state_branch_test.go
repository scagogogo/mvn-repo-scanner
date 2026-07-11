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

// state.go ValidateConfig 的 MaxFileSize 复合条件
// `MaxFileSize != "" && MaxFileSize != cfg.MaxFileSize`
// 覆盖：空快照短路通过、相同值通过、不同值报错。
func TestValidateConfig_MaxFileSize_BranchCombos(t *testing.T) {
	tmpDir := t.TempDir()

	// 空快照（MaxFileSize==""）→ 短路通过，即使 cfg 不同
	s1 := NewScanStateWithConfig("mf1", "https://a.com", "", filepath.Join(tmpDir, "mf1.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com"}) // MaxFileSize 空
	assert.NoError(t, s1.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "100MB"}))

	// 相同值 → 通过
	s2 := NewScanStateWithConfig("mf2", "https://a.com", "", filepath.Join(tmpDir, "mf2.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	assert.NoError(t, s2.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"}))

	// 不同值 → 报错，且错误消息含 MaxFileSize
	s3 := NewScanStateWithConfig("mf3", "https://a.com", "", filepath.Join(tmpDir, "mf3.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	err := s3.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "100MB"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxFileSize")
}
