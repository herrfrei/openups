// Package nut implements a read-only subset of the NUT network protocol (upsd).
package nut

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const idleTimeout = 5 * time.Minute

type UPSInfo struct{ Name, Desc string }

type VarGetter interface {
	ListUPS() []UPSInfo
	// Vars returns false for an unknown UPS. The map must not be modified.
	Vars(ups string) (map[string]string, bool)
	Stale(ups string) bool
}

type Server struct {
	Addr    string
	State   VarGetter
	Version string
}

func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	log.Printf("nut: listening on %s", ln.Addr())
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("nut: accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 1024), 4096)
	w := bufio.NewWriter(c)
	for { // upsd sends no greeting; the client speaks first
		_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
		if !sc.Scan() {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		args, err := splitArgs(line)
		if err != nil || len(args) == 0 {
			fmt.Fprint(w, "ERR INVALID-ARGUMENT\n")
			if w.Flush() != nil {
				return
			}
			continue
		}
		quit := s.exec(w, args)
		if w.Flush() != nil || quit {
			return
		}
	}
}

// splitArgs splits on whitespace and understands "quoted \"args\"".
func splitArgs(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inQ && c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == '"':
			inQ, have = !inQ, true
		case !inQ && (c == ' ' || c == '\t'):
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if inQ {
		return nil, errors.New("unterminated quote")
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

// quote escapes like upsd: only backslash and double quote.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (s *Server) findUPS(name string) (UPSInfo, bool) {
	for _, u := range s.State.ListUPS() {
		if u.Name == name {
			return u, true
		}
	}
	return UPSInfo{}, false
}

// vars returns the variables, or an ERR code.
func (s *Server) vars(name string) (map[string]string, string) {
	v, ok := s.State.Vars(name)
	if !ok {
		return nil, "UNKNOWN-UPS"
	}
	if s.State.Stale(name) {
		return nil, "DATA-STALE"
	}
	return v, ""
}

func (s *Server) exec(w *bufio.Writer, a []string) (quit bool) {
	fail := func(code string) { fmt.Fprintf(w, "ERR %s\n", code) }
	switch strings.ToUpper(a[0]) {
	case "HELP":
		fmt.Fprint(w, "Commands: HELP VER NETVER GET LIST SET INSTCMD LOGIN LOGOUT USERNAME PASSWORD STARTTLS\n")
	case "VER", "VERSION":
		fmt.Fprintf(w, "Network UPS Tools upsd-compatible openupsd %s\n", s.Version)
	case "NETVER":
		fmt.Fprint(w, "1.3\n")
	case "USERNAME", "PASSWORD":
		fmt.Fprint(w, "OK\n") // read-only: credentials are accepted but grant nothing
	case "LOGIN":
		if len(a) != 2 {
			fail("INVALID-ARGUMENT")
		} else if _, ok := s.findUPS(a[1]); !ok {
			fail("UNKNOWN-UPS")
		} else {
			fmt.Fprint(w, "OK\n")
		}
	case "LOGOUT":
		fmt.Fprint(w, "OK Goodbye\n")
		return true
	case "STARTTLS":
		fail("FEATURE-NOT-CONFIGURED")
	case "SET", "INSTCMD", "PRIMARY", "MASTER", "FSD":
		fail("ACCESS-DENIED")
	case "GET":
		s.get(w, a[1:], fail)
	case "LIST":
		s.list(w, a[1:], fail)
	default:
		fail("UNKNOWN-COMMAND")
	}
	return false
}

func (s *Server) get(w *bufio.Writer, a []string, fail func(string)) {
	if len(a) < 2 {
		fail("INVALID-ARGUMENT")
		return
	}
	sub, name := strings.ToUpper(a[0]), a[1]
	switch sub {
	case "UPSDESC", "NUMLOGINS":
		if len(a) != 2 {
			fail("INVALID-ARGUMENT")
			return
		}
		u, ok := s.findUPS(name)
		if !ok {
			fail("UNKNOWN-UPS")
		} else if sub == "UPSDESC" {
			fmt.Fprintf(w, "UPSDESC %s %s\n", name, quote(u.Desc))
		} else {
			fmt.Fprintf(w, "NUMLOGINS %s 0\n", name)
		}
	case "VAR", "TYPE", "DESC":
		if len(a) != 3 {
			fail("INVALID-ARGUMENT")
			return
		}
		vars, code := s.vars(name)
		if code != "" {
			fail(code)
			return
		}
		val, ok := vars[a[2]]
		if !ok {
			fail("VAR-NOT-SUPPORTED")
			return
		}
		switch sub {
		case "VAR":
			fmt.Fprintf(w, "VAR %s %s %s\n", name, a[2], quote(val))
		case "TYPE":
			typ := "STRING:64"
			if _, err := strconv.ParseFloat(val, 64); err == nil {
				typ = "NUMBER"
			}
			fmt.Fprintf(w, "TYPE %s %s %s\n", name, a[2], typ)
		case "DESC":
			fmt.Fprintf(w, "DESC %s %s %s\n", name, a[2], quote("Description unavailable"))
		}
	default:
		fail("INVALID-ARGUMENT")
	}
}

func (s *Server) list(w *bufio.Writer, a []string, fail func(string)) {
	if len(a) == 0 {
		fail("INVALID-ARGUMENT")
		return
	}
	sub := strings.ToUpper(a[0])
	if sub == "UPS" {
		fmt.Fprint(w, "BEGIN LIST UPS\n")
		for _, u := range s.State.ListUPS() {
			fmt.Fprintf(w, "UPS %s %s\n", u.Name, quote(u.Desc))
		}
		fmt.Fprint(w, "END LIST UPS\n")
		return
	}
	if len(a) < 2 {
		fail("INVALID-ARGUMENT")
		return
	}
	name := a[1]
	switch sub {
	case "VAR":
		if len(a) != 2 {
			fail("INVALID-ARGUMENT")
			return
		}
		vars, code := s.vars(name)
		if code != "" {
			fail(code)
			return
		}
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "BEGIN LIST VAR %s\n", name)
		for _, k := range keys {
			fmt.Fprintf(w, "VAR %s %s %s\n", name, k, quote(vars[k]))
		}
		fmt.Fprintf(w, "END LIST VAR %s\n", name)
	case "RW", "CMD", "CLIENT": // nothing writable, no commands, no client tracking
		if len(a) != 2 {
			fail("INVALID-ARGUMENT")
		} else if _, ok := s.findUPS(name); !ok {
			fail("UNKNOWN-UPS")
		} else {
			fmt.Fprintf(w, "BEGIN LIST %s %s\nEND LIST %s %s\n", sub, name, sub, name)
		}
	case "ENUM", "RANGE":
		if len(a) != 3 {
			fail("INVALID-ARGUMENT")
		} else if _, ok := s.findUPS(name); !ok {
			fail("UNKNOWN-UPS")
		} else {
			fail("VAR-NOT-SUPPORTED")
		}
	default:
		fail("INVALID-ARGUMENT")
	}
}
