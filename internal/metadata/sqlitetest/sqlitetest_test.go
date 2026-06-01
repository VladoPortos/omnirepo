package sqlitetest_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestNewReturnsMigratedDB(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	var name string
	if err := db.Reader.QueryRow("SELECT name FROM schema_migrations WHERE name='001_initial'").Scan(&name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "001_initial" {
		t.Fatalf("got %q, want 001_initial", name)
	}
}

func TestNewIsolatesSubtests(t *testing.T) {
	t.Parallel()
	t.Run("a", func(t *testing.T) {
		db := sqlitetest.New(t)
		if _, err := db.Writer.Exec("INSERT INTO projects(name) VALUES ('only-in-a')"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})
	t.Run("b", func(t *testing.T) {
		db := sqlitetest.New(t)
		var n int
		if err := db.Reader.QueryRow("SELECT COUNT(*) FROM projects WHERE name='only-in-a'").Scan(&n); err != nil {
			t.Fatalf("query: %v", err)
		}
		if n != 0 {
			t.Fatalf("subtest b sees subtest a's data; DBs not isolated")
		}
	})
}
