// Package ghelp holds small helpers shared by the dialect packages to translate Python idioms.
package ghelp

// Iff is Python's conditional expression `a if c else b`.
func Iff[T any](c bool, a, b T) T {
	if c {
		return a
	}
	return b
}

// Pfx is the `f"{prefix}{s}" if s else ""` idiom.
func Pfx(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

// Sfx is the `f"{s}{suffix}" if s else ""` idiom.
func Sfx(s, suffix string) string {
	if s == "" {
		return ""
	}
	return s + suffix
}

// Around is the `f"{prefix}{s}{suffix}" if s else ""` idiom.
func Around(prefix, s, suffix string) string {
	if s == "" {
		return ""
	}
	return prefix + s + suffix
}

// VersionGE reports whether a dialect version is >= (major, minor[, patch]).
func VersionGE(v [3]int, want ...int) bool {
	for i := 0; i < 3; i++ {
		w := 0
		if i < len(want) {
			w = want[i]
		}
		if v[i] != w {
			return v[i] > w
		}
	}
	return true
}
