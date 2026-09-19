package presenceedge

import (
	"strings"
	"testing"
)

func TestMySQLDSNConvertsURLWithoutLeakingPassword(t *testing.T) {
	dsn, err := mysqlDSN("mysql://presence:p%40ss@mysql.data.svc.cluster.local:3306/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "presence:p@ss@tcp(mysql.data.svc.cluster.local:3306)/backoffice") {
		t.Fatalf("unexpected dsn shape: %q", dsn)
	}
}

func TestMySQLDSNRejectsNonMySQLURL(t *testing.T) {
	if _, err := mysqlDSN("postgres://user:pass@db/app"); err == nil {
		t.Fatal("non-MySQL URL accepted")
	}
}
