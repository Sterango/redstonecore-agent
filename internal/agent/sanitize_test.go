package agent

import "testing"

func TestSanitizeServerDirName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "special chars (! and |) and spaces become a clean slug",
			in:   "Mastering Magic | A Magic World with Quests!",
			want: "mastering-magic-a-magic-world-with-quests",
		},
		{
			name: "version dots become dashes",
			in:   "Craftoria 1.30.0",
			want: "craftoria-1-30-0",
		},
		{
			name: "apostrophes dropped, no double dashes",
			in:   "Len's Server",
			want: "len-s-server",
		},
		{
			name: "leading/trailing junk trimmed",
			in:   "  !!Hello World!!  ",
			want: "hello-world",
		},
		{
			name: "empty / all-special falls back to 'server'",
			in:   "!!!",
			want: "server",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeServerDirName(tc.in); got != tc.want {
				t.Errorf("sanitizeServerDirName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
