package httpx

import "testing"

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in   string
		want ContentRange
		ok   bool
	}{
		{"bytes 0-255/1000", ContentRange{0, 255, 1000}, true},
		{"bytes 100-199/*", ContentRange{100, 199, -1}, true},
		{"bytes 0-99/100", ContentRange{0, 99, 100}, true},
		{"bytes 0-100/100", ContentRange{}, false},
		{"bytes 10-5/100", ContentRange{}, false},
		{"bytes */100", ContentRange{}, false},
		{"items 0-1/2", ContentRange{}, false},
	}
	for _, c := range cases {
		got, ok := ParseContentRange(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseContentRange(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseContentDisposition(t *testing.T) {
	cases := map[string]string{
		`attachment; filename="a b.zip"`:                              "a b.zip",
		`attachment; filename*=UTF-8''%E4%B8%AD%E6%96%87.txt`:         "中文.txt",
		`attachment; filename="fallback.txt"; filename*=UTF-8''x.txt`: "x.txt",
		`attachment; filename=my file.iso`:                            "my file.iso",
		`inline`:                                                      "",
		``:                                                            "",
	}
	for in, want := range cases {
		if got := ParseContentDisposition(in); got != want {
			t.Errorf("ParseContentDisposition(%q) = %q; want %q", in, got, want)
		}
	}
}
