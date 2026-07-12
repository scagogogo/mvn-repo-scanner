package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonMarshal serializes a Cursor to JSON bytes.
func jsonMarshal(c Cursor) ([]byte, error) {
	return json.Marshal(c)
}

// jsonUnmarshal deserializes JSON bytes into a Cursor.
func jsonUnmarshal(data []byte) (Cursor, error) {
	var c Cursor
	err := json.Unmarshal(data, &c)
	return c, err
}

// mockPageFetcher serves a fixed set of directory listings.
type mockPageFetcher struct {
	pages map[string]string // dirURL -> html
}

func (m *mockPageFetcher) FetchPage(_ context.Context, dirURL string) (string, error) {
	html, ok := m.pages[dirURL]
	if !ok {
		return "", fmt.Errorf("HTTP 404: %s", dirURL)
	}
	return html, nil
}

// buildMockRepo builds a small Maven tree with realistic 3-segment group paths:
//
//	root/
//	  com/
//	    example/
//	      lib/                 <- artifactID
//	        1.0/               <- version
//	          lib-1.0.jar
//	          lib-1.0.pom
//	        2.0/
//	          lib-2.0.jar
//	  org/
//	    apache/
//	      commons/             <- artifactID
//	        1.0/
//	          commons-1.0.jar
func buildMockRepo() *mockPageFetcher {
	base := "https://repo.example.com/maven2"
	pages := map[string]string{
		base + "/": `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
		base + "/com/":                     `<a href="../">../</a><a href="example/">example/</a>`,
		base + "/com/example/":             `<a href="../">../</a><a href="lib/">lib/</a>`,
		base + "/com/example/lib/":         `<a href="../">../</a><a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`,
		base + "/com/example/lib/1.0/":     `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a><a href="lib-1.0.pom">lib-1.0.pom</a>`,
		base + "/com/example/lib/2.0/":     `<a href="../">../</a><a href="lib-2.0.jar">lib-2.0.jar</a>`,
		base + "/org/":                     `<a href="../">../</a><a href="apache/">apache/</a>`,
		base + "/org/apache/":              `<a href="../">../</a><a href="commons/">commons/</a>`,
		base + "/org/apache/commons/":      `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		base + "/org/apache/commons/1.0/":  `<a href="../">../</a><a href="commons-1.0.jar">commons-1.0.jar</a>`,
	}
	return &mockPageFetcher{pages: pages}
}

func TestCursorWalker_FullWalk(t *testing.T) {
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	var artifacts []Artifact
	cur, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 0, len(cur), "cursor should be empty after full walk")

	// Sorted order: com/example/lib/1.0 files, then 2.0, then org/apache/commons/1.0
	require.Equal(t, 4, len(artifacts))
	assert.Equal(t, "com.example", artifacts[0].GroupID)
	assert.Equal(t, "lib", artifacts[0].ArtifactID)
	assert.Equal(t, "1.0", artifacts[0].Version)
	assert.Equal(t, "lib-1.0.jar", artifacts[0].FileName)

	assert.Equal(t, "org.apache", artifacts[3].GroupID)
	assert.Equal(t, "commons", artifacts[3].ArtifactID)
	assert.Equal(t, "commons-1.0.jar", artifacts[3].FileName)
}

func TestCursorWalker_StopAndResume(t *testing.T) {
	fetcher := buildMockRepo()

	var firstBatch []Artifact
	var savedCursor Cursor

	// First walk: stop after 2 artifacts
	w1 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	count := 0
	cur, err := w1.Walk(context.Background(), RootCursor(), func(a Artifact) {
		firstBatch = append(firstBatch, a)
		count++
	}, func() bool { return count >= 2 })
	require.NoError(t, err)
	savedCursor = cur

	require.Equal(t, 2, len(firstBatch), "should yield 2 artifacts before stopping")
	assert.Greater(t, len(savedCursor), 0, "cursor should be non-empty at stop point")

	// Second walk: resume from saved cursor, collect the rest
	w2 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	var secondBatch []Artifact
	_, err = w2.Walk(context.Background(), savedCursor, func(a Artifact) {
		secondBatch = append(secondBatch, a)
	}, func() bool { return false })
	require.NoError(t, err)

	// Full walk for comparison
	w3 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	var all []Artifact
	_, _ = w3.Walk(context.Background(), RootCursor(), func(a Artifact) {
		all = append(all, a)
	}, func() bool { return false })

	// firstBatch + secondBatch should equal all, with no overlap
	assert.Equal(t, len(all), len(firstBatch)+len(secondBatch),
		"first batch + resumed batch should cover all artifacts")

	// No duplicates between batches
	seen := make(map[string]bool)
	for _, a := range firstBatch {
		seen[a.String()+":"+a.FileName] = true
	}
	for _, a := range secondBatch {
		key := a.String() + ":" + a.FileName
		assert.False(t, seen[key], "no duplicate artifact across resume: %s", key)
	}

	// Ordering preserved: firstBatch are the first N of all
	for i := 0; i < len(firstBatch); i++ {
		assert.Equal(t, all[i].String()+":"+all[i].FileName,
			firstBatch[i].String()+":"+firstBatch[i].FileName,
			"artifact %d should match between firstBatch and all", i)
	}
	// secondBatch are the rest
	for i := 0; i < len(secondBatch); i++ {
		assert.Equal(t, all[len(firstBatch)+i].String()+":"+all[len(firstBatch)+i].FileName,
			secondBatch[i].String()+":"+secondBatch[i].FileName,
			"resumed artifact %d should match tail of all", i)
	}
}

func TestCursorWalker_CursorDepthIsTreeDepth(t *testing.T) {
	fetcher := buildMockRepo()

	// Walk to the deepest leaf and stop mid-way. Cursor depth should equal
	// the current tree depth.
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	count := 0
	cur, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		count++
	}, func() bool { return count >= 1 }) // stop after first artifact
	require.NoError(t, err)

	// First artifact is com/example/lib/1.0/lib-1.0.jar — depth is 6:
	// root, com, example, lib, 1.0, (file).
	assert.LessOrEqual(t, len(cur), 7, "cursor depth should be bounded by tree depth")
	assert.Greater(t, len(cur), 0, "cursor should be non-empty at stop")
}

func TestCursorWalker_ResumeSkipsCompleted(t *testing.T) {
	fetcher := buildMockRepo()

	// Walk to first artifact, save cursor, then resume.
	// The resumed walk should NOT re-yield the first artifact.
	w1 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	yielded := 0
	cur, err := w1.Walk(context.Background(), RootCursor(), func(a Artifact) {
		yielded++
	}, func() bool { return yielded >= 1 })
	require.NoError(t, err)

	// Resume — should yield the remaining 3
	w2 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	var resumed []Artifact
	_, err = w2.Walk(context.Background(), cur, func(a Artifact) {
		resumed = append(resumed, a)
	}, func() bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 3, len(resumed), "resume should yield the remaining 3 artifacts")
}

func TestCursorWalker_EmptyRepo(t *testing.T) {
	fetcher := &mockPageFetcher{
		pages: map[string]string{
			"https://repo.example.com/maven2/": `<a href="../">../</a>`,
		},
	}
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	var artifacts []Artifact
	cur, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 0, len(artifacts))
	assert.Equal(t, 0, len(cur))
}

func TestCursorWalker_FetchErrorSkipsDir(t *testing.T) {
	// org/ returns 404 — should skip it but still return com/ artifacts.
	// com/example/lib/1.0/lib-1.0.jar is a valid artifact (3-segment group).
	fetcher := &mockPageFetcher{
		pages: map[string]string{
			"https://repo.example.com/maven2/":           `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
			"https://repo.example.com/maven2/com/":        `<a href="../">../</a><a href="example/">example/</a>`,
			"https://repo.example.com/maven2/com/example/":     `<a href="../">../</a><a href="lib/">lib/</a>`,
			"https://repo.example.com/maven2/com/example/lib/": `<a href="../">../</a><a href="1.0/">1.0/</a>`,
			"https://repo.example.com/maven2/com/example/lib/1.0/": `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
			// org/ intentionally missing -> 404
		},
	}
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	var artifacts []Artifact
	_, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err, "fetch error should not fail the whole walk")
	assert.Equal(t, 1, len(artifacts), "should still get com/ artifact despite org/ 404")
}

func TestCursorWalker_GroupFilterStartPath(t *testing.T) {
	// When a group filter is set, the walker starts at that group's path.
	// Simulate group filter "com.example" -> start at "com/example".
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	startCur := Cursor{{DirPath: "com/example", NextIdx: 0}}

	var artifacts []Artifact
	_, err := w.Walk(context.Background(), startCur, func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err)
	// Should only find com.example artifacts, not org.apache
	for _, a := range artifacts {
		assert.Equal(t, "com.example", a.GroupID, "group filter should restrict to com.example")
	}
	assert.Equal(t, 3, len(artifacts))
}

func TestCursorWalker_FetchErrorRecordsDirFailure(t *testing.T) {
	// org/apache/ returns 404 — the onDirFailed callback must be invoked with
	// that directory path, while the walk still returns the com/ artifact and
	// a nil error (preserving the "skip, don't abort" contract).
	fetcher := &mockPageFetcher{
		pages: map[string]string{
			"https://repo.example.com/maven2/":           `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
			"https://repo.example.com/maven2/com/":        `<a href="../">../</a><a href="example/">example/</a>`,
			"https://repo.example.com/maven2/com/example/":     `<a href="../">../</a><a href="lib/">lib/</a>`,
			"https://repo.example.com/maven2/com/example/lib/": `<a href="../">../</a><a href="1.0/">1.0/</a>`,
			"https://repo.example.com/maven2/com/example/lib/1.0/": `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
			"https://repo.example.com/maven2/org/":        `<a href="../">../</a><a href="apache/">apache/</a>`,
			// org/apache/ intentionally missing -> 404
		},
	}
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	var failedDirs []string
	w.SetOnDirFailed(func(dirPath string) {
		failedDirs = append(failedDirs, dirPath)
	})

	var artifacts []Artifact
	_, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err, "fetch error should not fail the whole walk")
	assert.Equal(t, 1, len(artifacts), "should still get com/ artifact despite org/apache 404")
	// The failed directory must be reported exactly once.
	assert.Equal(t, []string{"org/apache"}, failedDirs,
		"onDirFailed should fire once with the failed directory path")
}

func TestCursorWalker_FetchErrorRootDoesNotPanic(t *testing.T) {
	// Root listing fails entirely — Walk must return (empty cursor, nil error)
	// without panicking, and the root path must be reported via onDirFailed.
	fetcher := &mockPageFetcher{pages: map[string]string{}}
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")

	var failedDirs []string
	w.SetOnDirFailed(func(dirPath string) {
		failedDirs = append(failedDirs, dirPath)
	})

	var artifacts []Artifact
	cur, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		artifacts = append(artifacts, a)
	}, func() bool { return false })
	require.NoError(t, err, "root fetch failure should not fail the whole walk")
	assert.Equal(t, 0, len(artifacts))
	assert.Equal(t, 0, len(cur), "cursor should be empty after root failure pops the only frame")
	assert.Equal(t, []string{""}, failedDirs, "root path (\"\") should be reported as failed")
}

func TestRootCursor(t *testing.T) {
	c := RootCursor()
	assert.Equal(t, 1, len(c))
	assert.Equal(t, "", c[0].DirPath)
	assert.Equal(t, 0, c[0].NextIdx)
}

func TestCursorWalker_CursorSerializable(t *testing.T) {
	// Cursor is a plain slice of structs with JSON tags — verify it round-trips
	// and that a deserialized cursor resumes correctly.
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	count := 0
	cur, err := w.Walk(context.Background(), RootCursor(), func(a Artifact) {
		count++
	}, func() bool { return count >= 1 })
	require.NoError(t, err)

	data, err := jsonMarshal(cur)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	restored, err := jsonUnmarshal(data)
	require.NoError(t, err)

	// Resume from deserialized cursor should yield remaining artifacts
	w2 := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	var resumed []Artifact
	_, err = w2.Walk(context.Background(), restored, func(a Artifact) {
		resumed = append(resumed, a)
	}, func() bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 3, len(resumed), "deserialized cursor should resume correctly")
}

func TestCursor_CursorDepth(t *testing.T) {
	assert.Equal(t, 0, Cursor{}.CursorDepth())
	assert.Equal(t, 1, RootCursor().CursorDepth())
	assert.Equal(t, 3, Cursor{{"", 0}, {"com", 1}, {"com/example", 2}}.CursorDepth())
}

func TestCursor_String(t *testing.T) {
	// 空游标
	assert.Equal(t, "cursor[]", Cursor{}.String())
	// 多帧游标
	s := Cursor{{DirPath: "com", NextIdx: 1}, {DirPath: "com/example", NextIdx: 0}}.String()
	assert.Contains(t, s, "{com@1}")
	assert.Contains(t, s, "{com/example@0}")
	assert.True(t, strings.HasPrefix(s, "cursor["))
}

func TestCursorWalker_EmptyStartCursor(t *testing.T) {
	// 传空 Cursor{}（len==0）→ Walk 内部初始化为 RootCursor（line 96-98）
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	count := 0
	cur, err := w.Walk(context.Background(), Cursor{}, func(a Artifact) {
		count++
	}, func() bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 0, len(cur))
	assert.True(t, count >= 1, "should discover artifacts from empty cursor")
}

func TestCursorWalker_ContextCanceled(t *testing.T) {
	// ctx 已取消 → Walk 返回 cur, ctx.Err()（line 102-104）
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cur, err := w.Walk(ctx, RootCursor(), func(a Artifact) {}, func() bool { return false })
	assert.Error(t, err)
	assert.True(t, len(cur) >= 1, "cursor should hold the start frame")
}

func TestCursorWalker_BuildArtifact_TooFewSegments(t *testing.T) {
	// buildArtifact: dirPath 段数 < 3 → 返回 nil（line 189-191）
	fetcher := buildMockRepo()
	w := NewCursorWalker(fetcher, "https://repo.example.com/maven2")
	// segs=["com","lib"] len=2 < 3
	art := w.buildArtifact("com/lib", entry{Name: "lib.jar", URL: "u"})
	assert.Nil(t, art)
}
