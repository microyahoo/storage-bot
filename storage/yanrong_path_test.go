package storage

import "testing"

func TestNormalizeQuotaPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/"},
		{"/", "/"},
		{"//", "/"},
		{".", "/"},
		{"   /foo/   ", "/foo"},
		{"/foo/", "/foo"},
		{"foo/bar", "/foo/bar"},                               // missing leading slash
		{"/foo//bar///baz/", "/foo/bar/baz"},                  // collapsed runs
		{"/foo/./bar", "/foo/bar"},                            // dot segment
		{"/foo/bar/../baz", "/foo/baz"},                       // dot-dot inside
		{"/../../etc/passwd", "/etc/passwd"},                  // dot-dot escaping root clamps
		{"./a/./b/", "/a/b"},                                  // leading dot + relative
		{"/public-data//user///xiaobaowen//xxx/", "/public-data/user/xiaobaowen/xxx"},
	}
	for _, c := range cases {
		if got := normalizeQuotaPath(c.in); got != c.want {
			t.Errorf("normalizeQuotaPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathHasPrefix(t *testing.T) {
	cases := []struct {
		p, prefix string
		want      bool
	}{
		// Basic component-boundary cases — the whole reason we use filepath.Rel.
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/bc", "/a/b", false},  // sibling — must NOT match
		{"/a/b", "/a/b/c", false}, // shallower — must NOT match
		{"/a", "/b", false},       // unrelated

		// Root prefix is universal.
		{"/", "/", true},
		{"/x", "/", true},
		{"/a/b/c", "/", true},

		// Dirty inputs must canonicalize before comparison.
		{"//a//b/../c", "/a", true},
		{"/foo//bar/", "/foo/bar", true},
		{"/foo/bar/../baz", "/foo", true},
		{"/foo/bar/../../etc", "/foo", false},

		// Real-world recycle-bin nesting.
		{"/public-data/user/xiaobaowen/x", "/public-data/user/xiaobaowen", true},
		{"/public-data/user/alice/x", "/public-data/user/xiaobaowen", false},
		{"/public-data/user/alice/x", "/public-data/user", true},
	}
	for _, c := range cases {
		if got := pathHasPrefix(c.p, c.prefix); got != c.want {
			t.Errorf("pathHasPrefix(%q, %q) = %v, want %v", c.p, c.prefix, got, c.want)
		}
	}
}

func TestPickRecycleForPath(t *testing.T) {
	recycles := []recycleEntry{
		{ID: 1, Path: "/public-data/user"},
		{ID: 2, Path: "/public-data/user/xiaobaowen"},
		{ID: 3, Path: "/"},
	}

	cases := []struct {
		name    string
		query   string
		wantID  int64
		wantOK  bool
	}{
		// Longest-prefix wins — query lives under both recycles 1 and 2, picks 2.
		{"longest-prefix wins", "/public-data/user/xiaobaowen/xxx", 2, true},

		// Sibling user "alice" only matches the broader /public-data/user (id=1).
		{"sibling falls back to parent", "/public-data/user/alice/data", 1, true},

		// Exact match on a recycle path returns that recycle.
		{"exact match on parent", "/public-data/user", 1, true},
		{"exact match on child", "/public-data/user/xiaobaowen", 2, true},

		// Unrelated path falls back to root recycle (id=3) — root is universal.
		{"root fallback", "/random/path", 3, true},
		{"root itself", "/", 3, true},

		// Dirty inputs go through normalization before matching.
		{"dirty input matches child", "/public-data//user/xiaobaowen/./foo/", 2, true},
		{"dot-dot resolves", "/public-data/user/xiaobaowen/foo/../bar", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pickRecycleForPath(recycles, c.query)
			if ok != c.wantOK {
				t.Fatalf("pickRecycleForPath(%q) ok=%v, want %v", c.query, ok, c.wantOK)
			}
			if ok && got.ID != c.wantID {
				t.Errorf("pickRecycleForPath(%q) id=%d, want %d (matched path=%q)", c.query, got.ID, c.wantID, got.Path)
			}
		})
	}
}

// No-match case: without a root recycle, an unrelated query returns ok=false.
func TestPickRecycleForPath_NoMatch(t *testing.T) {
	recycles := []recycleEntry{
		{ID: 1, Path: "/public-data/user"},
		{ID: 2, Path: "/public-data/user/xiaobaowen"},
	}
	if _, ok := pickRecycleForPath(recycles, "/random/path"); ok {
		t.Errorf("expected no match for /random/path against non-root recycles")
	}
}
