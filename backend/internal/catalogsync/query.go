package catalogsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// QueryVersion is the only version of the condition tree this build reads and
// writes. The version sits at the root of every stored query so a later
// grammar can be told apart from this one without guessing.
const QueryVersion = 1

// Limits that keep a stored query small enough to compile and read.
const (
	maxQueryDepth    = 6
	maxQueryNodes    = 64
	maxQueryValues   = 100
	maxQueryTextLen  = 200
	dayMs            = int64(24 * time.Hour / time.Millisecond)
	minuteMs         = float64(time.Minute / time.Millisecond)
	dateLayoutQuery  = "2006-01-02"
	maxIntegerFields = float64(1 << 53)
)

// Query is a versioned condition tree over catalog items: the stored form of
// block.criteria, filler_pool.criteria and the criteria block item. Its JSON
// is {"version": 1} plus exactly one of
//
//	"all": [node, ...]   every child matches
//	"any": [node, ...]   at least one child matches
//	"not": node          the child does not match
//	"field"/"op"/"value" one condition on a catalog field
//
// at every level, the root included. Children are nodes of the same shape
// (without a version). Parsing is strict: unknown keys, fields, operators and
// malformed values are all rejected, so a stored query is always compilable.
type Query struct {
	Version int `json:"version"`
	Node
}

// Node is one level of the tree: a group (all / any / not) or one condition.
type Node struct {
	All   []Node          `json:"all,omitempty"`
	Any   []Node          `json:"any,omitempty"`
	Not   *Node           `json:"not,omitempty"`
	Field string          `json:"field,omitempty"`
	Op    string          `json:"op,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Operators.
const (
	OpEquals   = "equals"
	OpIn       = "in"
	OpContains = "contains"
	OpLt       = "lt"
	OpLte      = "lte"
	OpGt       = "gt"
	OpGte      = "gte"
	OpBetween  = "between"
)

type fieldType int

const (
	typeList   fieldType = iota // curated names: exact match on the normalized name
	typeText                    // free text: accent- and case-insensitive contains
	typeEnum                    // fixed values: exact match
	typeNumber                  // numbers: equals, comparisons, between
	typeDate                    // dates (YYYY-MM-DD, UTC): the same operators, day granularity
)

// fieldSpec describes one queryable field.
type fieldSpec struct {
	typ fieldType
	// Column is the catalog_item column of a scalar field.
	column string
	// integer rejects fractional numbers (years, season/episode numbers).
	integer bool
	// scale multiplies a number into the column's unit (duration: minutes to ms).
	scale float64
	// For list fields (and person): the link and dimension tables, plus an
	// optional link kind column filter.
	link, dim, dimCol, kindCol, kindVal string
	// maxLen bounds an enum value.
	maxLen int
}

// queryFields is the whole queryable surface. Nothing outside this table can
// reach the SQL, which together with bind parameters for every value is what
// makes compilation injection-safe.
var queryFields = map[string]fieldSpec{
	"genre":             {typ: typeList, link: "catalog_item_genre", dim: "genre", dimCol: "genre_id"},
	"studio":            {typ: typeList, link: "catalog_item_studio", dim: "studio", dimCol: "studio_id"},
	"tag":               {typ: typeList, link: "catalog_item_tag", dim: "tag", dimCol: "tag_id"},
	"audio_language":    {typ: typeList, link: "catalog_item_language", dim: "language", dimCol: "language_id", kindCol: "kind", kindVal: "audio"},
	"subtitle_language": {typ: typeList, link: "catalog_item_language", dim: "language", dimCol: "language_id", kindCol: "kind", kindVal: "subtitle"},
	"title":             {typ: typeText, column: "search_text"},
	"overview":          {typ: typeText, column: "overview_norm"},
	"person":            {typ: typeText, link: "catalog_item_person", dim: "person", dimCol: "person_id"},
	"actor":             {typ: typeText, link: "catalog_item_person", dim: "person", dimCol: "person_id", kindCol: "kind", kindVal: "actor"},
	"director":          {typ: typeText, link: "catalog_item_person", dim: "person", dimCol: "person_id", kindCol: "kind", kindVal: "director"},
	"kind":              {typ: typeEnum, column: "kind"},
	"content_rating":    {typ: typeEnum, column: "content_rating", maxLen: 32},
	"year":              {typ: typeNumber, column: "production_year", integer: true},
	"duration":          {typ: typeNumber, column: "duration_ms", scale: minuteMs},
	"community_rating":  {typ: typeNumber, column: "community_rating"},
	"critic_rating":     {typ: typeNumber, column: "critic_rating"},
	"season_number":     {typ: typeNumber, column: "season_number", integer: true},
	"episode_number":    {typ: typeNumber, column: "episode_number", integer: true},
	"premiere_date":     {typ: typeDate, column: "premiere_date"},
	"date_added":        {typ: typeDate, column: "date_added"},
	// Everything one source brought in, which is how a channel is built from a
	// plugin-backed source without listing its items one by one.
	"media_source_id": {typ: typeEnum, column: "media_source_id", maxLen: 64},
}

// QueryFieldNames lists the queryable fields, for error messages and docs.
func QueryFieldNames() []string {
	return []string{
		"genre", "studio", "tag", "audio_language", "subtitle_language", "title", "overview", "person", "actor", "director",
		"kind", "content_rating", "year", "duration", "community_rating", "critic_rating",
		"season_number", "episode_number", "premiere_date", "date_added", "media_source_id",
	}
}

var enumKinds = map[string]bool{"movie": true, "episode": true}

func opsFor(t fieldType) []string {
	switch t {
	case typeList, typeEnum:
		return []string{OpEquals, OpIn}
	case typeText:
		return []string{OpContains}
	default:
		return []string{OpEquals, OpLt, OpLte, OpGt, OpGte, OpBetween}
	}
}

func hasOp(t fieldType, op string) bool {
	for _, o := range opsFor(t) {
		if o == op {
			return true
		}
	}
	return false
}

// ErrLegacyCriteria is returned for the pre-tree criteria shape
// ({kind, genres, year_from, year_to}), which clients can no longer send.
var ErrLegacyCriteria = errors.New(`criteria uses the legacy {kind, genres, year_from, year_to} shape, which is no longer accepted; send a versioned condition tree such as {"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]}`)

// IsLegacyCriteria reports whether raw is a criteria JSON object in the old
// fixed shape: an object without a "version" key.
func IsLegacyCriteria(raw string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false
	}
	_, versioned := probe["version"]
	return !versioned
}

// ParseQuery strictly parses and validates a condition tree and returns it in
// canonical form (values normalized). Errors name the offending node, for
// example `all[1].op: "contains" is not valid for field "year"`.
func ParseQuery(raw []byte) (*Query, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("a query is required")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, errors.New("a query must be a JSON object")
	}
	if _, versioned := probe["version"]; !versioned {
		if _, ok := probe["kind"]; ok {
			return nil, ErrLegacyCriteria
		}
		if _, ok := probe["genres"]; ok {
			return nil, ErrLegacyCriteria
		}
		return nil, errors.New(`version is required (currently 1)`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var q Query
	if err := dec.Decode(&q); err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	if dec.More() {
		return nil, errors.New("invalid query: trailing data")
	}
	if q.Version != QueryVersion {
		return nil, fmt.Errorf("version %d is not supported (currently %d)", q.Version, QueryVersion)
	}
	nodes := 0
	if err := normalizeNode(&q.Node, "", 1, &nodes); err != nil {
		return nil, err
	}
	return &q, nil
}

// JSON is the canonical stored form of the query.
func (q *Query) JSON() string {
	b, err := json.Marshal(q)
	if err != nil {
		// A validated query always marshals; this only guards a programming error.
		panic(fmt.Sprintf("catalogsync: marshal query: %v", err))
	}
	return string(b)
}

// normalizeNode validates n (at the given JSON path and depth) and rewrites
// condition values into their canonical form.
func normalizeNode(n *Node, path string, depth int, nodes *int) error {
	*nodes++
	if *nodes > maxQueryNodes {
		return fmt.Errorf("the query has more than %d nodes", maxQueryNodes)
	}
	if depth > maxQueryDepth {
		return fmt.Errorf("%snesting is deeper than %d levels", prefix(path), maxQueryDepth)
	}
	kinds := 0
	if n.All != nil {
		kinds++
	}
	if n.Any != nil {
		kinds++
	}
	if n.Not != nil {
		kinds++
	}
	isCondition := n.Field != "" || n.Op != "" || len(n.Value) > 0
	if isCondition {
		kinds++
	}
	if kinds != 1 {
		return fmt.Errorf("%sexactly one of all, any, not or a field condition is required", prefix(path))
	}
	switch {
	case n.All != nil:
		return normalizeChildren(n.All, path+"all", depth, nodes)
	case n.Any != nil:
		return normalizeChildren(n.Any, path+"any", depth, nodes)
	case n.Not != nil:
		return normalizeNode(n.Not, path+"not.", depth+1, nodes)
	}
	return normalizeCondition(n, path)
}

func prefix(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(path, ".") + ": "
}

func normalizeChildren(children []Node, path string, depth int, nodes *int) error {
	if len(children) == 0 {
		return fmt.Errorf("%s must not be empty", path)
	}
	for i := range children {
		if err := normalizeNode(&children[i], fmt.Sprintf("%s[%d].", path, i), depth+1, nodes); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCondition(n *Node, path string) error {
	spec, ok := queryFields[n.Field]
	if !ok {
		return fmt.Errorf("%sfield %q is not supported (use one of %s)", prefix(path), n.Field, strings.Join(QueryFieldNames(), ", "))
	}
	if !hasOp(spec.typ, n.Op) {
		return fmt.Errorf("%sop %q is not valid for field %q (use one of %s)", prefix(path), n.Op, n.Field, strings.Join(opsFor(spec.typ), ", "))
	}
	if len(n.Value) == 0 {
		return fmt.Errorf("%svalue is required", prefix(path))
	}
	v, err := parseValue(spec, n.Op, n.Value)
	if err != nil {
		return fmt.Errorf("%svalue for %q: %w", prefix(path), n.Field, err)
	}
	canon, err := json.Marshal(v.canonical())
	if err != nil {
		return err
	}
	n.Value = canon
	return nil
}

// condValue is a parsed, validated condition value.
type condValue struct {
	strs []string  // text/list/enum values (list/enum already folded or exact)
	nums []float64 // number values, or date day-starts as epoch ms
	// dates keeps the original YYYY-MM-DD strings for the canonical form.
	dates []string
	multi bool // the value was written as a JSON array
}

func (v condValue) canonical() any {
	switch {
	case v.dates != nil:
		if v.multi {
			return v.dates
		}
		return v.dates[0]
	case v.nums != nil:
		if v.multi {
			return v.nums
		}
		return v.nums[0]
	}
	if v.multi {
		return v.strs
	}
	return v.strs[0]
}

func checkText(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("must not be empty")
	}
	if len([]rune(s)) > maxQueryTextLen {
		return "", fmt.Errorf("must be at most %d characters", maxQueryTextLen)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("must not contain control characters")
		}
	}
	return s, nil
}

func parseValue(spec fieldSpec, op string, raw json.RawMessage) (condValue, error) {
	switch spec.typ {
	case typeList, typeEnum, typeText:
		return parseStringValue(spec, op, raw)
	}
	return parseNumberValue(spec, op, raw)
}

func parseStringValue(spec fieldSpec, op string, raw json.RawMessage) (condValue, error) {
	var out condValue
	var vals []string
	if op == OpIn {
		if err := json.Unmarshal(raw, &vals); err != nil {
			return out, errors.New("must be an array of strings")
		}
		if len(vals) == 0 {
			return out, errors.New("must not be empty")
		}
		if len(vals) > maxQueryValues {
			return out, fmt.Errorf("must have at most %d values", maxQueryValues)
		}
		out.multi = true
	} else {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return out, errors.New("must be a string")
		}
		vals = []string{s}
	}
	seen := map[string]bool{}
	for _, s := range vals {
		s, err := checkText(s)
		if err != nil {
			return out, err
		}
		if spec.typ == typeEnum && spec.column == "kind" {
			if !enumKinds[s] {
				return out, errors.New("must be movie or episode")
			}
		}
		if spec.typ == typeEnum && spec.maxLen > 0 && len([]rune(s)) > spec.maxLen {
			return out, fmt.Errorf("must be at most %d characters", spec.maxLen)
		}
		key := s
		if spec.typ != typeEnum {
			key = FoldText(s)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out.strs = append(out.strs, s)
	}
	return out, nil
}

func parseNumberValue(spec fieldSpec, op string, raw json.RawMessage) (condValue, error) {
	var out condValue
	var items []json.RawMessage
	if op == OpBetween {
		if err := json.Unmarshal(raw, &items); err != nil || len(items) != 2 {
			return out, errors.New("must be a [from, to] pair")
		}
		out.multi = true
	} else {
		items = []json.RawMessage{raw}
	}
	for _, it := range items {
		if spec.typ == typeDate {
			var s string
			if err := json.Unmarshal(it, &s); err != nil {
				return out, errors.New("must be a date string YYYY-MM-DD")
			}
			t, err := time.Parse(dateLayoutQuery, strings.TrimSpace(s))
			if err != nil {
				return out, errors.New("must be a date string YYYY-MM-DD")
			}
			out.dates = append(out.dates, t.Format(dateLayoutQuery))
			out.nums = append(out.nums, float64(t.UnixMilli()))
			continue
		}
		var f float64
		if err := json.Unmarshal(it, &f); err != nil {
			return out, errors.New("must be a number")
		}
		if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > maxIntegerFields {
			return out, errors.New("is out of range")
		}
		if spec.integer && f != math.Trunc(f) {
			return out, errors.New("must be a whole number")
		}
		out.nums = append(out.nums, f)
	}
	if op == OpBetween && out.nums[0] > out.nums[1] {
		return out, errors.New("the range start must not be after its end")
	}
	return out, nil
}

// Where compiles the query into a WHERE clause (and its arguments) selecting
// the tenant's matching catalog_item rows. Every value is a bind argument and
// every identifier comes from queryFields, so no query text can alter the SQL.
// A condition never matches an item that lacks the field (a NULL year is not
// "before 1990"), so negating it with not matches those items.
func (q *Query) Where(tenantID string) (string, []any) {
	var args []any
	sql := compileNode(q.Node, &args)
	return "tenant_id = ? AND (" + sql + ")", append([]any{tenantID}, args...)
}

func compileNode(n Node, args *[]any) string {
	switch {
	case n.All != nil:
		return compileGroup(n.All, " AND ", args)
	case n.Any != nil:
		return compileGroup(n.Any, " OR ", args)
	case n.Not != nil:
		return "NOT (" + compileNode(*n.Not, args) + ")"
	}
	return compileCondition(n, args)
}

func compileGroup(children []Node, sep string, args *[]any) string {
	parts := make([]string, len(children))
	for i, c := range children {
		parts[i] = compileNode(c, args)
	}
	return "(" + strings.Join(parts, sep) + ")"
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// escapeLike escapes the LIKE wildcards of a user value (used with ESCAPE '\').
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func compileCondition(n Node, args *[]any) string {
	spec := queryFields[n.Field]
	// n was validated by ParseQuery, so these parses cannot fail.
	v, _ := parseValue(spec, n.Op, n.Value)

	switch spec.typ {
	case typeList:
		for _, s := range v.strs {
			*args = append(*args, FoldText(s))
		}
		return existsLink(spec, "d.name_norm IN ("+placeholders(len(v.strs))+")")
	case typeText:
		pattern := "%" + escapeLike(FoldText(v.strs[0])) + "%"
		*args = append(*args, pattern)
		if spec.link != "" {
			return existsLink(spec, `d.name_norm LIKE ? ESCAPE '\'`)
		}
		return spec.column + ` LIKE ? ESCAPE '\'`
	case typeEnum:
		for _, s := range v.strs {
			*args = append(*args, s)
		}
		if len(v.strs) == 1 {
			return spec.column + " = ?"
		}
		return spec.column + " IN (" + placeholders(len(v.strs)) + ")"
	case typeDate:
		return compileDate(spec.column, n.Op, v.nums, args)
	}
	scale := spec.scale
	if scale == 0 {
		scale = 1
	}
	return compileNumber(spec.column, n.Op, v.nums, scale, args)
}

// existsLink is an EXISTS over a link table joined to its dimension table on
// the normalized name (tenant_id leads every key involved).
func existsLink(spec fieldSpec, nameCond string) string {
	s := "EXISTS (SELECT 1 FROM " + spec.link + " l JOIN " + spec.dim + " d ON d.tenant_id = l.tenant_id AND d.id = l." + spec.dimCol +
		" WHERE l.tenant_id = catalog_item.tenant_id AND l.catalog_item_id = catalog_item.id"
	if spec.kindCol != "" {
		s += " AND l." + spec.kindCol + " = '" + spec.kindVal + "'"
	}
	return s + " AND " + nameCond + ")"
}

func compileNumber(col, op string, nums []float64, scale float64, args *[]any) string {
	val := func(f float64) float64 { return f * scale }
	notNull := col + " IS NOT NULL AND "
	switch op {
	case OpBetween:
		*args = append(*args, val(nums[0]), val(nums[1]))
		return "(" + notNull + col + " >= ? AND " + col + " <= ?)"
	case OpEquals:
		*args = append(*args, val(nums[0]))
		return "(" + notNull + col + " = ?)"
	}
	cmp := map[string]string{OpLt: "<", OpLte: "<=", OpGt: ">", OpGte: ">="}[op]
	*args = append(*args, val(nums[0]))
	return "(" + notNull + col + " " + cmp + " ?)"
}

// compileDate compares at day granularity: a date means the whole UTC day, so
// equals is [day, next day), lte includes the day itself and gt starts after it.
func compileDate(col, op string, days []float64, args *[]any) string {
	notNull := col + " IS NOT NULL AND "
	start := func(i int) int64 { return int64(days[i]) }
	switch op {
	case OpBetween:
		*args = append(*args, start(0), start(1)+dayMs)
		return "(" + notNull + col + " >= ? AND " + col + " < ?)"
	case OpEquals:
		*args = append(*args, start(0), start(0)+dayMs)
		return "(" + notNull + col + " >= ? AND " + col + " < ?)"
	case OpLt:
		*args = append(*args, start(0))
		return "(" + notNull + col + " < ?)"
	case OpLte:
		*args = append(*args, start(0)+dayMs)
		return "(" + notNull + col + " < ?)"
	case OpGt:
		*args = append(*args, start(0)+dayMs)
		return "(" + notNull + col + " >= ?)"
	}
	*args = append(*args, start(0)) // gte
	return "(" + notNull + col + " >= ?)"
}
