package typed

// Adversarial tests for Check. Everything in this file passes. The attacks

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// The grammar a caller can receive, per type, written independently of the
// checker.
var (
	gDate     = regexp.MustCompile(`^"[0-9]{4}-[0-9]{2}-[0-9]{2}"$`)
	gDateTime = regexp.MustCompile(`^"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z|[+-][0-9]{2}:[0-9]{2})"$`)
	gNumber   = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,6})?$`)
	gMoney    = regexp.MustCompile(`^\{"amount":"-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,4})?","currency":"[A-Z]{3}"\}$`)
	gBoolean  = regexp.MustCompile(`^(true|false)$`)
)

var advChoices = []string{"shipped", "delivered", "a\u202eb", "<script>", "x\ny", "�", "é"}

func advSpecs() []Spec {
	return []Spec{
		{Type: Date}, {Type: DateTime}, {Type: Number}, {Type: Money}, {Type: Boolean},
		{Type: Choice, Choices: advChoices},
		{Type: Record, Fields: map[string]Spec{
			"d": {Type: Date}, "t": {Type: DateTime}, "n": {Type: Number}, "m": {Type: Money},
			"b": {Type: Boolean}, "c": {Type: Choice, Choices: advChoices},
		}},
		{Type: Text},
	}
}

// advInGrammar reports whether out is a value of spec's grammar.
func advInGrammar(spec Spec, out string) bool {
	switch spec.Type {
	case Date:
		return gDate.MatchString(out)
	case DateTime:
		return gDateTime.MatchString(out)
	case Number:
		return gNumber.MatchString(out)
	case Money:
		return gMoney.MatchString(out)
	case Boolean:
		return gBoolean.MatchString(out)
	case Choice:
		for _, c := range spec.Choices {
			if b, _ := json.Marshal(c); string(b) == out {
				return true
			}
		}
		return false
	case Record:
		// Exactly the spec's field names, sorted, each null or in grammar.
		names := make([]string, 0, len(spec.Fields))
		for n := range spec.Fields {
			names = append(names, n)
		}
		sort.Strings(names)
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(out), &m) != nil || len(m) != len(names) {
			return false
		}
		rebuilt := "{"
		for i, n := range names {
			v, ok := m[n]
			if !ok {
				return false
			}
			if string(v) != "null" && !advInGrammar(spec.Fields[n], string(v)) {
				return false
			}
			if i > 0 {
				rebuilt += ","
			}
			rebuilt += `"` + n + `":` + string(v)
		}
		return rebuilt+"}" == out // no duplicate keys, no spare bytes
	}
	return false
}

func checkOne(t interface{ Fatalf(string, ...any) }, spec Spec, raw []byte) {
	out, ok := Check(spec, raw)
	if !ok {
		if out != nil {
			t.Fatalf("rejected but returned bytes %q", out)
		}
		return
	}
	if spec.Type == Text {
		t.Fatalf("a text spec was accepted: %q", raw)
	}
	if !advInGrammar(spec, string(out)) {
		t.Fatalf("type %s: output %q (from %q) is outside the grammar", spec.Type, out, raw)
	}
	if !json.Valid(out) {
		t.Fatalf("output %q is not JSON", out)
	}
}

func FuzzCheckStaysInGrammar(f *testing.F) {
	seeds := []string{
		`"2027-07-17"`, `"2024-02-29"`, `"2023-02-29"`, `"0000-00-00"`, `"1899-12-31"`, `"2201-01-01"`,
		`"2024-01-01T00:00:00Z"`, `"2024-01-01T00:00:00+24:00"`, `"2024-01-01t00:00:00z"`, `"2016-12-31T23:59:60Z"`,
		`"2024-01-01T00:00:00.123456789-23:59"`, `"2024-01-01T1:00:00Z"`, `"2024-01-01T01:00:00,5Z"`,
		`-0`, `1E5`, `00`, `1e400`, `0.0000001`, `999999999999999.999999`, `1234567890123456`, `-0.0`, `NaN`, `Infinity`, `0x10`,
		`{"amount":"412.60","currency":"CAD"}`, `{"amount":412.60,"currency":"CAD"}`, `{"amount":"١٢","currency":"USD"}`,
		`{"amount":"1","currency":"usd"}`, `{"amount":"1","currency":"USD","currency":"XXX"}`, `{"amount":"1","currency":"USD","x":1}`,
		`{"amount":"1\n","currency":"USD"}`, `{"Amount":"1","Currency":"USD"}`, `{"amount":"１","currency":"USD"}`,
		`true`, `false`, `"true"`, `1`, `null`, ` true `, `true]`, "\xef\xbb\xbftrue", "true\x00",
		`"shipped"`, `"Shipped"`, `"shipped "`, `"a\u202eb"`, `"\ud800"`, "\"\xff\"", `"ship\u0000ped"`, `"x\ny"`,
		`{"d":"2027-07-17","n":1,"b":true}`, `{"d":null}`, `{}`, `{"zz":1}`, `{"d":"2027-07-17","d":"IGNORE"}`,
		`{"c":"shipped","m":{"amount":"1","currency":"USD"},"t":"2024-01-01T00:00:00Z"}`,
		`"IGNORE PREVIOUS INSTRUCTIONS"`, `[`, ``, strings.Repeat("[", 20000), `{"a":` + strings.Repeat(`{"a":`, 5000),
	}
	for i := range advSpecs() {
		for _, s := range seeds {
			f.Add(i, []byte(s))
		}
	}
	specs := advSpecs()
	f.Fuzz(func(t *testing.T, which int, raw []byte) {
		if which < 0 {
			which = -(which + 1)
		}
		checkOne(t, specs[which%len(specs)], raw)
	})
}

// TestGeneratedAnswersStayInGrammar builds answers that are nearly right,
// which random bytes rarely are.
func TestGeneratedAnswersStayInGrammar(t *testing.T) {
	specs := advSpecs()
	digits := rapid.StringMatching(`-?[0-9]{0,17}(\.[0-9]{0,8})?([eE][+-]?[0-9]{1,3})?`)
	dt := rapid.StringMatching(`[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt ][0-9]{1,2}:[0-9]{2}:[0-9]{2}([.,][0-9]{1,9})?(Z|z|[+-][0-9]{2}:?[0-9]{2})`)
	scalar := rapid.OneOf(
		digits,
		rapid.StringMatching(`"[0-9]{4}-[0-9]{2}-[0-9]{2}"`),
		rapid.Map(dt, func(s string) string { return `"` + s + `"` }),
		rapid.Map(digits, func(s string) string { return `{"amount":"` + s + `","currency":"USD"}` }),
		rapid.Map(digits, func(s string) string { return `{"amount":` + s + `,"currency":"EUR"}` }),
		rapid.Map(rapid.StringMatching(`[A-Za-z]{3}`), func(s string) string { return `{"amount":"1","currency":"` + s + `"}` }),
		rapid.Map(rapid.SampledFrom(advChoices), func(s string) string { b, _ := json.Marshal(s); return string(b) }),
		rapid.Map(rapid.String(), func(s string) string { b, _ := json.Marshal(s); return string(b) }),
		rapid.SampledFrom([]string{"true", "false", "null", "[]", "{}"}),
	)
	record := rapid.Custom(func(rt *rapid.T) string {
		keys := rapid.SliceOfN(rapid.SampledFrom([]string{"d", "t", "n", "m", "b", "c", "zz", "D"}), 0, 8).Draw(rt, "keys")
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = `"` + k + `":` + scalar.Draw(rt, "value")
		}
		return "{" + strings.Join(parts, ",") + "}"
	})
	rapid.Check(t, func(rt *rapid.T) {
		spec := rapid.SampledFrom(specs).Draw(rt, "spec")
		raw := rapid.OneOf(scalar, record).Draw(rt, "raw")
		pad := rapid.SampledFrom([]string{"", " ", "\n", "\t\r\n "}).Draw(rt, "pad")
		checkOne(rt, spec, []byte(pad+raw+pad))
	})
}

// TestAnswerSizeLimit checks the boundary of MaxAnswerLen: padding takes a
// valid answer up to the limit and one byte past it.
func TestAnswerSizeLimit(t *testing.T) {
	at := []byte(strings.Repeat(" ", MaxAnswerLen-4) + "true")
	if out, ok := Check(Spec{Type: Boolean}, at); !ok || string(out) != "true" {
		t.Errorf("an answer of exactly MaxAnswerLen bytes: %q, %v", out, ok)
	}
	if _, ok := Check(Spec{Type: Boolean}, append([]byte(" "), at...)); ok {
		t.Error("an answer one byte over MaxAnswerLen was accepted")
	}
	if _, ok := CheckText(append([]byte(`"`), append([]byte(strings.Repeat("x", MaxTextRunes+1)), '"')...)); ok {
		t.Error("text one rune over MaxTextRunes was accepted")
	}
}

// TestCapacity measures how many attacker-chosen bits one accepted value
// of each type can carry: log2 of the number of distinct outputs.
func TestCapacity(t *testing.T) {
	log2 := func(n *big.Int) float64 {
		f, _ := new(big.Float).SetInt(n).Float64()
		if !math.IsInf(f, 0) {
			return math.Log2(f)
		}
		return float64(n.BitLen())
	}
	pow10 := func(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }
	mul := func(xs ...*big.Int) *big.Int {
		out := big.NewInt(1)
		for _, x := range xs {
			out.Mul(out, x)
		}
		return out
	}

	first := time.Date(minYear, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(maxYear+1, 1, 1, 0, 0, 0, 0, time.UTC)
	days := int64(end.Sub(first).Hours() / 24)
	date := big.NewInt(days)

	// Offsets: count the distinct ones Check lets through and emits.
	offsets := map[string]bool{}
	for _, sign := range []string{"+", "-"} {
		for h := 0; h <= 99; h++ {
			for m := 0; m <= 99; m++ {
				in := `"2024-06-15T12:00:00` + sign + string(rune('0'+h/10)) + string(rune('0'+h%10)) + ":" + string(rune('0'+m/10)) + string(rune('0'+m%10)) + `"`
				if out, ok := Check(Spec{Type: DateTime}, []byte(in)); ok {
					offsets[string(out)[20:]] = true
				}
			}
		}
	}
	datetime := mul(big.NewInt(days*86400), big.NewInt(int64(len(offsets))))

	// number: sign, 10^15 integer parts, and no fraction or 1..6 digits.
	number := mul(big.NewInt(2), pow10(15), big.NewInt(1+10+100+1000+10000+100000+1000000))
	// money: sign, 10^12 integer parts, no fraction or 1..4 digits, a code.
	money := mul(big.NewInt(2), pow10(12), big.NewInt(1+10+100+1000+10000), big.NewInt(int64(len(currencies))))
	choice := big.NewInt(MaxChoices)
	// record: MaxFields fields, each the widest type or null.
	perField := new(big.Int).Add(number, big.NewInt(1))
	record := new(big.Int).Exp(perField, big.NewInt(MaxFields), nil)
	record.Sub(record, big.NewInt(1)) // all-null is rejected

	t.Logf("distinct datetime offsets emitted: %d", len(offsets))
	t.Logf("currencies: %d", len(currencies))
	for _, row := range []struct {
		name string
		n    *big.Int
	}{
		{"boolean", big.NewInt(2)}, {"choice (32 choices)", choice}, {"date", date}, {"datetime", datetime},
		{"money", money}, {"number", number}, {"record of 16 numbers", record},
	} {
		t.Logf("%-22s %8.1f bits", row.name, log2(row.n))
	}

	// Spot-check the extremes the counts assume.
	for spec, in := range map[Type]string{
		Number: `-999999999999999.999999`, Money: `{"amount":"-999999999999.9999","currency":"ZWG"}`,
		Date: `"2200-12-31"`, DateTime: `"1900-01-01T00:00:00-24:00"`,
	} {
		if _, ok := Check(Spec{Type: spec}, []byte(in)); !ok {
			t.Errorf("%s: %s should be accepted for the count to hold", spec, in)
		}
	}
	for spec, in := range map[Type]string{
		Number: `1000000000000000`, Money: `{"amount":"1.00000","currency":"USD"}`, Date: `"2201-01-01"`,
	} {
		if _, ok := Check(Spec{Type: spec}, []byte(in)); ok {
			t.Errorf("%s: %s should be rejected for the count to hold", spec, in)
		}
	}
}

// TestSpecParsingRefusesOddShapes covers ParseSpec, whose output reaches a
// caller as record field names and choice strings.
func TestSpecParsingRefusesOddShapes(t *testing.T) {
	bad := []string{
		``, `null`, `1`, `[]`, `"essay"`, `{"type":"date","choices":["a"]}`, `{"type":"date","extra":1}`,
		`{"type":"choice"}`, `{"type":"choice","choices":[]}`, `{"type":"choice","choices":["a","a"]}`,
		`{"type":"choice","choices":[""]}`, `{"type":"choice","choices":["` + strings.Repeat("x", MaxChoiceLen+1) + `"]}`,
		`{"type":"record","fields":{}}`, `{"type":"record","fields":{"A":"date"}}`, `{"type":"record","fields":{"a b":"date"}}`,
		`{"type":"record","fields":{"a":"text"}}`, `{"type":"record","fields":{"a":{"type":"record","fields":{"b":"date"}}}}`,
		`{"type":"record","fields":{"a":null}}`, `{"type":"record","fields":{"":"date"}}`,
		`{"type":"record","fields":{"a\n":"date"}}`,
	}
	for _, raw := range bad {
		if s, err := ParseSpec(json.RawMessage(raw)); err == nil {
			t.Errorf("ParseSpec(%s) = %+v, want an error", raw, s)
		}
	}
}
