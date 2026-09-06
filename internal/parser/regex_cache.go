package parser

import (
	"regexp"
	"sync"
)

// RegexCache holds pre-compiled regex patterns for SQL parsing
type RegexCache struct {
	// Type inference patterns
	CoalesceRe    *regexp.Regexp
	ThenRe        *regexp.Regexp
	ArithmeticRe  *regexp.Regexp
	TableColRefRe *regexp.Regexp

	// CTE patterns
	WithRe func(cteAlias string) *regexp.Regexp

	// Column reference patterns
	ColRefPattern func(cteColumn string) *regexp.Regexp

	// Aggregate function patterns
	ArrayAggPattern  func(cteColumn string) *regexp.Regexp
	StringAggPattern func(cteColumn string) *regexp.Regexp
	CountPattern     func(cteColumn string) *regexp.Regexp
	SumPattern       func(cteColumn string) *regexp.Regexp
	AvgPattern       func(cteColumn string) *regexp.Regexp
	MaxPattern       func(cteColumn string) *regexp.Regexp
	MinPattern       func(cteColumn string) *regexp.Regexp
	LengthPattern    func(cteColumn string) *regexp.Regexp
	ExtractPattern   func(cteColumn string) *regexp.Regexp
}

var regexCache *RegexCache

var initRegexCache = sync.OnceFunc(func() {
	regexCache = &RegexCache{
		// Pre-compile static patterns
		CoalesceRe:    regexp.MustCompile(`(?i)COALESCE\s*\(\s*([^,)]+)`),
		ThenRe:        regexp.MustCompile(`(?i)THEN\s+'([^']*)'`),
		ArithmeticRe:  regexp.MustCompile(`\s*[+\-*/]\s*`),
		TableColRefRe: regexp.MustCompile(`^(\w+)\.(\w+)$`),

		// Dynamic pattern generators (compiled on-demand and cached)
		WithRe: func(cteAlias string) *regexp.Regexp {
			return regexp.MustCompile(`(?is)` + regexp.QuoteMeta(cteAlias) + `\s+AS\s*\((.*?)\)(?:\s*,|\s+SELECT)`)
		},

		ColRefPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)(\w+)\.(\w+)\s+AS\s+` + cteColumn)
		},

		// Aggregate function pattern generators
		ArrayAggPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)ARRAY_AGG\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
		StringAggPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)STRING_AGG\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
		CountPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)COUNT\([^)]*\)(?:\s+FILTER\s*\([^)]*\))?\s+(?:AS\s+)?` + cteColumn)
		},
		SumPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)SUM\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
		AvgPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)AVG\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
		MaxPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)MAX\(([^)]+)\)\s+(?:AS\s+)?` + cteColumn)
		},
		MinPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)MIN\(([^)]+)\)\s+(?:AS\s+)?` + cteColumn)
		},
		LengthPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)LENGTH\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
		ExtractPattern: func(cteColumn string) *regexp.Regexp {
			return regexp.MustCompile(`(?i)EXTRACT\([^)]+\)\s+(?:AS\s+)?` + cteColumn)
		},
	}
})

// GetRegexCache returns the global regex cache (initialized once)
func GetRegexCache() *RegexCache {
	initRegexCache()
	return regexCache
}

// Pattern cache for frequently used dynamic patterns
type patternCache struct {
	mu       sync.RWMutex
	patterns map[string]*regexp.Regexp
}

var dynPatternCache = &patternCache{
	patterns: make(map[string]*regexp.Regexp),
}

// GetCachedPattern returns a cached compiled pattern or compiles and caches it
func GetCachedPattern(key string, compile func() *regexp.Regexp) *regexp.Regexp {
	// Try read lock first
	dynPatternCache.mu.RLock()
	if pattern, ok := dynPatternCache.patterns[key]; ok {
		dynPatternCache.mu.RUnlock()
		return pattern
	}
	dynPatternCache.mu.RUnlock()

	// Compile and cache with write lock
	dynPatternCache.mu.Lock()
	defer dynPatternCache.mu.Unlock()

	// Double-check after acquiring write lock
	if pattern, ok := dynPatternCache.patterns[key]; ok {
		return pattern
	}

	pattern := compile()
	dynPatternCache.patterns[key] = pattern
	return pattern
}
