package nut

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type fake struct{ stale bool }

func (f fake) ListUPS() []UPSInfo { return []UPSInfo{{"openups", "Test UPS"}} }
func (f fake) Vars(n string) (map[string]string, bool) {
	if n != "openups" {
		return nil, false
	}
	return map[string]string{
		"ups.status": "OL", "battery.voltage": "19.96", "device.model": `Open "UPS"`,
	}, true
}
func (f fake) Stale(string) bool { return f.stale }

func ask(t *testing.T, g VarGetter, in string, lines int) []string {
	t.Helper()
	cl, sv := net.Pipe()
	defer cl.Close()
	s := &Server{State: g}
	go s.handle(sv)
	_ = cl.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(cl, in+"\n"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(cl)
	var out []string
	for i := 0; i < lines; i++ {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("%q: read line %d: %v", in, i, err)
		}
		out = append(out, strings.TrimRight(l, "\r\n"))
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestListUPS(t *testing.T) {
	eq(t, ask(t, fake{}, "LIST UPS", 3),
		"BEGIN LIST UPS", `UPS openups "Test UPS"`, "END LIST UPS")
}

func TestGetVarAndQuoting(t *testing.T) {
	eq(t, ask(t, fake{}, "GET VAR openups ups.status", 1), `VAR openups ups.status "OL"`)
	eq(t, ask(t, fake{}, "GET VAR openups device.model", 1), `VAR openups device.model "Open \"UPS\""`)
}

func TestListVarSorted(t *testing.T) {
	eq(t, ask(t, fake{}, "LIST VAR openups", 5),
		"BEGIN LIST VAR openups",
		`VAR openups battery.voltage "19.96"`,
		`VAR openups device.model "Open \"UPS\""`,
		`VAR openups ups.status "OL"`,
		"END LIST VAR openups")
}

func TestErrors(t *testing.T) {
	eq(t, ask(t, fake{}, "GET VAR nope x", 1), "ERR UNKNOWN-UPS")
	eq(t, ask(t, fake{}, "GET VAR openups nope", 1), "ERR VAR-NOT-SUPPORTED")
	eq(t, ask(t, fake{stale: true}, "GET VAR openups ups.status", 1), "ERR DATA-STALE")
	eq(t, ask(t, fake{}, "SET VAR openups x 1", 1), "ERR ACCESS-DENIED")
	eq(t, ask(t, fake{}, "BOGUS", 1), "ERR UNKNOWN-COMMAND")
}

func TestSplitArgs(t *testing.T) {
	a, err := splitArgs(`SET VAR ups "a \"b\" c" x`)
	if err != nil || len(a) != 5 || a[3] != `a "b" c` {
		t.Fatalf("%q %v", a, err)
	}
	if _, err := splitArgs(`GET "oops`); err == nil {
		t.Fatal("unterminated quote must fail")
	}
}
