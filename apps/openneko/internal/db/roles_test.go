package db

import (
	"strings"
	"testing"
)

func TestServeRolesAreNamedAfterTheDatabase(t *testing.T) {
	if got := FilesRole("neko"); got != "neko_files_neko" {
		t.Fatalf("FilesRole(neko) = %q", got)
	}
	if got := SubscriptionsRole("shop_2"); got != "neko_subscriptions_shop_2" {
		t.Fatalf("SubscriptionsRole(shop_2) = %q", got)
	}
	long := strings.Repeat("a", 60)
	for _, database := range []string{long, "My-Shop", "店舗", long + "b"} {
		for _, role := range []string{FilesRole(database), SubscriptionsRole(database)} {
			if len(role) > 63 || !plainRoleSuffix.MatchString(role) || !IsServeRole(role) {
				t.Fatalf("%q -> %q", database, role)
			}
		}
	}
	if SubscriptionsRole(long) == SubscriptionsRole(long+"b") || FilesRole("my-shop") == FilesRole("my_shop") {
		t.Fatal("two databases share a role name")
	}
	if got := SubscriptionsRole("My-Shop"); !strings.HasPrefix(got, "neko_subscriptions_my_shop_") || len(got) != len("neko_subscriptions_my_shop_")+8 {
		t.Fatalf("SubscriptionsRole(My-Shop) = %q", got)
	}
	for name, want := range map[string]bool{"neko_files": true, "neko_subscriptions_x": true, "neko": false, "neko_filesx": false, "reporting": false} {
		if IsServeRole(name) != want {
			t.Fatalf("IsServeRole(%q) != %v", name, want)
		}
	}
}
