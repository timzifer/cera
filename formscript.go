package cera

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/timzifer/cera/internal/pdf"
)

// Scripts of form fields, without JavaScript. A field formats, validates
// and calculates with scripts (/AA: /F, /K, /V, /C), and cera runs no
// JavaScript. But nearly every such script is one call of a function that
// Acrobat's form editor writes, with literal arguments:
//
//	AFNumber_Format(2, 0, 0, 0, "$", true);
//	AFRange_Validate(true, 0, true, 100);
//	AFSimple_Calculate("SUM", new Array("price", "tax"));
//
// Such a call is recognised by parsing it — a name and a list of literals,
// no interpreter — and what it does is done here. A field whose scripts
// are all of that kind is Computed; any other script leaves the field as
// it was, with HasActions.
//
// The formats follow Acrobat's: separators and negative styles as its
// form editor offers them, month and day names in English, numbers
// rounded half away from zero.

// fieldScripts is what the scripts of a field do.
type fieldScripts struct {
	format *formatScript // how the value is shown; nil: as it is
	rng    *rangeScript  // what the value may be; nil: anything
	calc   *calcScript   // how the value follows from others; nil: it does not
}

// formatScript is a format: a kind and the arguments of its function.
type formatScript struct {
	kind     byte // 'n' number, 'p' percent, 'd' date, 't' time, 's' special
	dec      int  // decimals
	sep      int  // separator style: 0 1,234.5  1 1234.5  2 1.234,5  3 1234,5  4 1'234.5
	neg      int  // negative style: 0 -1  1 red  2 (1)  3 red (1)
	currency string
	prepend  bool   // the currency, or the percent sign, before the number
	pattern  string // of a date or time
	special  int    // 0 zip code, 1 zip + 4, 2 phone number, 3 social security number
}

// rangeScript bounds a number (AFRange_Validate).
type rangeScript struct {
	min, max       float64
	hasMin, hasMax bool
}

// calcScript calculates a number from fields (AFSimple_Calculate).
type calcScript struct {
	fn    string // SUM, AVG, PRD, MIN, MAX
	names []string
}

// parseCall parses a script that is one call of a function with literal
// arguments: numbers, strings, true and false, and arrays of them, written
// [..] or new Array(..). It reports false for any other script.
func parseCall(js string) (name string, args []any, ok bool) {
	p := &callParser{s: js}
	p.space()
	start := p.i
	for p.i < len(p.s) && (p.s[p.i] == '_' || p.s[p.i] == '.' || unicode.IsLetter(rune(p.s[p.i])) || unicode.IsDigit(rune(p.s[p.i]))) {
		p.i++
	}
	name = p.s[start:p.i]
	if name == "" || !p.eat('(') {
		return "", nil, false
	}
	args, ok = p.list(')')
	if !ok {
		return "", nil, false
	}
	p.space()
	for p.eat(';') {
	}
	return name, args, p.i == len(p.s)
}

type callParser struct {
	s string
	i int
}

func (p *callParser) space() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *callParser) eat(c byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

// list parses literals separated by commas up to the closing character.
func (p *callParser) list(end byte) ([]any, bool) {
	var out []any
	if p.eat(end) {
		return out, true
	}
	for len(out) < 1<<12 {
		v, ok := p.value()
		if !ok {
			return nil, false
		}
		out = append(out, v)
		if p.eat(end) {
			return out, true
		}
		if !p.eat(',') {
			return nil, false
		}
	}
	return nil, false
}

func (p *callParser) value() (any, bool) {
	p.space()
	if p.i >= len(p.s) {
		return nil, false
	}
	rest := p.s[p.i:]
	switch c := p.s[p.i]; {
	case c == '"' || c == '\'':
		return p.str(c)
	case c == '[':
		p.i++
		return p.list(']')
	case strings.HasPrefix(rest, "new Array"):
		p.i += len("new Array")
		if !p.eat('(') {
			return nil, false
		}
		return p.list(')')
	case strings.HasPrefix(rest, "true"):
		p.i += 4
		return true, true
	case strings.HasPrefix(rest, "false"):
		p.i += 5
		return false, true
	default:
		start := p.i
		for p.i < len(p.s) && strings.IndexByte("+-.0123456789eE", p.s[p.i]) >= 0 {
			p.i++
		}
		v, err := strconv.ParseFloat(p.s[start:p.i], 64)
		return v, err == nil
	}
}

func (p *callParser) str(quote byte) (any, bool) {
	var b strings.Builder
	for p.i++; p.i < len(p.s); p.i++ {
		switch c := p.s[p.i]; {
		case c == quote:
			p.i++
			return b.String(), true
		case c == '\\' && p.i+1 < len(p.s):
			p.i++
			switch e := p.s[p.i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'u':
				if p.i+4 < len(p.s) {
					if v, err := strconv.ParseUint(p.s[p.i+1:p.i+5], 16, 16); err == nil {
						b.WriteRune(rune(v))
						p.i += 4
						continue
					}
				}
				return nil, false
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return nil, false
}

// readScripts reads the scripts of a field from its /AA and reports
// whether it has any, and whether cera does what all of them do.
func (d *Document) readScripts(aa pdf.Dict) (s fieldScripts, any, all bool) {
	all = true
	for key, action := range aa.All() {
		act := d.dict(action)
		if act.IsZero() {
			continue
		}
		any = true
		if !strings.Contains("F K V C", string(key)) || len(key) != 1 {
			all = false // a mouse or focus action
			continue
		}
		kind, _ := d.name(act.Get("S"))
		var js string
		switch o := d.resolve(act.Get("JS")); o.Kind() {
		case pdf.KindString:
			js = textString(o)
		case pdf.KindStream:
			if st, ok := o.Stream(); ok {
				js = decodeTextString(d.r.Decode(st).Data)
			}
		}
		name, args, ok := parseCall(js)
		if kind != "JavaScript" || !ok || !act.Get("Next").IsNull() || !s.set(string(key), name, args) {
			all = false
		}
	}
	return s, any, all
}

// set takes the call of the action key (F, K, V or C) and reports whether
// it is one cera does.
func (s *fieldScripts) set(key, name string, args []any) bool {
	num := func(i int) (float64, bool) {
		if i >= len(args) {
			return 0, false
		}
		v, ok := args[i].(float64)
		return v, ok
	}
	integer := func(i int) (int, bool) {
		v, ok := num(i)
		return int(v), ok && v == math.Trunc(v) && math.Abs(v) < 1e6
	}
	str := func(i int) (string, bool) {
		if i >= len(args) {
			return "", false
		}
		v, ok := args[i].(string)
		return v, ok
	}
	boolean := func(i int) bool {
		if i >= len(args) {
			return false
		}
		switch v := args[i].(type) {
		case bool:
			return v
		case float64:
			return v != 0
		}
		return false
	}
	switch key + " " + name {
	case "K AFNumber_Keystroke", "K AFPercent_Keystroke", "K AFDate_KeystrokeEx", "K AFDate_Keystroke",
		"K AFTime_Keystroke", "K AFSpecial_Keystroke", "K AFSpecial_KeystrokeEx":
		return true // what may be typed: the toolkit's
	case "F AFNumber_Format", "F AFPercent_Format":
		dec, ok1 := integer(0)
		sep, ok2 := integer(1)
		if !ok1 || !ok2 || dec < 0 || dec > 20 || sep < 0 || sep > 4 {
			return false
		}
		f := &formatScript{kind: 'n', dec: dec, sep: sep}
		if name == "AFPercent_Format" {
			f.kind, f.prepend = 'p', boolean(2)
		} else {
			f.neg, _ = integer(2)
			f.currency, _ = str(4)
			f.prepend = boolean(5)
			if f.neg < 0 || f.neg > 3 {
				return false
			}
		}
		s.format = f
		return true
	case "F AFDate_FormatEx", "F AFTime_FormatEx":
		pattern, ok := str(0)
		s.format = &formatScript{kind: 'd', pattern: pattern}
		return ok && pattern != ""
	case "F AFDate_Format":
		i, ok := integer(0)
		if !ok || i < 0 || i >= len(datePatterns) {
			return false
		}
		s.format = &formatScript{kind: 'd', pattern: datePatterns[i]}
		return true
	case "F AFTime_Format":
		i, ok := integer(0)
		if !ok || i < 0 || i >= len(timePatterns) {
			return false
		}
		s.format = &formatScript{kind: 't', pattern: timePatterns[i]}
		return true
	case "F AFSpecial_Format":
		i, ok := integer(0)
		s.format = &formatScript{kind: 's', special: i}
		return ok && i >= 0 && i <= 3
	case "V AFRange_Validate":
		r := &rangeScript{hasMin: boolean(0), hasMax: boolean(2)}
		var ok1, ok2 bool
		r.min, ok1 = num(1)
		r.max, ok2 = num(3)
		s.rng = r
		return len(args) == 4 && (ok1 || !r.hasMin) && (ok2 || !r.hasMax)
	case "C AFSimple_Calculate":
		fn, ok := str(0)
		c := &calcScript{fn: fn}
		if len(args) != 2 || !ok || !strings.Contains("SUM AVG PRD MIN MAX", fn) || len(fn) != 3 {
			return false
		}
		switch v := args[1].(type) {
		case string: // names separated by commas
			for _, n := range strings.Split(v, ",") {
				c.names = append(c.names, strings.TrimSpace(n))
			}
		case []any:
			for _, n := range v {
				name, ok := n.(string)
				if !ok {
					return false
				}
				c.names = append(c.names, name)
			}
		default:
			return false
		}
		s.calc = c
		return true
	}
	return false
}

// The formats AFDate_Format and AFTime_Format name by number.
var (
	datePatterns = []string{"m/d", "m/d/yy", "mm/dd/yy", "mm/yy", "d-mmm", "d-mmm-yy", "dd-mmm-yy", "yy-mm-dd",
		"mmm-yy", "mmmm-yy", "mmm d, yyyy", "mmmm d, yyyy", "m/d/yy h:MM tt", "m/d/yy HH:MM"}
	timePatterns = []string{"HH:MM", "h:MM tt", "HH:MM:ss", "h:MM:ss tt"}
)

// number reads a number as a user types it into a form: with a point or a
// comma before the decimals. It reports false for anything else, also for
// nothing.
func formNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
		s = strings.Replace(s, ",", ".", 1)
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// text returns what a field shows for the value s, and whether in red. A
// value the format does not understand is shown as it is.
func (f *formatScript) text(s string) (string, bool) {
	switch f.kind {
	case 'n', 'p':
		v, ok := formNumber(s)
		if !ok {
			return s, false
		}
		if f.kind == 'p' {
			n := groupNumber(math.Abs(v*100), f.dec, f.sep)
			if v < 0 {
				n = "-" + n
			}
			if f.prepend {
				return "%" + n, false
			}
			return n + "%", false
		}
		n := groupNumber(math.Abs(v), f.dec, f.sep)
		negative := v < 0 && strings.Trim(n, "0.,'") != "" // -0.001 with two decimals is 0.00
		if f.prepend {
			n = f.currency + n
		} else {
			n += f.currency
		}
		switch {
		case negative && f.neg == 0:
			n = "-" + n
		case negative && f.neg >= 2:
			n = "(" + n + ")"
		}
		return n, negative && (f.neg == 1 || f.neg == 3)
	case 'd', 't':
		t, ok := parseFormDate(s)
		if !ok {
			return s, false
		}
		return formatDate(t, f.pattern), false
	case 's':
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, s)
		mask := map[int]string{0: "99999", 1: "99999-9999", 2: "(999) 999-9999", 3: "999-99-9999"}[f.special]
		if f.special == 2 && len(digits) == 7 {
			mask = "999-9999"
		}
		if len(digits) != strings.Count(mask, "9") {
			return s, false
		}
		var b strings.Builder
		for i, k := 0, 0; i < len(mask); i++ {
			if mask[i] == '9' {
				b.WriteByte(digits[k])
				k++
			} else {
				b.WriteByte(mask[i])
			}
		}
		return b.String(), false
	}
	return s, false
}

// groupNumber writes v, not negative, with dec decimals in the separator
// style sep, rounded half away from zero.
func groupNumber(v float64, dec, sep int) string {
	p := math.Pow(10, float64(dec))
	v = math.Floor(v*p+0.5+1e-9) / p
	s := strconv.FormatFloat(v, 'f', dec, 64)
	whole, frac, _ := strings.Cut(s, ".")
	group, point := [...]string{",", "", ".", "", "'"}[sep], [...]string{".", ".", ",", ",", "."}[sep]
	if group != "" {
		var b strings.Builder
		for i, c := range whole {
			if i > 0 && (len(whole)-i)%3 == 0 {
				b.WriteString(group)
			}
			b.WriteRune(c)
		}
		whole = b.String()
	}
	if dec == 0 {
		return whole
	}
	return whole + point + frac
}

var (
	monthNames = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	dayNames   = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
)

// formatDate writes t in a pattern of Acrobat's: d dd ddd dddd, m mm mmm
// mmmm, yy yyyy, H HH (24 hours), h hh (12 hours), M MM, s ss, t tt.
func formatDate(t time.Time, pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); {
		c := pattern[i]
		n := 1
		for i+n < len(pattern) && pattern[i+n] == c {
			n++
		}
		two := func(v int) {
			if n >= 2 {
				fmt.Fprintf(&b, "%02d", v)
			} else {
				b.WriteString(strconv.Itoa(v))
			}
		}
		h12 := (t.Hour()+11)%12 + 1
		switch c {
		case 'd':
			switch {
			case n >= 4:
				b.WriteString(dayNames[t.Weekday()])
			case n == 3:
				b.WriteString(dayNames[t.Weekday()][:3])
			default:
				two(t.Day())
			}
		case 'm':
			switch {
			case n >= 4:
				b.WriteString(monthNames[t.Month()-1])
			case n == 3:
				b.WriteString(monthNames[t.Month()-1][:3])
			default:
				two(int(t.Month()))
			}
		case 'y':
			if n >= 4 {
				fmt.Fprintf(&b, "%04d", t.Year())
			} else {
				fmt.Fprintf(&b, "%02d", t.Year()%100)
			}
		case 'H':
			two(t.Hour())
		case 'h':
			two(h12)
		case 'M':
			two(t.Minute())
		case 's':
			two(t.Second())
		case 't':
			ampm := "am"
			if t.Hour() >= 12 {
				ampm = "pm"
			}
			b.WriteString(ampm[:min(n, 2)])
		case '\\':
			if i+1 < len(pattern) {
				b.WriteByte(pattern[i+1])
				n = 2
			}
		default:
			b.WriteString(pattern[i : i+n])
		}
		i += n
	}
	return b.String()
}

// parseFormDate reads a date, a time or both as a user types them: the
// numbers of a date in the order year-month-day when the first has four
// digits, day.month.year with points, else month/day/year as Acrobat's
// formats have it; a month may be a name; a time is hours:minutes with
// seconds and am or pm if they are there. What is not said is today's
// date, and midnight.
func parseFormDate(s string) (time.Time, bool) {
	now := time.Now()
	year, month, day := now.Year(), int(now.Month()), now.Day()
	hour, minute, second := 0, 0, 0
	var nums []string
	seps := ""
	pm, am := false, false
	fields := strings.FieldsFunc(s, func(r rune) bool {
		if strings.ContainsRune("/-., ", r) {
			seps += string(r)
			return true
		}
		return false
	})
	for _, f := range fields {
		lf := strings.ToLower(f)
		switch {
		case strings.Contains(f, ":"):
			parts := strings.Split(f, ":")
			if len(parts) < 2 || len(parts) > 3 {
				return time.Time{}, false
			}
			vals := []*int{&hour, &minute, &second}
			for i, p := range parts {
				p = strings.TrimRight(strings.ToLower(p), "apm")
				v, err := strconv.Atoi(p)
				if err != nil {
					return time.Time{}, false
				}
				*vals[i] = v
			}
			pm = pm || strings.HasSuffix(lf, "pm") || strings.HasSuffix(lf, "p")
			am = am || strings.HasSuffix(lf, "am") || strings.HasSuffix(lf, "a")
		case lf == "pm" || lf == "p":
			pm = true
		case lf == "am" || lf == "a":
			am = true
		case unicode.IsLetter(rune(f[0])):
			found := false
			for i, name := range monthNames {
				if len(lf) >= 3 && strings.HasPrefix(strings.ToLower(name), lf) {
					month, found = i+1, true
				}
			}
			if !found {
				return time.Time{}, false
			}
			nums = append(nums, "month")
		default:
			if _, err := strconv.Atoi(f); err != nil {
				return time.Time{}, false
			}
			nums = append(nums, f)
		}
	}
	// The numbers of the date, the month taken out when it was a name.
	var date []int
	named := false
	first4 := false
	for i, n := range nums {
		if n == "month" {
			named = true
			continue
		}
		v, _ := strconv.Atoi(n)
		if i == 0 && len(n) == 4 {
			first4 = true
		}
		date = append(date, v)
	}
	yy := func(v int) int {
		if v < 100 {
			if v < 50 {
				return 2000 + v
			}
			return 1900 + v
		}
		return v
	}
	switch {
	case named && len(date) == 1 && date[0] > 31:
		year = yy(date[0])
		day = 1
	case named && len(date) == 1:
		day = date[0]
	case named && len(date) == 2 && first4:
		year, day = date[0], date[1]
	case named && len(date) == 2:
		day, year = date[0], yy(date[1])
	case !named && len(date) == 3 && first4:
		year, month, day = date[0], date[1], date[2]
	case !named && len(date) == 3 && strings.HasPrefix(seps, ".."):
		day, month, year = date[0], date[1], yy(date[2])
	case !named && len(date) == 3:
		month, day, year = date[0], date[1], yy(date[2])
	case !named && len(date) == 2 && strings.HasPrefix(seps, "."):
		day, month = date[0], date[1]
	case !named && len(date) == 2:
		month, day = date[0], date[1]
	case len(date) == 0 && (named || len(fields) > 0 && strings.Contains(s, ":")):
	default:
		return time.Time{}, false
	}
	if pm && hour < 12 {
		hour += 12
	}
	if am && hour == 12 {
		hour = 0
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	return t, t.Day() == day
}

// Format returns the text the field shows for v: a text formatted as the
// format of the field says — a number with separators and a currency, a
// date, a phone number — where the field has a format cera does
// ([Field.Computed]) and the text is of its kind; else the text of v. The
// value stays what it is: the format is how it is shown, in the
// appearances cera generates and writes, and in a toolkit's widget while
// it is not being edited.
func (f *Field) Format(v Value) string {
	if f.Type == FieldText && f.scripts.format != nil {
		s, _ := f.scripts.format.text(v.text)
		return s
	}
	return v.text
}

// valid checks a value against the range the field validates with.
func (f *Field) valid(v Value) error {
	r := f.scripts.rng
	if r == nil || f.Type != FieldText {
		return nil
	}
	n, ok := formNumber(v.text)
	if !ok {
		return nil // nothing, or no number: not this script's to refuse
	}
	switch {
	case r.hasMin && r.hasMax && (n < r.min || n > r.max):
		return fmt.Errorf("%w: %s: %s is not between %s and %s", ErrInvalidValue, f.Name, v.text, formatFloat(r.min), formatFloat(r.max))
	case r.hasMin && n < r.min:
		return fmt.Errorf("%w: %s: %s is less than %s", ErrInvalidValue, f.Name, v.text, formatFloat(r.min))
	case r.hasMax && n > r.max:
		return fmt.Errorf("%w: %s: %s is greater than %s", ErrInvalidValue, f.Name, v.text, formatFloat(r.max))
	}
	return nil
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
}

// calculate returns the value the calculation of f gives from the values
// value returns, and false for a field that does not calculate.
func (f *Field) calculate(value func(*Field) Value) (Value, bool) {
	c := f.scripts.calc
	if c == nil || f.Type != FieldText {
		return Value{}, false
	}
	var vals []float64
	for _, name := range c.names {
		for _, g := range f.form.named(name) {
			v := value(g)
			text := v.text
			if v.kind == ValueChoice && len(v.sel) > 0 && v.sel[0] < len(g.Options) {
				text = g.Options[v.sel[0]].Export
			}
			n, _ := formNumber(text) // nothing and no number count as 0
			vals = append(vals, n)
		}
	}
	res := 0.0
	if len(vals) > 0 {
		res = vals[0]
		for _, v := range vals[1:] {
			switch c.fn {
			case "SUM", "AVG":
				res += v
			case "PRD":
				res *= v
			case "MIN":
				res = min(res, v)
			case "MAX":
				res = max(res, v)
			}
		}
		if c.fn == "AVG" {
			res /= float64(len(vals))
		}
	}
	return TextValue(formatFloat(res)), true
}

// named returns the fields a calculation names: the field of that name,
// or the fields below a name that is not a field's own, "order" for
// "order.price" and "order.tax".
func (f *Form) named(name string) []*Field {
	if g := f.byName[name]; g != nil {
		return []*Field{g}
	}
	var out []*Field
	for _, g := range f.Fields {
		if strings.HasPrefix(g.Name, name+".") {
			out = append(out, g)
		}
	}
	return out
}
