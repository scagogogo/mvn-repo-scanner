package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- OpenStore 错误分支 ---

func TestOpenStore_BadDBPath(t *testing.T) {
	// 用一个目录路径作为 dbPath（无法创建文件）触发 sql.Open 后续 WAL/migrate 失败
	_, err := OpenStore("/proc/cannot/open/here.db")
	assert.Error(t, err)
}

// 用只读模式打开内存库：PRAGMA WAL 成功（内存库无需 WAL 文件）但
// CREATE TABLE 失败（只读），覆盖 OpenStore 的 migrate err 分支。
func TestOpenStore_MigrateFails(t *testing.T) {
	_, err := OpenStore("file::memory:?mode=ro")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "migrate")
}

// DB 访问器返回底层 *sql.DB，可用于 raw diagnostics。
func TestStore_DB(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()
	db := store.DB()
	require.NotNil(t, db)
	assert.NoError(t, db.Ping())
}

// 关闭 DB 后调用各查询方法，覆盖 Query/QueryRow/Exec 的 err 分支。
func TestStore_ClosedDB_QueryErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, store.Close())

	_, err = store.GetFindings(1)
	assert.Error(t, err)

	_, err = store.GetStats()
	assert.Error(t, err)

	_, err = store.RecentRecords(1)
	assert.Error(t, err)

	_, err = store.FindingsByRule()
	assert.Error(t, err)

	_, err = store.FindingsBySeverity()
	assert.Error(t, err)

	_, err = store.DeleteOldRecords(1)
	assert.Error(t, err)

	_, err = store.ExportFindingsJSON()
	assert.Error(t, err)

	_, err = store.GetTask("x")
	assert.Error(t, err)

	_, err = store.ListTasks("")
	assert.Error(t, err)

	_, err = store.DueTasks(time.Now())
	assert.Error(t, err)

	assert.Error(t, store.UpdateTaskRun("x", "ok", time.Now()))
	assert.Error(t, store.DeleteTask("x"))
	assert.Error(t, store.SetTaskStatus("x", TaskActive))
}

// --- 循环内 Scan err 分支（破坏列类型让 Scan 失败） ---

func TestStore_RecentRecords_ScanError(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	// findings_count 列存文本 → Scan int 失败
	_, err = store.db.Exec(`INSERT INTO scan_records (group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms) VALUES ('a','b','1','r','complete', 'NOT_AN_INT', '2020-01-01', 0)`)
	require.NoError(t, err)

	_, err = store.RecentRecords(1)
	assert.Error(t, err)
}

func TestStore_GetRecord_ScanError(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	_, err = store.db.Exec(`INSERT INTO scan_records (group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms) VALUES ('a','b','1','r','complete', 1, 'NOT_A_DATE', 0)`)
	require.NoError(t, err)

	_, err = store.GetRecord("a", "b", "1", "r")
	assert.Error(t, err)
}

// GetStats 的主 Scan 读取 4 个 int（COUNT/SUM/COALESCE），永远为 int 不会失败；
// 但 CriticalCount/HighCount 子查询同样读 int。整个 GetStats 的 Scan err 不可达。
// 这里改为验证 GetStats 在空库上正常工作（覆盖两个子查询 Scan）。
func TestStore_GetStats_EmptyDB(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	stats, err := store.GetStats()
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalRecords)
	assert.Equal(t, 0, stats.CriticalCount)
	assert.Equal(t, 0, stats.HighCount)
}

// 在 findings 表插入 line_number 为文本的行，触发 GetFindings/ExportFindingsJSON 循环内 Scan err。
func TestStore_FindingsScanErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.UpsertRecord(&GAVRecord{GroupID: "a", ArtifactID: "b", Version: "1", RepoURL: "r", Status: DBStatusComplete, ScanTime: time.Now()}))
	rec, err := store.GetRecord("a", "b", "1", "r")
	require.NoError(t, err)
	require.NotNil(t, rec)

	// line_number 列存文本 → Scan int 失败
	_, err = store.db.Exec(`INSERT INTO findings (record_id, rule_id, rule_name, severity, file_path, line_number) VALUES (?, 'r', 'n', 's', 'f', 'NOT_AN_INT')`, rec.ID)
	require.NoError(t, err)

	_, err = store.GetFindings(rec.ID)
	assert.Error(t, err)
	_, err = store.ExportFindingsJSON()
	assert.Error(t, err)
}

// --- tasks 循环内 Scan err ---

func TestStore_TasksScanErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	// scan_count 列存文本 → Scan int 失败（覆盖 GetTask/ListTasks/DueTasks 的 Scan err）
	_, err = store.db.Exec(`INSERT INTO scan_tasks (task_id, repo_url, config, scan_interval_sec, status, created_at, scan_count) VALUES ('bt', 'r', '{}', 0, 'active', '2020-01-01', 'NOT_AN_INT')`)
	require.NoError(t, err)

	_, err = store.GetTask("bt")
	assert.Error(t, err)
	_, err = store.ListTasks("")
	assert.Error(t, err)
	_, err = store.DueTasks(time.Now())
	assert.Error(t, err)
}

// --- workspace 错误分支 ---

func TestNewWorkspace_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := NewWorkspace()
	assert.Error(t, err)
}

func TestWorkspace_CleanCache_ReadDirError(t *testing.T) {
	// CacheDir 是文件 → ReadDir 失败
	tmpDir := t.TempDir()
	cacheAsFile := filepath.Join(tmpDir, "cache")
	require.NoError(t, os.WriteFile(cacheAsFile, []byte("x"), 0644))
	w := &Workspace{BaseDir: tmpDir, CacheDir: cacheAsFile}
	assert.Error(t, w.CleanCache())
}

func TestWorkspace_DBSizeMB_StatError(t *testing.T) {
	// DBPath 在无权限目录下 → Stat 返回非 NotExist err
	w := &Workspace{DBPath: "/proc/1/root/nonexistent"}
	_, err := w.DBSizeMB()
	assert.Error(t, err)
}

// 多文件场景：覆盖 sort.Slice 比较函数 + 删除循环内 break。
func TestWorkspace_EnforceCacheLimit_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)
	w.CacheMaxMB = 5 // 限制 5MB

	// 3 个文件每个 3MB，共 9MB > 5MB。按 mtime 排序后删最老的直到 ≤ 5MB。
	// 删第 1 个 usage 9→6，第 2 个 6→3 ≤5 → break（第 3 个保留）。
	// 这样覆盖 sort 比较函数 + 循环内 break + 保留未删文件。
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(w.CacheDir, "f"+string(rune('a'+i))+".bin"), make([]byte, 3*1024*1024), 0644))
	}
	require.NoError(t, w.EnforceCacheLimit())

	entries, _ := os.ReadDir(w.CacheDir)
	assert.Equal(t, 1, len(entries), "one file should remain after enforcing limit")
}
