package main

import (
	"path/filepath"
	"testing"
)

const sampleCompose = `services:
    laravel.test:
        ports:
            - '${APP_PORT:-80}:80'
            - '${VITE_PORT:-5173}:${VITE_PORT:-5173}'
    mysql:
        ports:
            - '${FORWARD_DB_PORT:-3306}:3306'
`

func TestDetectPortVars(t *testing.T) {
	v := detectPortVars(sampleCompose)
	want := []PortVar{{"APP_PORT", 80}, {"VITE_PORT", 5173}, {"FORWARD_DB_PORT", 3306}}
	if len(v) != len(want) {
		t.Fatalf("got %v", v)
	}
	for i := range want {
		if v[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, v[i], want[i])
		}
	}
}

func TestAllocate(t *testing.T) {
	vars := []PortVar{{"APP_PORT", 80}, {"DB", 3306}}
	free := func(p int) bool { return p != 81 }
	got, err := allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{82: true}, free)
	if err != nil {
		t.Fatal(err)
	}
	if got["DB"] != 3310 || got["APP_PORT"] != 83 {
		t.Errorf("got %v", got)
	}
	// 他ワークツリーに取られた既存割当は再割当てされる
	got, _ = allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{3310: true}, func(int) bool { return true })
	if got["DB"] != 3307 {
		t.Errorf("got %v", got)
	}
}

func TestEnvSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	e := &envFile{lines: []string{"# c", "APP_PORT=80", "APP_URL=\"http://localhost\""}}
	e.Set("APP_PORT", "81")
	e.Set("NEW", "1")
	if v, _ := e.Get("APP_URL"); v != "http://localhost" {
		t.Errorf("APP_URL=%q", v)
	}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	r, _ := readEnv(p)
	if v, _ := r.Get("APP_PORT"); v != "81" {
		t.Errorf("APP_PORT=%q", v)
	}
	if v, _ := r.Get("NEW"); v != "1" {
		t.Errorf("NEW=%q", v)
	}
}
