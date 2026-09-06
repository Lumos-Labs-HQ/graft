package seeder

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

//go:embed data.json
var dataJSON []byte

type FakeData struct {
	FirstNames   []string `json:"firstNames"`
	LastNames    []string `json:"lastNames"`
	Domains      []string `json:"domains"`
	ImageUrls    []string `json:"imageUrls"`
	AvatarUrls   []string `json:"avatarUrls"`
	DocumentUrls []string `json:"documentUrls"`
	Titles       []string `json:"titles"`
	Sentences    []string `json:"sentences"`
	Paragraphs   []string `json:"paragraphs"`
	Cities       []string `json:"cities"`
	States       []string `json:"states"`
	Streets      []string `json:"streets"`
	Companies    []string `json:"companies"`
	Products     []string `json:"products"`
	Tags         []string `json:"tags"`
	Statuses     []string `json:"statuses"`
	Categories   []string `json:"categories"`
}

type patternEntry struct {
	keywords  []string
	generator func() any
}

type typeEntry struct {
	match     string
	generator func() any
}

type DataGenerator struct {
	rand        *rand.Rand
	counter     int
	fakeData    *FakeData
	patternList []patternEntry
	typeList    []typeEntry
}

func NewDataGenerator() (*DataGenerator, error) {
	var data FakeData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		return nil, fmt.Errorf("failed to parse embedded fake data: %w", err)
	}

	g := &DataGenerator{
		rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
		fakeData: &data,
	}
	g.initPatterns()
	g.initTypeList()
	return g, nil
}

func (g *DataGenerator) initPatterns() {
	rawPatterns := map[string]func() any{
		// Identity
		"first_name|firstname|fname":                              g.randomFrom(g.fakeData.FirstNames, "John"),
		"last_name|lastname|lname":                                g.randomFrom(g.fakeData.LastNames, "Doe"),
		"email|e_mail":                                            g.generateEmail,
		"username|user_name|login|handle|screen_name":             g.generateUsername,
		"password|passwd|pwd|secret":                              g.generatePassword,
		"token|api_key|access_token|refresh_token|jwt|auth_token": g.generateToken,
		"name": g.generateFullName,

		// Content
		"title|headline|subject":                 g.randomFrom(g.fakeData.Titles, "Sample Title"),
		"description|summary|excerpt":            g.randomFrom(g.fakeData.Paragraphs, "Sample description"),
		"content|body|message|comment|note|text": g.randomFrom(g.fakeData.Paragraphs, "Sample content"),
		"bio|about|biography":                    g.randomFrom(g.fakeData.Paragraphs, "Short bio text"),
		"phone|tel|mobile|cell":                  g.generatePhone,
		"url|link|website|homepage":              g.generateURL,
		"slug|permalink|path":                    g.generateSlug,

		// Media
		"image|img|photo|picture|thumbnail|banner|cover": g.randomFrom(g.fakeData.ImageUrls, "https://picsum.photos/400/300"),
		"avatar|profile_pic|profile_image":               g.randomFrom(g.fakeData.AvatarUrls, "https://i.pravatar.cc/150"),

		// Location
		"address|addr":          g.generateAddress,
		"city":                  g.randomFrom(g.fakeData.Cities, "New York"),
		"state|province|region": g.randomFrom(g.fakeData.States, "California"),
		"zip|postal|postcode":   g.generateZip,
		"country|nation":        g.randomFrom([]string{"US", "CA", "GB", "DE", "FR", "JP", "AU", "BR", "IN", "MX"}, "US"),
		"latitude|lat":          func() any { return float64(g.rand.Intn(180000)-90000) / 1000.0 },
		"longitude|lng|lon":     func() any { return float64(g.rand.Intn(360000)-180000) / 1000.0 },

		// Business
		"company|organization|org|firm": g.randomFrom(g.fakeData.Companies, "Tech Company Inc"),
		"product|plan|package|item|sku": g.randomFrom(g.fakeData.Products, "Product"),
		"tag|label":                     g.randomFrom(g.fakeData.Tags, "tag"),
		"status|state":                  g.randomFrom(g.fakeData.Statuses, "active"),
		"category|cat|genre|topic":      g.randomFrom(g.fakeData.Categories, "General"),
		"priority":                      g.randomFrom([]string{"low", "medium", "high", "urgent"}, "medium"),
		"role|permission|access_level":  g.randomFrom([]string{"user", "admin", "editor", "viewer", "guest"}, "user"),
		"gender|sex":                    g.randomFrom([]string{"male", "female", "non-binary", "other", "prefer-not-to-say"}, "other"),

		// Technical
		"color|colour|hex":                     g.generateColor,
		"ip|ip_address|remote_addr":            g.generateIP,
		"locale|lang|language":                 g.randomFrom([]string{"en", "es", "fr", "de", "ja", "zh", "pt", "it", "ko", "ru"}, "en"),
		"currency|curr":                        g.randomFrom([]string{"USD", "EUR", "GBP", "JPY", "CAD", "AUD", "CHF", "CNY"}, "USD"),
		"metadata|meta|extra|attrs|properties": func() any { return `{"generated": true}` },
		"hash|checksum|md5|sha|sha256|digest":  g.generateHash,
		"code|short_code|reference|ref_no":     g.generateRefCode,
		"version|ver":                          func() any { return fmt.Sprintf("%d.%d.%d", g.rand.Intn(5), g.rand.Intn(20), g.rand.Intn(20)) },

		// Temporal
		"dob|birth_date|birthdate|date_of_birth": func() any {
			return time.Now().AddDate(-18-g.rand.Intn(60), -g.rand.Intn(12), -g.rand.Intn(28))
		},
		"age":                      func() any { return g.rand.Intn(80) + 18 },
		"duration|elapsed|timeout": func() any { return g.rand.Intn(3600) + 1 },
		"sort_order|display_order|position|rank|seq|sequence": func() any { return g.rand.Intn(1000) },

		// Numeric / Finance
		"price|amount|cost|fee|charge|salary|wage": func() any { return float64(g.rand.Intn(1000000)) / 100.0 },
		"quantity|qty|stock|inventory|count":       func() any { return g.rand.Intn(1000) + 1 },
		"rating|score|stars|grade":                 func() any { return g.rand.Intn(5) + 1 },
		"percent|percentage|pct|rate|ratio":        func() any { return float64(g.rand.Intn(10000)) / 100.0 },
		"progress|completion|completion_rate":      func() any { return g.rand.Intn(101) },
		"size|file_size|bytes|length|width|height": func() any { return g.rand.Intn(104857600) + 1024 },

		// Boolean states (not prefixes — those are handled separately)
		"active|enabled|verified|confirmed|published|visible|deleted|archived|disabled|locked|banned|approved|featured|premium|subscribed|notify|subscribe|public|private|read_only|readonly|mandatory|required|optional|default|highlighted": func() any { return g.rand.Intn(2) == 1 },

		// Security — Documents always NULL
		"document|doc|file|attachment|pdf|upload": func() any { return nil },
	}

	g.patternList = make([]patternEntry, 0, len(rawPatterns))
	for pattern, generator := range rawPatterns {
		g.patternList = append(g.patternList, patternEntry{
			keywords:  strings.Split(pattern, "|"),
			generator: generator,
		})
	}
}

func (g *DataGenerator) initTypeList() {
	g.typeList = []typeEntry{
		{"BIGINT", func() any { return int64(g.rand.Intn(9000000) + 1) }},
		{"INT", func() any { return g.rand.Intn(1000000) + 1 }},
		{"SMALLINT", func() any { return g.rand.Intn(32767) + 1 }},
		{"TINYINT", func() any { return g.rand.Intn(127) + 1 }},
		{"MEDIUMINT", func() any { return g.rand.Intn(8388607) + 1 }},
		{"SERIAL", func() any { return g.rand.Intn(1000000) + 1 }},
		{"VARCHAR", func() any { return g.randomSentence() }},
		{"TEXT", func() any { return g.randomSentence() }},
		{"CHAR", func() any {
			chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			b := make([]byte, 6)
			for i := range b {
				b[i] = chars[g.rand.Intn(len(chars))]
			}
			return string(b)
		}},
		{"BOOL", func() any { return g.rand.Intn(2) == 1 }},
		{"TIMESTAMPTZ", func() any { return time.Now().AddDate(0, 0, -g.rand.Intn(365)) }},
		{"TIMESTAMP", func() any { return time.Now().AddDate(0, 0, -g.rand.Intn(365)) }},
		{"DATETIME", func() any { return time.Now().AddDate(0, 0, -g.rand.Intn(365)) }},
		{"DATE", func() any { return time.Now().AddDate(0, 0, -g.rand.Intn(365)) }},
		{"TIME", func() any {
			return fmt.Sprintf("%02d:%02d:%02d", g.rand.Intn(24), g.rand.Intn(60), g.rand.Intn(60))
		}},
		{"YEAR", func() any { return 2000 + g.rand.Intn(25) }},
		{"DECIMAL", func() any { return float64(g.rand.Intn(1000000)) / 100.0 }},
		{"NUMERIC", func() any { return float64(g.rand.Intn(1000000)) / 100.0 }},
		{"FLOAT", func() any { return float64(g.rand.Intn(1000000)) / 100.0 }},
		{"REAL", func() any { return float64(g.rand.Intn(1000000)) / 100.0 }},
		{"DOUBLE", func() any { return float64(g.rand.Intn(1000000)) / 100.0 }},
		{"UUID", func() any { return g.generateUUID() }},
		{"JSONB", func() any { return `{"generated": true}` }},
		{"JSON", func() any { return `{"generated": true}` }},
		{"BYTEA", func() any { return []byte("generated binary data") }},
		{"BLOB", func() any { return []byte("generated binary data") }},
		{"BINARY", func() any { return []byte("generated binary data") }},
		{"ARRAY", func() any { return "{item1,item2,item3}" }},
		{"ENUM", func() any { return "option_a" }},
	}
}

// generateUUID returns an RFC 4122 version 4 UUID.
func (g *DataGenerator) generateUUID() string {
	b := make([]byte, 16)
	g.rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// columnMatches checks if colLower matches keyword with word-boundary awareness.
func columnMatches(colLower, keyword string) bool {
	if colLower == keyword {
		return true
	}
	if strings.HasPrefix(colLower, keyword+"_") {
		return true
	}
	if strings.HasSuffix(colLower, "_"+keyword) {
		return true
	}
	if strings.Contains(colLower, "_"+keyword+"_") {
		return true
	}
	return false
}

func (g *DataGenerator) GenerateForColumn(colName, colType string, nullable bool, enumTypes map[string][]string) any {
	if nullable && g.rand.Intn(10) < 2 {
		return nil
	}

	// Check if the column type is a custom enum type
	if enumTypes != nil {
		if vals, ok := enumTypes[strings.ToLower(colType)]; ok && len(vals) > 0 {
			return vals[g.rand.Intn(len(vals))]
		}
	}

	colLower := strings.ToLower(colName)

	// Boolean prefixes: is_active, has_permission, can_edit, etc.
	boolPrefixes := []string{"is_", "has_", "can_", "should_", "was_", "did_", "allow_", "enable_", "require_"}
	for _, prefix := range boolPrefixes {
		if strings.HasPrefix(colLower, prefix) {
			return g.rand.Intn(2) == 1
		}
	}

	// Find the best matching pattern (longest keyword wins for specificity)
	var bestGen func() any
	bestLen := 0
	for _, entry := range g.patternList {
		for _, keyword := range entry.keywords {
			if columnMatches(colLower, keyword) && len(keyword) > bestLen {
				bestLen = len(keyword)
				bestGen = entry.generator
			}
		}
	}
	if bestGen != nil {
		return bestGen()
	}

	return g.Generate(colType, nullable)
}

var enumExtractRegex = regexp.MustCompile(`(?i)ENUM\s*\((.*)\)`)
var enumValueRegex = regexp.MustCompile(`['"]([^'"]*)['"]`)

func parseEnumValues(colType string) []string {
	m := enumExtractRegex.FindStringSubmatch(colType)
	if len(m) < 2 {
		return nil
	}
	var vals []string
	for _, sm := range enumValueRegex.FindAllStringSubmatch(m[1], -1) {
		if len(sm) >= 2 {
			vals = append(vals, sm[1])
		}
	}
	return vals
}

func (g *DataGenerator) Generate(colType string, nullable bool) any {
	if nullable && g.rand.Intn(10) < 2 {
		return nil
	}

	typeUpper := strings.ToUpper(strings.Split(colType, "(")[0])

	// Special handling for ENUM — parse values from the type definition
	if strings.Contains(strings.ToUpper(colType), "ENUM") {
		if vals := parseEnumValues(colType); len(vals) > 0 {
			return vals[g.rand.Intn(len(vals))]
		}
		return "option_a"
	}

	for _, entry := range g.typeList {
		if strings.Contains(typeUpper, entry.match) {
			return entry.generator()
		}
	}

	return g.randomSentence()
}

func (g *DataGenerator) randomFrom(slice []string, fallback string) func() any {
	return func() any {
		if len(slice) == 0 {
			return fallback
		}
		return slice[g.rand.Intn(len(slice))]
	}
}

func (g *DataGenerator) randomSentence() string {
	if len(g.fakeData.Sentences) == 0 {
		return "Sample text"
	}
	return g.fakeData.Sentences[g.rand.Intn(len(g.fakeData.Sentences))]
}

func (g *DataGenerator) generateEmail() any {
	g.counter++
	if len(g.fakeData.FirstNames) == 0 || len(g.fakeData.LastNames) == 0 || len(g.fakeData.Domains) == 0 {
		return fmt.Sprintf("user%d@example.com", g.counter)
	}
	first := strings.ToLower(g.fakeData.FirstNames[g.rand.Intn(len(g.fakeData.FirstNames))])
	last := strings.ToLower(g.fakeData.LastNames[g.rand.Intn(len(g.fakeData.LastNames))])
	domain := g.fakeData.Domains[g.rand.Intn(len(g.fakeData.Domains))]
	return fmt.Sprintf("%s.%s%d@%s", first, last, g.counter, domain)
}

func (g *DataGenerator) generateUsername() any {
	g.counter++
	first := strings.ToLower(g.randomFrom(g.fakeData.FirstNames, "user")().(string))
	last := strings.ToLower(g.randomFrom(g.fakeData.LastNames, "name")().(string))
	return fmt.Sprintf("%s_%s_%d", first, last, g.counter)
}

func (g *DataGenerator) generatePassword() any {
	chars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, 16)
	for i := range b {
		b[i] = chars[g.rand.Intn(len(chars))]
	}
	return string(b)
}

func (g *DataGenerator) generateToken() any {
	b := make([]byte, 32)
	g.rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (g *DataGenerator) generateFullName() any {
	first := g.randomFrom(g.fakeData.FirstNames, "John")().(string)
	last := g.randomFrom(g.fakeData.LastNames, "Doe")().(string)
	return fmt.Sprintf("%s %s", first, last)
}

func (g *DataGenerator) generatePhone() any {
	return fmt.Sprintf("+1-%03d-%03d-%04d", g.rand.Intn(900)+100, g.rand.Intn(900)+100, g.rand.Intn(10000))
}

func (g *DataGenerator) generateURL() any {
	words := []string{"about", "contact", "blog", "products", "services", "help", "faq", "terms", "privacy", "careers"}
	return fmt.Sprintf("https://example.com/%s", words[g.rand.Intn(len(words))])
}

func (g *DataGenerator) generateSlug() any {
	words := []string{"getting-started", "best-practices", "introduction", "advanced-guide", "quick-start", "tutorial", "reference", "changelog"}
	return words[g.rand.Intn(len(words))] + fmt.Sprintf("-%d", g.rand.Intn(1000))
}

func (g *DataGenerator) generateAddress() any {
	street := g.randomFrom(g.fakeData.Streets, "Main Street")().(string)
	city := g.randomFrom(g.fakeData.Cities, "New York")().(string)
	state := g.randomFrom(g.fakeData.States, "NY")().(string)
	return fmt.Sprintf("%d %s, %s, %s %05d", g.rand.Intn(9999)+1, street, city, state, g.rand.Intn(100000))
}

func (g *DataGenerator) generateZip() any {
	return fmt.Sprintf("%05d", g.rand.Intn(100000))
}

func (g *DataGenerator) generateColor() any {
	return fmt.Sprintf("#%06x", g.rand.Intn(0xffffff))
}

func (g *DataGenerator) generateIP() any {
	return fmt.Sprintf("%d.%d.%d.%d", g.rand.Intn(256), g.rand.Intn(256), g.rand.Intn(256), g.rand.Intn(256))
}

func (g *DataGenerator) generateHash() any {
	b := make([]byte, 16)
	g.rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (g *DataGenerator) generateRefCode() any {
	chars := "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[g.rand.Intn(len(chars))]
	}
	return string(b)
}
