package store

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

func mustMigrate(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

func mustCreateHost(t *testing.T, st *Store, cluster, host, space string) *Host {
	t.Helper()
	h := &Host{
		Host:         host,
		Cluster:      cluster,
		Namespace:    space,
		IngressClass: "nginx",
	}
	if err := st.CreateHost(h); err != nil {
		t.Fatalf("create host %s/%s: %v", cluster, host, err)
	}
	return h
}

func TestCreateGetHost(t *testing.T) {
	st := mustMigrate(t)
	h := mustCreateHost(t, st, "prod", "api.example.com", "team-alpha")
	if err := h.Routes.Marshal([]Route{{Path: "/api", AppName: "api-gateway", Port: 8080}}); err != nil {
		t.Fatalf("marshal routes: %v", err)
	}
	if err := h.Annotations.Marshal(map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "8m"}); err != nil {
		t.Fatalf("marshal annotations: %v", err)
	}
	if err := st.UpdateHost(h); err != nil {
		t.Fatalf("update host: %v", err)
	}

	got, err := st.GetHost("prod", "api.example.com")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	var routes []Route
	if err := got.Routes.Unmarshal(&routes); err != nil {
		t.Fatalf("unmarshal routes: %v", err)
	}
	if len(routes) != 1 || routes[0].Path != "/api" || routes[0].AppName != "api-gateway" || routes[0].Port != 8080 || routes[0].Annotations != nil {
		t.Errorf("routes = %+v, want one /api route without annotations", routes)
	}
	var ann map[string]string
	if err := got.Annotations.Unmarshal(&ann); err != nil {
		t.Fatalf("unmarshal annotations: %v", err)
	}
	if ann["nginx.ingress.kubernetes.io/proxy-body-size"] != "8m" {
		t.Errorf("annotations = %v, want proxy-body-size=8m", ann)
	}
	if got.DesiredRev != 1 || got.AppliedRev != 0 || !got.CertSynced || got.PublishedAt != nil || got.LastError != "" {
		t.Errorf("defaults not applied: %+v", got)
	}
}

func TestGetHostNotFound(t *testing.T) {
	st := mustMigrate(t)
	if _, err := st.GetHost("prod", "missing.example.com"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err = %v, want wrapped gorm.ErrRecordNotFound", err)
	}
}

func TestListHosts(t *testing.T) {
	st := mustMigrate(t)
	mustCreateHost(t, st, "prod", "a.example.com", "team-alpha")
	mustCreateHost(t, st, "staging", "b.example.com", "team-alpha")
	mustCreateHost(t, st, "prod", "c.example.com", "team-beta")

	tests := []struct {
		name    string
		space   string
		cluster string
		want    []string
	}{
		{name: "space spans all clusters", space: "team-alpha", want: []string{"a.example.com", "b.example.com"}},
		{name: "cluster filter narrows", space: "team-alpha", cluster: "prod", want: []string{"a.example.com"}},
		{name: "other space", space: "team-beta", want: []string{"c.example.com"}},
		{name: "unknown space empty", space: "team-gamma", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts, err := st.ListHosts(tt.space, tt.cluster)
			if err != nil {
				t.Fatalf("list hosts: %v", err)
			}
			var got []string
			for _, h := range hosts {
				got = append(got, h.Host)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("hosts = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("hosts = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestCreateHostDuplicateClassified(t *testing.T) {
	st := mustMigrate(t)
	mustCreateHost(t, st, "prod", "dup.example.com", "team-alpha")
	err := st.CreateHost(&Host{Host: "dup.example.com", Cluster: "prod", Namespace: "team-alpha", IngressClass: "nginx"})
	if err == nil {
		t.Fatal("duplicate create succeeded, want unique violation")
	}
	if !IsUniqueViolation(err) {
		t.Errorf("IsUniqueViolation(duplicate) = false, err = %v", err)
	}
	// Uniqueness is on the (cluster, host) pair: the same host in another
	// cluster is a distinct registration.
	if err := st.CreateHost(&Host{Host: "dup.example.com", Cluster: "staging", Namespace: "team-alpha", IngressClass: "nginx"}); err != nil {
		t.Errorf("same host in other cluster rejected: %v", err)
	}
}

func TestUniqueViolationClassification(t *testing.T) {
	st := mustMigrate(t)
	mustCreateHost(t, st, "prod", "x.example.com", "s")
	dupErr := st.CreateHost(&Host{Host: "x.example.com", Cluster: "prod", Namespace: "s", IngressClass: "nginx"})
	if dupErr == nil {
		t.Fatal("setup: duplicate create succeeded")
	}
	_, notFoundErr := st.GetHost("prod", "none.example.com")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "duplicate host create", err: dupErr, want: true},
		{name: "wrapped record not found", err: notFoundErr, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUniqueViolation(tt.err); got != tt.want {
				t.Errorf("IsUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestUpdateDeleteHost(t *testing.T) {
	st := mustMigrate(t)
	h := mustCreateHost(t, st, "prod", "gone.example.com", "team-alpha")
	h.IngressClass = "traefik"
	h.LastError = "apply failed"
	if err := st.UpdateHost(h); err != nil {
		t.Fatalf("update host: %v", err)
	}
	got, err := st.GetHost("prod", "gone.example.com")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if got.IngressClass != "traefik" || got.LastError != "apply failed" {
		t.Errorf("update not persisted: %+v", got)
	}

	deleted, err := st.DeleteHost("prod", "gone.example.com")
	if err != nil || !deleted {
		t.Fatalf("delete host: deleted=%v err=%v", deleted, err)
	}
	if again, err := st.DeleteHost("prod", "gone.example.com"); err != nil || again {
		t.Fatalf("second delete: deleted=%v err=%v, want false/nil", again, err)
	}
	if _, err := st.GetHost("prod", "gone.example.com"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err after delete = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestBumpDesiredRevTx(t *testing.T) {
	st := mustMigrate(t)
	h := mustCreateHost(t, st, "prod", "rev.example.com", "team-alpha")

	err := st.DB().Transaction(func(tx *gorm.DB) error {
		if err := BumpDesiredRevTx(tx, h.ID); err != nil {
			return err
		}
		if err := tx.Model(&Host{}).Where("id = ?", h.ID).Update("ingress_class", "traefik").Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	got, err := st.GetHost(h.Cluster, h.Host)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if got.DesiredRev != 2 {
		t.Errorf("desiredRev = %d, want 2", got.DesiredRev)
	}
}

func TestBumpDesiredRevTxRollsBackWithCaller(t *testing.T) {
	st := mustMigrate(t)
	h := mustCreateHost(t, st, "prod", "rollback.example.com", "team-alpha")

	err := st.DB().Transaction(func(tx *gorm.DB) error {
		if err := BumpDesiredRevTx(tx, h.ID); err != nil {
			return err
		}
		return errors.New("caller failure")
	})
	if err == nil {
		t.Fatal("transaction error missing")
	}
	got, err := st.GetHost(h.Cluster, h.Host)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if got.DesiredRev != 1 {
		t.Errorf("desiredRev after rollback = %d, want 1", got.DesiredRev)
	}
}

func TestAnnotationKeyCRUD(t *testing.T) {
	st := mustMigrate(t)
	created := &AnnotationKey{Key: "example.com/annotate", Description: "first", IngressClass: "nginx"}
	if err := st.CreateAnnotationKey(created); err != nil {
		t.Fatalf("create annotation key: %v", err)
	}
	if err := st.CreateAnnotationKey(&AnnotationKey{Key: "example.com/annotate", IngressClass: "nginx"}); err == nil || !IsUniqueViolation(err) {
		t.Errorf("duplicate create err = %v, want unique violation", err)
	}

	got, err := st.GetAnnotationKey(created.ID)
	if err != nil {
		t.Fatalf("get annotation key: %v", err)
	}
	if got.Description != "first" {
		t.Errorf("description = %q, want %q", got.Description, "first")
	}

	got.Description = "second"
	got.IngressClass = "traefik"
	if err := st.UpdateAnnotationKey(got); err != nil {
		t.Fatalf("update annotation key: %v", err)
	}
	updated, err := st.GetAnnotationKey(created.ID)
	if err != nil {
		t.Fatalf("reload annotation key: %v", err)
	}
	if updated.Description != "second" || updated.IngressClass != "traefik" {
		t.Errorf("update not persisted: %+v", updated)
	}

	keys, err := st.ListAnnotationKeys()
	if err != nil {
		t.Fatalf("list annotation keys: %v", err)
	}
	if len(keys) != len(seedAnnotationKeys)+1 {
		t.Errorf("catalog size = %d, want %d", len(keys), len(seedAnnotationKeys)+1)
	}

	deleted, err := st.DeleteAnnotationKey(created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete annotation key: deleted=%v err=%v", deleted, err)
	}
	if _, err := st.GetAnnotationKey(created.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err after delete = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestImportTx(t *testing.T) {
	st := mustMigrate(t)
	batch := []AnnotationKey{
		{Key: "import.example.com/new-one", Description: "new", IngressClass: "nginx"},
		{Key: seedAnnotationKeys[0].Key, Description: "collision attempt", IngressClass: "nginx"},
		{Key: "import.example.com/new-two", Description: "new too", IngressClass: "traefik"},
	}

	err := st.DB().Transaction(func(tx *gorm.DB) error {
		imported, skipped, err := ImportTx(tx, batch)
		if err != nil {
			return err
		}
		if imported != 2 || skipped != 1 {
			t.Errorf("imported=%d skipped=%d, want 2/1", imported, skipped)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("import transaction: %v", err)
	}

	var existing AnnotationKey
	if err := st.DB().Where("key = ?", seedAnnotationKeys[0].Key).First(&existing).Error; err != nil {
		t.Fatalf("load existing seed: %v", err)
	}
	if existing.Description != seedAnnotationKeys[0].Description {
		t.Errorf("existing row overwritten: description = %q", existing.Description)
	}

	var count int64
	if err := st.DB().Model(&AnnotationKey{}).Count(&count).Error; err != nil {
		t.Fatalf("count catalog: %v", err)
	}
	if count != int64(len(seedAnnotationKeys))+2 {
		t.Errorf("catalog size = %d, want %d", count, len(seedAnnotationKeys)+2)
	}
}

func TestImportTxRollsBackWithCaller(t *testing.T) {
	st := mustMigrate(t)
	err := st.DB().Transaction(func(tx *gorm.DB) error {
		if _, _, err := ImportTx(tx, []AnnotationKey{{Key: "import.example.com/dropped", IngressClass: "nginx"}}); err != nil {
			return err
		}
		return errors.New("caller failure")
	})
	if err == nil {
		t.Fatal("transaction error missing")
	}
	var count int64
	if err := st.DB().Model(&AnnotationKey{}).Where("key = ?", "import.example.com/dropped").Count(&count).Error; err != nil {
		t.Fatalf("count dropped key: %v", err)
	}
	if count != 0 {
		t.Errorf("rolled-back key persisted: count = %d", count)
	}
}
