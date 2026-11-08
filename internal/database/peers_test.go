package database

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// TestPeersCRUD: insert fills defaults, update keeps the passwords, the in-password is a bcrypt
// hash checked by AuthenticatePeer, a disabled peer cannot log in, delete removes the row.
func TestPeersCRUD(t *testing.T) {
	db := w0DB(t)
	name := w0Name("peer")
	p := &models.Peer{Name: name, PathToken: "news.example.org", Hosts: " 127.0.0.1, 10.0.0.0/8 news.example.org ",
		InUsername: name + "-login", OutPassword: "out-secret", Enabled: true}
	id, err := db.InsertPeer(p, "in-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPeerByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hosts != "127.0.0.1,10.0.0.0/8,news.example.org" || got.InPatterns != "*" || got.OutPatterns != "*,!control*,!junk" ||
		got.OutPort != 119 || got.MaxConns != 4 || got.OutPassword != "out-secret" || got.InPasswordHash == "in-secret" || got.InPasswordHash == "" {
		t.Fatalf("stored peer = %+v", got)
	}
	if _, err := db.AuthenticatePeer(name+"-login", "in-secret"); err != nil {
		t.Fatalf("right password: %v", err)
	}
	for _, c := range [][2]string{{name + "-login", "wrong"}, {"nobody-" + name, "in-secret"}, {"", ""}} {
		if _, err := db.AuthenticatePeer(c[0], c[1]); !errors.Is(err, ErrPeerAuth) {
			t.Errorf("AuthenticatePeer(%q, %q) = %v, want ErrPeerAuth", c[0], c[1], err)
		}
	}

	got.Notes = "edited"
	got.Enabled = false
	if err := db.UpdatePeer(got); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetPeerByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if again.Notes != "edited" || again.Enabled || again.InPasswordHash != got.InPasswordHash || again.OutPassword != "out-secret" {
		t.Fatalf("after update = %+v", again)
	}
	if _, err := db.AuthenticatePeer(name+"-login", "in-secret"); !errors.Is(err, ErrPeerAuth) {
		t.Errorf("disabled peer login = %v, want ErrPeerAuth", err)
	}
	if err := db.SetPeerInPassword(id, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPeerOutPassword(id, "new-out"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePeer(id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPeerByID(id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("after delete: %v, want sql.ErrNoRows", err)
	}
	if err := db.DeletePeer(id); err == nil {
		t.Error("second delete: want an error")
	}
}

func TestPeersValidate(t *testing.T) {
	for _, p := range []models.Peer{
		{Name: "", PathToken: "a"},
		{Name: "x", PathToken: ""},
		{Name: "x", PathToken: "a!b"},
		{Name: "x", PathToken: "a b"},
		{Name: "x", PathToken: "a", Hosts: "bad_host!"},
		{Name: "x", PathToken: "a", Hosts: "h", OutPort: 70000},
		{Name: "x", PathToken: "a", Hosts: "h", MaxConns: 65},
		{Name: "x", PathToken: "a", Hosts: "h", OutEnabled: true},
		{Name: "x", PathToken: "a"},
	} {
		if err := ValidatePeer(&p); err == nil {
			t.Errorf("ValidatePeer(%+v) = nil, want an error", p)
		}
	}
	ok := models.Peer{Name: "x", PathToken: "a", Hosts: "::1,fe80::/10,host-1.example.org.", OutEnabled: true, OutHost: "b"}
	if err := ValidatePeer(&ok); err != nil {
		t.Errorf("ValidatePeer(ok) = %v", err)
	}
	loginOnly := models.Peer{Name: "x", PathToken: "a", InUsername: "feeder"}
	if err := ValidatePeer(&loginOnly); err != nil {
		t.Errorf("ValidatePeer(login only) = %v", err)
	}
	if _, err := w0DB(t).InsertPeer(&models.Peer{Name: w0Name("pw"), PathToken: "a", Hosts: "127.0.0.1"}, "secret-password"); err == nil {
		t.Error("InsertPeer with a password and no login: want an error")
	}
}

func TestPeersHistoryRejectDays(t *testing.T) {
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_HISTORY_REJECT_DAYS)
	if err != nil {
		t.Fatal(err)
	}
	if old != "30" {
		t.Errorf("seeded %s = %q, want 30 (migration 0034)", config.CFG_KEY_HISTORY_REJECT_DAYS, old)
	}
	t.Cleanup(func() { _ = db.SetConfigValue(config.CFG_KEY_HISTORY_REJECT_DAYS, old) })
	for _, c := range []struct {
		v    string
		want int
	}{{"0", 0}, {" 7 ", 7}, {"-1", 30}, {"x", 30}, {"", 30}} {
		if err := db.SetConfigValue(config.CFG_KEY_HISTORY_REJECT_DAYS, c.v); err != nil {
			t.Fatal(err)
		}
		if got := db.HistoryRejectDays(); got != c.want {
			t.Errorf("HistoryRejectDays with %q = %d, want %d", c.v, got, c.want)
		}
	}
}
