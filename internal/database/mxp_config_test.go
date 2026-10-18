package database

import (
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
)

// TestMxpMaxCrossPosts: the MaxCrossPosts config key is read through the config cache with
// the range 1..config.MaxCrossPostsLimit; anything else reads as config.DefaultMaxCrossPosts,
// and migration 0032 seeds the default row.
func TestMxpMaxCrossPosts(t *testing.T) {
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_MAXCROSSPOSTS)
	if err != nil {
		t.Fatal(err)
	}
	if old != "9" {
		t.Errorf("seeded %s = %q, want \"9\" (migration 0032)", config.CFG_KEY_MAXCROSSPOSTS, old)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_MAXCROSSPOSTS, old); err != nil {
			t.Errorf("restoring %s: %v", config.CFG_KEY_MAXCROSSPOSTS, err)
		}
	})
	for _, c := range []struct {
		v    string
		want int
	}{
		{"7", 7}, {" 3 ", 3}, {"1", 1}, {"15", 15}, {"16", 9}, {"0", 9}, {"-2", 9}, {"abc", 9}, {"", 9}, {"9", 9},
	} {
		if err := db.SetConfigValue(config.CFG_KEY_MAXCROSSPOSTS, c.v); err != nil {
			t.Fatal(err)
		}
		if got := db.MaxCrossPosts(); got != c.want {
			t.Errorf("MaxCrossPosts with %q = %d, want %d", c.v, got, c.want)
		}
	}
}
