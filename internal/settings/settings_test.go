package settings

import (
	"strconv"
	"strings"
	"testing"
)

func form(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		FieldPublicBaseURL: "https://shopping.example.com/",
		FieldTimezone:      "America/Denver",
		FieldUnits:         UnitsUS,
		FieldWeekStart:     "1",
		FieldSessionHours:  "48",
		FieldShareDays:     "90",
	}
}

func TestParseValid(t *testing.T) {
	s, errs := Parse(form(valid()))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := Settings{
		PublicBaseURL: "https://shopping.example.com", Timezone: "America/Denver",
		Units: UnitsUS, WeekStart: 1, SessionHours: 48, ShareDefaultDays: 90,
	}
	if s != want {
		t.Errorf("got %+v, want %+v", s, want)
	}
	if got := FromMap(s.Map()); got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ field, value string }{
		"http address":       {FieldPublicBaseURL, "http://shopping.example.com"},
		"address with path":  {FieldPublicBaseURL, "https://example.com/app"},
		"address with login": {FieldPublicBaseURL, "https://user:pw@example.com"},
		"not an address":     {FieldPublicBaseURL, "shopping"},
		"unknown timezone":   {FieldTimezone, "Mars/Olympus"},
		"empty timezone":     {FieldTimezone, ""},
		"bad units":          {FieldUnits, "stone"},
		"week day 7":         {FieldWeekStart, "7"},
		"week day text":      {FieldWeekStart, "monday"},
		"zero hours":         {FieldSessionHours, "0"},
		"too many hours":     {FieldSessionHours, strconv.Itoa(MaxSessionHours + 1)},
		"zero share days":    {FieldShareDays, "0"},
		"share over a year":  {FieldShareDays, "366"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			m[c.field] = c.value
			if _, errs := Parse(form(m)); errs[c.field] == "" {
				t.Errorf("%s = %q accepted", c.field, c.value)
			}
		})
	}
}

func TestShareLimitIsOneYear(t *testing.T) {
	if MaxShareExpiryDays != 365 {
		t.Fatalf("MaxShareExpiryDays = %d, the rule is 365", MaxShareExpiryDays)
	}
	m := valid()
	m[FieldShareDays] = "365"
	if _, errs := Parse(form(m)); len(errs) != 0 {
		t.Errorf("365 days refused: %v", errs)
	}
}

func TestEmptyAddressAllowed(t *testing.T) {
	m := valid()
	m[FieldPublicBaseURL] = "  "
	s, errs := Parse(form(m))
	if len(errs) != 0 || s.PublicBaseURL != "" {
		t.Errorf("empty address: %+v %v", s, errs)
	}
}

func TestFromMapFallsBackToDefaults(t *testing.T) {
	got := FromMap(map[string]string{
		keyPublicBaseURL:        "http://insecure.example.com",
		keyTimezone:             "Not/AZone",
		keyUnits:                "stone",
		keyWeekStart:            "9",
		keySessionHours:         "-4",
		keyShareDefaultExpiryDy: strings.Repeat("9", 5),
	})
	if got != Defaults() {
		t.Errorf("got %+v, want defaults %+v", got, Defaults())
	}
	if FromMap(nil) != Defaults() {
		t.Error("nil map is not the defaults")
	}
}
