package s3cli

import "testing"

// An S3 keyspace is flat: the slashes in "2026/08/dump.sql.gz" are ordinary
// characters, so "*" has to cross them. path.Match would not, which is why this
// translates to a regexp instead.
func TestCompileGlobSpansSlashes(t *testing.T) {
	for _, tc := range []struct {
		pattern, key string
		want         bool
	}{
		{"*.sql.gz", "dump.sql.gz", true},
		{"*.sql.gz", "2026/08/dump.sql.gz", true},
		{"2026/*", "2026/08/dump.sql.gz", true},
		{"2026/*", "2025/08/dump.sql.gz", false},
		{"dump-?.gz", "dump-1.gz", true},
		{"dump-?.gz", "dump-12.gz", false},
		{"dump.sql.gz", "dump.sql.gz", true},
		{"dump.sql.gz", "xdump.sql.gz", false},
		// A dot is a literal, not "any character" — a glob is not a regexp.
		{"a.c", "abc", false},
		{"a.c", "a.c", true},
	} {
		re, err := compileGlob(tc.pattern)
		if err != nil {
			t.Fatalf("compileGlob(%q) = %v", tc.pattern, err)
		}
		if got := re.MatchString(tc.key); got != tc.want {
			t.Errorf("%q matches %q = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
}

func TestKeyFilter(t *testing.T) {
	for _, tc := range []struct {
		name             string
		include, exclude []string
		key              string
		want             bool
	}{
		{"no patterns takes everything", nil, nil, "anything", true},
		{"include must match", []string{"*.gz"}, nil, "dump.gz", true},
		{"include that misses drops the key", []string{"*.gz"}, nil, "dump.txt", false},
		{"any include is enough", []string{"*.gz", "*.txt"}, nil, "dump.txt", true},
		{"exclude wins over include", []string{"*"}, []string{"e2e-*"}, "e2e-a.gz", false},
		{"exclude alone", nil, []string{"e2e-*"}, "nightly.gz", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := keyFilter{include: tc.include, exclude: tc.exclude}
			if err := f.compile(); err != nil {
				t.Fatal(err)
			}
			if got := f.match(tc.key); got != tc.want {
				t.Errorf("match(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// Only "*" and "?" are special, matching s5cmd: a bracket, a brace or a dot in
// a key is a literal, so "[2026]/dump" names exactly that key rather than a
// character class over one of four digits.
func TestCompileGlobTreatsEverythingElseAsLiteral(t *testing.T) {
	re, err := compileGlob("[2026]/dump.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("[2026]/dump.gz") {
		t.Error("a bracketed key did not match itself")
	}
	if re.MatchString("2/dump.gz") {
		t.Error("brackets were treated as a character class")
	}
}

func TestGlobPrefix(t *testing.T) {
	for _, tc := range []struct{ pattern, want string }{
		{"2026/08/*.gz", "2026/08/"},
		{"*.gz", ""},
		{"dump.sql.gz", "dump.sql.gz"},
		{"dump-?.gz", "dump-"},
	} {
		if got := globPrefix(tc.pattern); got != tc.want {
			t.Errorf("globPrefix(%q) = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

func TestHasGlob(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"b/k", false},
		{"b/*.gz", true},
		{"b/dump-?.gz", true},
	} {
		if got := hasGlob(tc.ref); got != tc.want {
			t.Errorf("hasGlob(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}
