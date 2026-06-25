package diskinfo

import "testing"

func TestGet(t *testing.T) {
	u, err := Get(".")
	if err != nil {
		t.Fatal(err)
	}
	if u.Total == 0 {
		t.Fatal("Total=0")
	}
	if u.Free > u.Total {
		t.Fatalf("Free(%d) > Total(%d)", u.Free, u.Total)
	}
	if p := u.UsedPercent(); p < 0 || p > 100 {
		t.Fatalf("UsedPercent=%.2f вне [0,100]", p)
	}
	if p := u.FreePercent(); p < 0 || p > 100 {
		t.Fatalf("FreePercent=%.2f вне [0,100]", p)
	}
}

func TestGetMissing(t *testing.T) {
	if _, err := Get("/no/such/path/xyzzy"); err == nil {
		t.Fatal("ожидалась ошибка для несуществующего пути")
	}
}
